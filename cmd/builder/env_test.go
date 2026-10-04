package main

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/MobAI-App/ios-builder/internal/config"
)

// writeSecretProject writes a builder.json with all three providers, a
// top-level env and two profiles into a fresh working directory.
func writeSecretProject(t *testing.T) {
	t.Helper()
	t.Chdir(t.TempDir())
	cfg := &config.Config{
		Project: "App", Platform: "ios", GitHub: config.GitHubConfig{Owner: "o", Repo: "r"},
		Codemagic: config.CIConfig{AppID: "cm-app", Branch: "main"},
		Bitrise:   config.CIConfig{AppID: "br-app", Branch: "main"},
		Env:       map[string]string{"API_URL": "https://api.example.com"},
		Profiles: map[string]config.Profile{
			"production": {Distribution: "store", Provider: "codemagic"},
			"staging":    {Distribution: "internal"},
		},
	}
	if err := config.NewManager().Save(cfg); err != nil {
		t.Fatal(err)
	}
}

func loadSaved(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.NewManager().Load()
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestEnvCommands(t *testing.T) {
	writeSecretProject(t)
	if _, _, err := run(t, "env", "set", "FEATURE", "on"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, "env", "set", "API_URL", "https://staging.example.com", "--profile", "staging"); err != nil {
		t.Fatal(err)
	}
	cfg := loadSaved(t)
	if cfg.Env["FEATURE"] != "on" || cfg.Profiles["staging"].Env["API_URL"] != "https://staging.example.com" || cfg.Profiles["staging"].Distribution != "internal" {
		t.Fatalf("saved %+v", cfg)
	}
	for name, args := range map[string][]string{
		"reserved":        {"env", "set", "SCHEME", "x"},
		"bad name":        {"env", "set", "A-B", "x"},
		"unknown profile": {"env", "set", "A", "x", "--profile", "nightly"},
		"not set":         {"env", "unset", "NOPE"},
	} {
		if _, _, err := run(t, args...); err == nil {
			t.Errorf("%s accepted", name)
		}
	}

	// With a profile: what its builds get, and where each value comes from.
	out, _, err := run(t, "env", "list", "--profile", "staging", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var entries []envEntry
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	want := []envEntry{{Name: "API_URL", Value: "https://staging.example.com", Profile: "staging"}, {Name: "FEATURE", Value: "on"}}
	if !reflect.DeepEqual(entries, want) {
		t.Fatalf("list --profile:\n got %+v\nwant %+v", entries, want)
	}
	out, _, err = run(t, "env", "list")
	if err != nil || !strings.Contains(out, "https://api.example.com") || !strings.Contains(out, "profile staging") {
		t.Fatalf("list: %v\n%s", err, out)
	}

	if _, _, err := run(t, "env", "unset", "API_URL", "--profile", "staging"); err != nil {
		t.Fatal(err)
	}
	if loadSaved(t).Profiles["staging"].Env != nil {
		t.Fatal("unset left the profile env")
	}
	data, _ := os.ReadFile("builder.json")
	if !strings.Contains(string(data), `"FEATURE": "on"`) {
		t.Fatalf("builder.json:\n%s", data)
	}
}
