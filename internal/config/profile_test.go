package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func boolPtr(b bool) *bool { return &b }

func profileConfig() *Config {
	return &Config{
		Provider: "github",
		IOS:      IOSConfig{Path: "ios", Scheme: "Top", Signing: true, Configuration: "Debug"},
		Profiles: map[string]Profile{
			"development": {Configuration: "Debug", Signing: boolPtr(false)},
			"preview":     {Configuration: "Release", Env: map[string]string{"API_URL": "https://staging.example.com"}},
			"production":  {Configuration: "Release", Scheme: "MyApp", Provider: "codemagic", Distribution: "app-store"},
		},
	}
}

func TestResolveProfile(t *testing.T) {
	cfg := profileConfig()
	for _, tt := range []struct {
		name, profile string
		want          BuildSettings
	}{
		{"no profile keeps top-level settings", "", BuildSettings{Configuration: "Debug", Scheme: "Top", Signing: true, Provider: "github"}},
		{"false overrides true", "development", BuildSettings{Profile: "development", Configuration: "Debug", Scheme: "Top", Signing: false, Provider: "github"}},
		{"unset fields inherit", "preview", BuildSettings{Profile: "preview", Configuration: "Release", Scheme: "Top", Signing: true, Provider: "github", Env: map[string]string{"API_URL": "https://staging.example.com"}}},
		{"every field overrides", "production", BuildSettings{Profile: "production", Configuration: "Release", Scheme: "MyApp", Signing: true, Provider: "codemagic", Distribution: "app-store"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := cfg.ResolveProfile(tt.profile)
			if err != nil {
				t.Fatal(err)
			}
			if got.Profile != tt.want.Profile || got.Configuration != tt.want.Configuration || got.Scheme != tt.want.Scheme ||
				got.Signing != tt.want.Signing || got.Provider != tt.want.Provider || got.Distribution != tt.want.Distribution ||
				len(got.Env) != len(tt.want.Env) || got.Env["API_URL"] != tt.want.Env["API_URL"] {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestResolveProfileDefault(t *testing.T) {
	cfg := profileConfig()
	cfg.DefaultProfile = "preview"
	s, err := cfg.ResolveProfile("")
	if err != nil || s.Profile != "preview" || s.Configuration != "Release" {
		t.Fatalf("default profile not applied: %+v %v", s, err)
	}
	// An explicit --profile beats defaultProfile.
	s, err = cfg.ResolveProfile("production")
	if err != nil || s.Profile != "production" {
		t.Fatalf("explicit profile lost to default: %+v %v", s, err)
	}
	cfg.DefaultProfile = "nightly"
	if _, err := cfg.ResolveProfile(""); err == nil || !strings.Contains(err.Error(), `defaultProfile "nightly"`) {
		t.Fatalf("unknown defaultProfile accepted or not named as the source: %v", err)
	}
}

func TestResolveProfileErrors(t *testing.T) {
	cfg := profileConfig()
	_, err := cfg.ResolveProfile("staging")
	if err == nil || !strings.Contains(err.Error(), "development, preview, production") {
		t.Fatalf("unknown profile should list the available names: %v", err)
	}
	if _, err := (&Config{}).ResolveProfile("staging"); err == nil || !strings.Contains(err.Error(), "no profiles") {
		t.Fatalf("missing profiles section: %v", err)
	}
	for name, p := range map[string]Profile{
		"bad distribution": {Distribution: "adhoc"},
		"bad env name":     {Env: map[string]string{"API-URL": "x"}},
		"env with equals":  {Env: map[string]string{"A=B": "x"}},
		"reserved env":     {Env: map[string]string{"SCHEME": "Other"}},
		"reserved secret":  {Env: map[string]string{"IOS_CERTIFICATE": "x"}},
		"reserved PATH":    {Env: map[string]string{"PATH": "/tmp"}},
		"GitHub namespace": {Env: map[string]string{"GITHUB_TOKEN": "x"}},
		"Codemagic space":  {Env: map[string]string{"CM_BUILD_ID": "x"}},
		"Bitrise space":    {Env: map[string]string{"BITRISE_GIT_BRANCH": "x"}},
	} {
		cfg.Profiles["bad"] = p
		if _, err := cfg.ResolveProfile("bad"); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestProfileJSONRoundTrip(t *testing.T) {
	raw := `{"project":"App","github":{"owner":"o","repo":"r"},"defaultProfile":"preview",
	  "profiles":{"preview":{"configuration":"Release","signing":false,"env":{"API_URL":"https://staging.example.com"},"distribution":"ad-hoc"}}}`
	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	p := cfg.Profiles["preview"]
	if p.Signing == nil || *p.Signing || p.Distribution != "ad-hoc" || cfg.DefaultProfile != "preview" {
		t.Fatalf("parsed %+v", cfg)
	}
	out, err := json.Marshal(&Config{Project: "App"})
	if err != nil || strings.Contains(string(out), "profiles") || strings.Contains(string(out), "defaultProfile") {
		t.Fatalf("empty profiles should be omitted: %s %v", out, err)
	}
}

func TestProfileEncodings(t *testing.T) {
	s := BuildSettings{}
	if s.EnvJSON() != "" || s.ProfileInput() != "" {
		t.Fatal("no profile must produce no inputs")
	}
	s = BuildSettings{Profile: "preview", Env: map[string]string{"MSG": "line one\nline \"two\""}, Distribution: "ad-hoc"}
	var env map[string]string
	if err := json.Unmarshal([]byte(s.EnvJSON()), &env); err != nil || env["MSG"] != s.Env["MSG"] {
		t.Fatalf("env encoding: %q %v", s.EnvJSON(), err)
	}
	var input struct {
		Name         string
		Env          map[string]string
		Distribution string
	}
	if err := json.Unmarshal([]byte(s.ProfileInput()), &input); err != nil || input.Name != "preview" || input.Distribution != "ad-hoc" || input.Env["MSG"] != s.Env["MSG"] {
		t.Fatalf("profile input: %q %v", s.ProfileInput(), err)
	}
	if strings.Contains(s.ProfileInput(), "\n") {
		t.Fatal("profile input must be a single line")
	}
	noEnv := BuildSettings{Profile: "development"}
	if got := noEnv.ProfileInput(); !strings.Contains(got, `"env":{}`) {
		t.Fatalf("env should be an object even when empty: %s", got)
	}
}
