package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MobAI-App/ios-builder/internal/builds"
	"github.com/MobAI-App/ios-builder/internal/config"
)

// stubSource is a provider history in memory; the provider fakes over HTTP
// live in internal/builds.
type stubSource struct {
	name      string
	list      []builds.Build
	calls     []string
	cancelled []string
}

func (s *stubSource) Provider() string { return s.name }

func (s *stubSource) List(_ context.Context, opts builds.ListOptions) ([]builds.Build, error) {
	s.calls = append(s.calls, fmt.Sprintf("list %d %s", opts.Limit, opts.Status))
	return s.list, nil
}

func (s *stubSource) Find(_ context.Context, ref string) (builds.Build, error) {
	s.calls = append(s.calls, "find "+ref)
	for i := range s.list {
		b := s.list[i]
		if b.ID == ref || b.RunID == ref || b.URL == ref {
			return b, nil
		}
	}
	return builds.Build{}, fmt.Errorf("no build %s", ref)
}

func (s *stubSource) Show(_ context.Context, b *builds.Build) (builds.Detail, error) {
	return builds.Detail{Build: *b, Jobs: []builds.Job{{Name: "build", Status: "failure", Steps: []builds.Step{{Number: 1, Name: "Build IPA", Status: "failure"}}}},
		Failure: &builds.Failure{Job: "build", Step: "Build IPA", Messages: []string{"No profile found"}}}, nil
}

type stubLogs struct{ failed bool }

func (l stubLogs) Read(_ context.Context, w io.Writer) (bool, error) {
	fmt.Fprintf(w, "log failed=%v\n", l.failed)
	return true, nil
}

func (s *stubSource) Logs(_ *builds.Build, failed bool) builds.LogStream { return stubLogs{failed} }

func (s *stubSource) Download(_ context.Context, b *builds.Build, dir string) (string, int64, error) {
	return filepath.Join(dir, "App-"+b.ID+".ipa"), 2 << 20, nil
}

func (s *stubSource) Cancel(_ context.Context, b *builds.Build) error {
	s.cancelled = append(s.cancelled, b.RunID)
	return nil
}

func stubBuilds(t *testing.T) map[string]*stubSource {
	t.Helper()
	chdir(t)
	cfg := &config.Config{Project: "App", GitHub: config.GitHubConfig{Owner: "o", Repo: "r"},
		Codemagic: config.CIConfig{AppID: "cm", Branch: "main"}, Bitrise: config.CIConfig{AppID: "br", Branch: "main"}}
	if err := config.NewManager().Save(cfg); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	end := start.Add(7 * time.Minute)
	sources := map[string]*stubSource{}
	for _, name := range []string{"github", "codemagic", "bitrise"} {
		sources[name] = &stubSource{name: name, list: []builds.Build{
			{ID: "1234abcd", Kind: builds.KindIPA, Status: builds.StatusRunning, State: "in_progress", Provider: name, RunID: "30", CreatedAt: start},
			{ID: "deadbeef", Kind: builds.KindIPA, Profile: "store", Status: builds.StatusFailed, State: "failure", Provider: name, RunID: "20",
				URL: "https://app.bitrise.io/build/20", CreatedAt: start, StartedAt: &start, FinishedAt: &end},
		}}
	}
	old := newBuildsSource
	newBuildsSource = func(_ *config.Config, name string) (builds.Source, error) { return sources[name], nil }
	t.Cleanup(func() { newBuildsSource = old })
	return sources
}

func TestBuildsList(t *testing.T) {
	src := stubBuilds(t)
	stdout, stderr, err := run(t, "builds", "--status", "failed", "--limit", "5")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if src["github"].calls[0] != "list 5 failed" {
		t.Fatalf("calls: %v", src["github"].calls)
	}
	for _, want := range []string{"BUILD ID", "deadbeef", "store", "7m0s", "https://app.bitrise.io/build/20", "running (in_progress)"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("list lacks %q:\n%s", want, stdout)
		}
	}

	stdout, _, err = run(t, "builds", "--json", "--provider", "codemagic")
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil || len(got) != 2 {
		t.Fatalf("json: %v\n%s", err, stdout)
	}
	if got[1]["provider"] != "codemagic" || got[1]["duration_seconds"] != float64(420) || got[1]["profile"] != "store" {
		t.Fatalf("json fields: %v", got[1])
	}

	if _, _, err := run(t, "builds", "--status", "done"); err == nil {
		t.Error("an unknown --status was accepted")
	}
}

func TestBuildsShowLogsDownload(t *testing.T) {
	src := stubBuilds(t)
	stdout, _, err := run(t, "builds", "show", "deadbeef")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Build deadbeef (ipa, profile store)", "Failed step: Build IPA (job build)", "No profile found", "Artifacts: none"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("show lacks %q:\n%s", want, stdout)
		}
	}
	// A run URL picks its provider.
	stdout, _, err = run(t, "builds", "show", "https://app.bitrise.io/build/20", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var detail map[string]any
	if err := json.Unmarshal([]byte(stdout), &detail); err != nil || detail["id"] != "deadbeef" || detail["duration_seconds"] != float64(420) ||
		detail["failure"] == nil || detail["artifacts"] == nil || detail["provider"] != "bitrise" {
		t.Fatalf("show --json: %v\n%s", err, stdout)
	}
	if len(src["bitrise"].calls) != 1 || len(src["github"].calls) != 1 {
		t.Fatalf("URL routing: github %v bitrise %v", src["github"].calls, src["bitrise"].calls)
	}

	stdout, _, err = run(t, "builds", "logs", "20", "--failed")
	if err != nil || stdout != "log failed=true\n" {
		t.Fatalf("logs: %q %v", stdout, err)
	}

	stdout, _, err = run(t, "builds", "download", "deadbeef", "-o", "out")
	if err != nil || !strings.Contains(stdout, filepath.Join("out", "App-deadbeef.ipa")) || !strings.Contains(stdout, "2.00 MB") {
		t.Fatalf("download: %q %v", stdout, err)
	}
}

func TestBuildsCancelAndIOSCancelAlias(t *testing.T) {
	src := stubBuilds(t)
	stdout, _, err := run(t, "builds", "cancel", "1234abcd")
	if err != nil || !strings.Contains(stdout, "Cancellation requested") || len(src["github"].cancelled) != 1 {
		t.Fatalf("cancel: %q %v %v", stdout, err, src["github"].cancelled)
	}
	stdout, _, err = run(t, "builds", "cancel", "deadbeef")
	if err != nil || !strings.Contains(stdout, "already ended") || len(src["github"].cancelled) != 1 {
		t.Fatalf("cancel of a finished build: %q %v", stdout, err)
	}

	// The old command still works, and a Codemagic run ID goes straight to
	// the cancel API without a lookup.
	stdout, _, err = run(t, "ios", "cancel", "--provider", "codemagic", "--run-id", "cm-run")
	if err != nil || !strings.Contains(stdout, "has stopped") {
		t.Fatalf("ios cancel: %q %v", stdout, err)
	}
	if got := src["codemagic"]; len(got.cancelled) != 1 || got.cancelled[0] != "cm-run" || len(got.calls) != 0 {
		t.Fatalf("ios cancel: cancelled %v calls %v", got.cancelled, got.calls)
	}
	if _, _, err := run(t, "ios", "cancel", "--provider", "codemagic"); err == nil {
		t.Error("ios cancel without --run-id")
	}
}
