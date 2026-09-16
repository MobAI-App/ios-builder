package build

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/MobAI-App/ios-builder/internal/config"
)

func profiledConfig() *config.Config {
	signed := true
	return &config.Config{
		Project: "App",
		GitHub:  config.GitHubConfig{Owner: "owner", Repo: "repo"},
		IOS:     config.IOSConfig{Path: "ios", Scheme: "App", Configuration: "Debug"},
		Flutter: config.FlutterConfig{Version: "3.24.0"},
		Profiles: map[string]config.Profile{
			"preview": {
				Configuration: "Release", Signing: &signed, Scheme: "AppPreview", Distribution: "ad-hoc",
				Env: map[string]string{"API_URL": "https://staging.example.com", "FLAGS": "a b"},
			},
			"ci": {Provider: "codemagic"},
		},
		Codemagic: config.CIConfig{AppID: "app", Branch: "main"},
	}
}

func TestSettingsPrecedence(t *testing.T) {
	c := NewCoordinatorWithOutput(profiledConfig(), nil, io.Discard)
	s, name, err := c.settings("", "", false)
	if err != nil || name != "github" || s.Profile != "" || s.Configuration != "Debug" || s.Signing {
		t.Fatalf("top-level settings: %+v %s %v", s, name, err)
	}
	s, name, err = c.settings("preview", "", false)
	if err != nil || name != "github" || !s.Signing || s.Configuration != "Release" {
		t.Fatalf("profile settings: %+v %s %v", s, name, err)
	}
	// --unsigned beats the profile's signing.
	if s, _, err = c.settings("preview", "", true); err != nil || s.Signing {
		t.Fatalf("--unsigned ignored: %+v %v", s, err)
	}
	// The profile's provider applies, and --provider beats it.
	if _, name, err = c.settings("ci", "", false); err != nil || name != "codemagic" {
		t.Fatalf("profile provider: %s %v", name, err)
	}
	if _, name, err = c.settings("ci", "bitrise", false); err != nil || name != "bitrise" {
		t.Fatalf("--provider ignored: %s %v", name, err)
	}
	if _, _, err = c.settings("nope", "", false); err == nil || !strings.Contains(err.Error(), "ci, preview") {
		t.Fatalf("unknown profile: %v", err)
	}
}

func TestGitHubInputsMapping(t *testing.T) {
	c := NewCoordinatorWithOutput(profiledConfig(), nil, io.Discard)

	s, _, _ := c.settings("", "", false)
	got := c.buildInputs("abcdef12", "refs/ios-builder/jobs/abcdef12", s)
	want := map[string]string{
		"build_id": "abcdef12", "snapshot_ref": "refs/ios-builder/jobs/abcdef12",
		"ios_path": "ios", "scheme": "App", "configuration": "Debug", "flutter_version": "3.24.0",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("without a profile the inputs must be unchanged:\n got %v\nwant %v", got, want)
	}

	s, _, _ = c.settings("preview", "", false)
	got = c.buildInputs("abcdef12", "ref", s)
	if got["scheme"] != "AppPreview" || got["configuration"] != "Release" || got["use_signing"] != "true" {
		t.Fatalf("profile not mapped: %v", got)
	}
	var profile struct {
		Name         string
		Env          map[string]string
		Distribution string
	}
	if err := json.Unmarshal([]byte(got["profile"]), &profile); err != nil {
		t.Fatalf("profile input is not JSON: %q %v", got["profile"], err)
	}
	if profile.Name != "preview" || profile.Distribution != "ad-hoc" || profile.Env["FLAGS"] != "a b" || len(profile.Env) != 2 {
		t.Fatalf("profile input: %+v", profile)
	}
	if len(got) > 10 {
		t.Fatalf("workflow_dispatch allows at most 10 inputs, sending %d", len(got))
	}

	share := c.workflowInputs("abcdef12", "ref", s)
	for _, k := range []string{"use_signing", "configuration"} {
		if _, ok := share[k]; ok {
			t.Fatalf("simulator workflow does not declare %s", k)
		}
	}
	if share["profile"] == "" || share["scheme"] != "AppPreview" {
		t.Fatalf("share inputs: %v", share)
	}
}

func TestRemoteInputsMapping(t *testing.T) {
	c := NewCoordinatorWithOutput(profiledConfig(), nil, io.Discard)

	s, _, _ := c.settings("", "", false)
	got := c.inputs("abcdef12", "ref", "sha", s)
	for _, k := range []string{"BUILD_ENV", "DISTRIBUTION"} {
		if _, ok := got[k]; ok {
			t.Fatalf("%s must be absent without a profile: %v", k, got)
		}
	}
	if got["USE_SIGNING"] != "false" || got["SCHEME"] != "App" || got["CONFIGURATION"] != "Debug" {
		t.Fatalf("top-level mapping: %v", got)
	}

	s, _, _ = c.settings("preview", "", false)
	got = c.inputs("abcdef12", "ref", "sha", s)
	if got["USE_SIGNING"] != "true" || got["SCHEME"] != "AppPreview" || got["CONFIGURATION"] != "Release" || got["DISTRIBUTION"] != "ad-hoc" {
		t.Fatalf("profile mapping: %v", got)
	}
	var env map[string]string
	if err := json.Unmarshal([]byte(got["BUILD_ENV"]), &env); err != nil || env["API_URL"] != "https://staging.example.com" {
		t.Fatalf("BUILD_ENV: %q %v", got["BUILD_ENV"], err)
	}
}

func TestSettingsPrinted(t *testing.T) {
	var out bytes.Buffer
	p := NewProgress(&out)
	p.Start("abcdef12")
	p.Settings(config.BuildSettings{Profile: "preview", Configuration: "Release", Signing: true, Env: map[string]string{"B": "2", "A": "1"}, Distribution: "ad-hoc"}, "github")
	for _, want := range []string{"Profile:       preview", "Configuration: Release", "Scheme:        (auto-detected)", "Signing:       signed", "Provider:      github", "Env:           A, B", "Distribution:  ad-hoc"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "staging") || strings.Contains(out.String(), "=1") {
		t.Fatal("env values should not be printed, only names")
	}
}
