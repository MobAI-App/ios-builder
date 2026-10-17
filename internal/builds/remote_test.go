package builds

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/MobAI-App/ios-builder/internal/ci"
	"github.com/MobAI-App/ios-builder/internal/config"
)

var ciConfig = config.CIConfig{AppID: "app", Branch: "main", BuildWorkflow: "ios-build", ShareWorkflow: "ios-share"}

// codemagicFake serves the builds API, the v3 status API, step logs and
// artifact files from one TLS server, since artifact URLs must be HTTPS.
type codemagicFake struct {
	srv       *httptest.Server
	mu        sync.Mutex
	cancelled bool
}

func newCodemagicFake(t *testing.T) *codemagicFake {
	f := &codemagicFake{}
	ipa := ipaBytes(t)
	mux := http.NewServeMux()
	var base string
	build := func(id, workflow, status, buildID, created string, steps string) string {
		return fmt.Sprintf(`{"_id":%q,"workflowId":%q,"status":%q,"createdAt":%q,"startedAt":%q,
			"environment":{"variables":{"BUILD_ID":%q,"BUILDER_PROFILE":"store"}},"buildActions":[%s]}`,
			id, workflow, status, created, created, buildID, steps)
	}
	failedSteps := fmt.Sprintf(`{"name":"Set up","status":"success","logUrl":"%[1]s/builds/cm2/step/1"},{"name":"Build","status":"failed","logUrl":"%[1]s/builds/cm2/step/2"}`, "BASE")
	builds := func() map[string]string {
		f.mu.Lock()
		defer f.mu.Unlock()
		running := "building"
		if f.cancelled {
			running = "canceled"
		}
		return map[string]string{
			"cm1": build("cm1", "ios-build", "finished", "abcd1234", "2026-10-01T10:00:00Z", ""),
			"cm2": build("cm2", "ios-build", "failed", "deadbeef", "2026-10-01T10:10:00Z", strings.ReplaceAll(failedSteps, "BASE", base)),
			"cm3": build("cm3", "ios-build", running, "1234abcd", "2026-10-01T10:20:00Z", ""),
			"cm4": build("cm4", "ios-share", "finished", "cafef00d", "2026-10-01T10:15:00Z", ""),
		}
	}
	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("x-auth-token") != "cm-token" {
				t.Errorf("%s without the token", r.URL.Path)
			}
			h(w, r)
		}
	}
	mux.HandleFunc("GET /builds", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("appId") != "app" {
			t.Errorf("list without the app: %s", r.URL)
		}
		wf := r.URL.Query().Get("workflowId")
		var items []string
		for _, id := range []string{"cm1", "cm2", "cm3", "cm4"} {
			if b := builds()[id]; strings.Contains(b, `"workflowId":"`+wf+`"`) {
				items = append(items, b)
			}
		}
		fmt.Fprintf(w, `{"builds":[%s]}`, strings.Join(items, ","))
	}))
	mux.HandleFunc("GET /builds/{id}", auth(func(w http.ResponseWriter, r *http.Request) {
		b, ok := builds()[r.PathValue("id")]
		if !ok {
			w.WriteHeader(404)
			fmt.Fprint(w, `{"message":"build not found"}`)
			return
		}
		fmt.Fprintf(w, `{"build":%s}`, b)
	}))
	mux.HandleFunc("GET /builds/cm2/step/{n}", auth(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "step %s output", r.PathValue("n"))
	}))
	mux.HandleFunc("GET /api/v3/builds/{id}", auth(func(w http.ResponseWriter, r *http.Request) {
		status := "finished"
		if r.PathValue("id") == "cm3" {
			f.mu.Lock()
			status = map[bool]string{true: "canceled", false: "building"}[f.cancelled]
			f.mu.Unlock()
		}
		fmt.Fprintf(w, `{"data":{"status":%q,"artifacts":[{"name":"abcd1234.ipa","size_in_bytes":%d,"short_lived_download_url":"%s/files/abcd1234.ipa"}]}}`, status, len(ipa), base)
	}))
	mux.HandleFunc("GET /files/abcd1234.ipa", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-auth-token") != "" {
			t.Error("the token went to the artifact URL")
		}
		_, _ = w.Write(ipa)
	})
	mux.HandleFunc("POST /builds/cm3/cancel", auth(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.cancelled = true
		f.mu.Unlock()
		w.WriteHeader(208)
	}))
	f.srv = httptest.NewTLSServer(mux)
	base = f.srv.URL
	t.Cleanup(f.srv.Close)
	return f
}

func (f *codemagicFake) source() *Remote {
	p := ci.NewCodemagicWith(ciConfig, "cm-token", ci.Options{APIURL: f.srv.URL, StatusURL: f.srv.URL + "/api/v3", Transport: f.srv.Client().Transport})
	return &Remote{P: p, CI: ciConfig, Project: "App"}
}

func TestCodemagicHistory(t *testing.T) {
	f := newCodemagicFake(t)
	r := f.source()
	ctx := context.Background()

	all, err := r.List(ctx, ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ids(all), "1234abcd:ipa:running cafef00d:simulator:succeeded deadbeef:ipa:failed abcd1234:ipa:succeeded"; got != want {
		t.Fatalf("list:\n got %s\nwant %s", got, want)
	}
	if all[0].Profile != "store" || all[0].RunID != "cm3" || all[0].URL != "https://codemagic.io/app/app/build/cm3" {
		t.Fatalf("fields: %+v", all[0])
	}
	if got, _ := r.List(ctx, ListOptions{Status: StatusFailed}); ids(got) != "deadbeef:ipa:failed" {
		t.Fatalf("--status failed: %s", ids(got))
	}

	for ref, want := range map[string]string{"deadbeef": "cm2", "cm1": "cm1", "https://codemagic.io/app/app/build/cm4": "cm4"} {
		if b, err := r.Find(ctx, ref); err != nil || b.RunID != want {
			t.Errorf("Find(%q) = %q %v, want %s", ref, b.RunID, err, want)
		}
	}
	if _, err := r.Find(ctx, "0badf00d"); err == nil {
		t.Error("unknown build ID found")
	}

	b, _ := r.Find(ctx, "deadbeef")
	d, err := r.Show(ctx, &b)
	if err != nil || d.Failure == nil || d.Failure.Step != "Build" || len(d.Jobs) != 1 || len(d.Jobs[0].Steps) != 2 {
		t.Fatalf("show: %+v %v", d, err)
	}
	var out bytes.Buffer
	if done, err := r.Logs(&b, true).Read(ctx, &out); err != nil || !done || out.String() != "==> Build (failed)\nstep 2 output\n" {
		t.Fatalf("--failed logs: %v %v %q", done, err, out.String())
	}
	out.Reset()
	if _, err := r.Logs(&b, false).Read(ctx, &out); err != nil || !strings.Contains(out.String(), "step 1 output") {
		t.Fatalf("logs: %v %q", err, out.String())
	}

	dir := t.TempDir()
	b, _ = r.Find(ctx, "abcd1234")
	path, _, err := r.Download(ctx, &b, dir)
	if err != nil || path != filepath.Join(dir, "App-abcd1234.ipa") {
		t.Fatalf("download: %s %v", path, err)
	}
	b, _ = r.Find(ctx, "cafef00d")
	if _, _, err := r.Download(ctx, &b, dir); err == nil || !strings.Contains(err.Error(), "simulator") {
		t.Fatalf("simulator download: %v", err)
	}

	b, _ = r.Find(ctx, "1234abcd")
	if err := r.Cancel(ctx, &b); err != nil || !f.cancelled {
		t.Fatalf("cancel: %v %v", err, f.cancelled)
	}
}

// bitriseFake serves the Bitrise builds, artifacts and log APIs.
type bitriseFake struct {
	srv     *httptest.Server
	mu      sync.Mutex
	aborted bool
	reads   int
}

func newBitriseFake(t *testing.T) *bitriseFake {
	f := &bitriseFake{}
	ipa := ipaBytes(t)
	var base string
	build := func(slug, workflow string, status int, text, buildID, at string) string {
		finished := `null`
		if status != 0 {
			finished = fmt.Sprintf("%q", strings.Replace(at, ":00:00", ":05:00", 1))
		}
		return fmt.Sprintf(`{"slug":%q,"status":%d,"status_text":%q,"triggered_workflow":%q,"triggered_at":%q,"started_on_worker_at":%q,"finished_at":%s,
			"original_build_params":{"environments":[{"mapped_to":"BUILD_ID","value":%q},{"mapped_to":"BUILDER_PROFILE","value":"preview"}]}}`,
			slug, status, text, workflow, at, at, finished, buildID)
	}
	builds := func() map[string]string {
		f.mu.Lock()
		defer f.mu.Unlock()
		running := build("br3", "ios-build", 0, "in-progress", "1234abcd", "2026-10-01T12:00:00Z")
		if f.aborted {
			running = build("br3", "ios-build", 3, "aborted", "1234abcd", "2026-10-01T12:00:00Z")
		}
		return map[string]string{
			"br1": build("br1", "ios-build", 1, "success", "abcd1234", "2026-10-01T10:00:00Z"),
			"br2": build("br2", "ios-build", 2, "error", "deadbeef", "2026-10-01T11:00:00Z"),
			"br4": build("br4", "ios-share", 1, "success", "cafef00d", "2026-10-01T11:30:00Z"),
			"br3": running,
		}
	}
	mux := http.NewServeMux()
	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "br-token" {
				t.Errorf("%s without the token", r.URL.Path)
			}
			h(w, r)
		}
	}
	mux.HandleFunc("GET /apps/app/builds", auth(func(w http.ResponseWriter, r *http.Request) {
		wf := r.URL.Query().Get("workflow")
		var items []string
		for _, id := range []string{"br3", "br4", "br2", "br1"} {
			if b := builds()[id]; strings.Contains(b, `"triggered_workflow":"`+wf+`"`) {
				items = append(items, b)
			}
		}
		fmt.Fprintf(w, `{"data":[%s],"paging":{}}`, strings.Join(items, ","))
	}))
	mux.HandleFunc("GET /apps/app/builds/{slug}", auth(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"data":%s}`, builds()[r.PathValue("slug")])
	}))
	mux.HandleFunc("GET /apps/app/builds/{slug}/artifacts", auth(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"data":[{"slug":"a1","title":"abcd1234.ipa.zip","file_size_bytes":%d}],"paging":{}}`, len(ipa))
	}))
	mux.HandleFunc("GET /apps/app/builds/{slug}/artifacts/a1", auth(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"data":{"expiring_download_url":"%s/files/a1"}}`, base)
	}))
	mux.HandleFunc("GET /files/a1", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(ipa) })
	mux.HandleFunc("GET /apps/app/builds/{slug}/log", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("slug") == "br2" {
			fmt.Fprintf(w, `{"is_archived":true,"expiring_raw_log_url":"%s/files/raw-log","log_chunks":[]}`, base)
			return
		}
		f.mu.Lock()
		f.reads++
		n := f.reads
		f.mu.Unlock()
		chunks := `{"chunk":"first\n","position":0}`
		if n > 1 {
			chunks += `,{"chunk":"second\n","position":1}`
		}
		fmt.Fprintf(w, `{"is_archived":false,"log_chunks":[%s]}`, chunks)
	}))
	mux.HandleFunc("GET /files/raw-log", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "whole archived log\n")
	})
	mux.HandleFunc("POST /apps/app/builds/br3/abort", auth(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.aborted = true
		f.mu.Unlock()
		fmt.Fprint(w, `{"status":"ok"}`)
	}))
	f.srv = httptest.NewTLSServer(mux)
	base = f.srv.URL
	t.Cleanup(f.srv.Close)
	return f
}

func (f *bitriseFake) source() *Remote {
	p := ci.NewBitriseWith(ciConfig, "br-token", ci.Options{APIURL: f.srv.URL, Transport: f.srv.Client().Transport})
	return &Remote{P: p, CI: ciConfig, Project: "App"}
}

func TestBitriseHistory(t *testing.T) {
	f := newBitriseFake(t)
	r := f.source()
	ctx := context.Background()

	all, err := r.List(ctx, ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ids(all), "1234abcd:ipa:running cafef00d:simulator:succeeded deadbeef:ipa:failed abcd1234:ipa:succeeded"; got != want {
		t.Fatalf("list:\n got %s\nwant %s", got, want)
	}
	if all[3].Profile != "preview" || all[3].URL != "https://app.bitrise.io/build/br1" || all[3].Duration(all[3].CreatedAt).Minutes() != 5 {
		t.Fatalf("fields: %+v", all[3])
	}
	if got, _ := r.List(ctx, ListOptions{Status: StatusRunning}); ids(got) != "1234abcd:ipa:running" {
		t.Fatalf("--status running: %s", ids(got))
	}
	for ref, want := range map[string]string{"abcd1234": "br1", "br2": "br2", "https://app.bitrise.io/build/br3": "br3"} {
		if b, err := r.Find(ctx, ref); err != nil || b.RunID != want {
			t.Errorf("Find(%q) = %q %v, want %s", ref, b.RunID, err, want)
		}
	}

	b, _ := r.Find(ctx, "deadbeef")
	d, err := r.Show(ctx, &b)
	if err != nil || d.Failure == nil || len(d.Artifacts) != 1 || d.Artifacts[0].Name != "abcd1234.ipa" {
		t.Fatalf("show: %+v %v", d, err)
	}
	var out bytes.Buffer
	if done, err := r.Logs(&b, true).Read(ctx, &out); err != nil || !done || out.String() != "whole archived log\n" {
		t.Fatalf("archived log: %v %v %q", done, err, out.String())
	}

	// A running build: chunks are printed once each, across reads.
	b, _ = r.Find(ctx, "1234abcd")
	out.Reset()
	stream := r.Logs(&b, false)
	for i := 0; i < 2; i++ {
		if done, err := stream.Read(ctx, &out); err != nil || done {
			t.Fatalf("read %d: %v %v", i, done, err)
		}
	}
	if out.String() != "first\nsecond\n" {
		t.Fatalf("chunks: %q", out.String())
	}

	dir := t.TempDir()
	b, _ = r.Find(ctx, "abcd1234")
	if path, _, err := r.Download(ctx, &b, dir); err != nil || path != filepath.Join(dir, "App-abcd1234.ipa") {
		t.Fatalf("download: %s %v", path, err)
	}
	b, _ = r.Find(ctx, "1234abcd")
	if _, _, err := r.Download(ctx, &b, dir); err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("running download: %v", err)
	}
	if err := r.Cancel(ctx, &b); err != nil || !f.aborted {
		t.Fatalf("cancel: %v %v", err, f.aborted)
	}
}

func TestRunIDFromURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://codemagic.io/app/app/build/65f0":      "65f0",
		"https://app.bitrise.io/build/1f2e-3d4c":       "1f2e-3d4c",
		"https://app.bitrise.io/build/1f2e-3d4c?tab=x": "1f2e-3d4c",
	} {
		if got, ok := runIDFromURL(in); !ok || got != want {
			t.Errorf("runIDFromURL(%q) = %q %v", in, got, ok)
		}
	}
	if ProviderFromURL("https://github.com/o/r/actions/runs/1") != "github" || ProviderFromURL("abcd1234") != "" {
		t.Error("ProviderFromURL")
	}
}
