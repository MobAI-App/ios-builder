package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func profileConfig() *Config {
	return &Config{
		Provider: "github",
		IOS:      IOSConfig{Path: "ios", Scheme: "Top", Signing: true, Configuration: "Debug"},
		Profiles: map[string]Profile{
			"unsigned":    {Configuration: "Release"},
			"development": {Distribution: "development"},
			"preview":     {Distribution: "internal", Env: map[string]string{"API_URL": "https://staging.example.com"}},
			"production":  {Scheme: "MyApp", Provider: "codemagic", Distribution: "store"},
			"debug-store": {Configuration: "Debug", Distribution: "store"},
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
		// A profile without a distribution is unsigned, whatever ios.signing says.
		{"no distribution is unsigned", "unsigned", BuildSettings{Profile: "unsigned", Configuration: "Release", Scheme: "Top", Provider: "github"}},
		{"development derives Debug", "development", BuildSettings{Profile: "development", Configuration: "Debug", Scheme: "Top", Signing: true, Provider: "github", Distribution: "development"}},
		{"internal is ad-hoc and derives Release", "preview", BuildSettings{Profile: "preview", Configuration: "Release", Scheme: "Top", Signing: true, Provider: "github", Distribution: "ad-hoc", Env: map[string]string{"API_URL": "https://staging.example.com"}}},
		{"store derives Release and overrides the rest", "production", BuildSettings{Profile: "production", Configuration: "Release", Scheme: "MyApp", Signing: true, Provider: "codemagic", Distribution: "store"}},
		{"an explicit configuration wins", "debug-store", BuildSettings{Profile: "debug-store", Configuration: "Debug", Scheme: "Top", Signing: true, Provider: "github", Distribution: "store"}},
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

func TestParseDistribution(t *testing.T) {
	for in, want := range map[string]string{
		"": "", "development": "development", "ad-hoc": "ad-hoc", "internal": "ad-hoc", "store": "store", "enterprise": "enterprise",
	} {
		// Flag values arrive with whatever spacing the user typed.
		if got, err := ParseDistribution(" " + in + " "); err != nil || got != want {
			t.Errorf("ParseDistribution(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"adhoc", "app-store", "AD_HOC", "Development", "distribution"} {
		if _, err := ParseDistribution(bad); err == nil {
			t.Errorf("ParseDistribution(%q) accepted", bad)
		}
	}
	// The old name of store points at the new one.
	if _, err := ParseDistribution("app-store"); err == nil || !strings.Contains(err.Error(), `is now "store"`) {
		t.Errorf("app-store: %v", err)
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
	if err == nil || !strings.Contains(err.Error(), "debug-store, development, preview, production, unsigned") {
		t.Fatalf("unknown profile should list the available names: %v", err)
	}
	if _, err := (&Config{}).ResolveProfile("staging"); err == nil || !strings.Contains(err.Error(), "no profiles") {
		t.Fatalf("missing profiles section: %v", err)
	}
	for name, p := range map[string]Profile{
		"bad distribution": {Distribution: "adhoc"},
		"old app-store":    {Distribution: "app-store"},
		"bad env name":     {Env: map[string]string{"API-URL": "x"}},
		"env with equals":  {Env: map[string]string{"A=B": "x"}},
		"reserved env":     {Env: map[string]string{"SCHEME": "Other"}},
		"reserved secret":  {Env: map[string]string{"IOS_CERTIFICATE": "x"}},
		"reserved set":     {Env: map[string]string{"IOS_PROVISIONING_PROFILE_STORE": "x"}},
		"reserved SIGNING": {Env: map[string]string{"SIGNING_SET": "AD_HOC"}},
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
	  "profiles":{"preview":{"configuration":"Release","env":{"API_URL":"https://staging.example.com"},"distribution":"ad-hoc"}}}`
	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	p := cfg.Profiles["preview"]
	if p.Distribution != "ad-hoc" || p.Configuration != "Release" || cfg.DefaultProfile != "preview" {
		t.Fatalf("parsed %+v", cfg)
	}
	out, err := json.Marshal(&Config{Project: "App"})
	if err != nil || strings.Contains(string(out), "profiles") || strings.Contains(string(out), "defaultProfile") {
		t.Fatalf("empty profiles should be omitted: %s %v", out, err)
	}
	// A profile written by signing setup is just its distribution.
	out, err = json.Marshal(Profile{Distribution: "store"})
	if err != nil || string(out) != `{"distribution":"store"}` {
		t.Fatalf("profile encoding: %s %v", out, err)
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
	if got := noEnv.ProfileInput(); !strings.Contains(got, `"env":{}`) || strings.Contains(got, "hooks") {
		t.Fatalf("env should be an object even when empty, and no hooks key without hooks: %s", got)
	}
}

func TestResolveHooks(t *testing.T) {
	cfg := profileConfig()
	cfg.Hooks = &Hooks{PreBuild: "  ./scripts/prebuild.sh\n", PostBuild: "echo top"}
	cfg.Profiles["notify"] = Profile{Distribution: "store", Hooks: &Hooks{PostBuild: "echo one\necho two"}}
	cfg.Profiles["blank"] = Profile{Hooks: &Hooks{PreBuild: " \n\t", PostBuild: ""}}
	cfg.Profiles["clear"] = Profile{}
	for _, tt := range []struct {
		name, profile string
		want          Hooks
	}{
		// Top-level hooks apply with no profile, trimmed.
		{"no profile", "", Hooks{PreBuild: "./scripts/prebuild.sh", PostBuild: "echo top"}},
		// A profile replaces the fields it sets and keeps the others.
		{"per-field override", "notify", Hooks{PreBuild: "./scripts/prebuild.sh", PostBuild: "echo one\necho two"}},
		// Whitespace-only is absent, so the top-level hook stays.
		{"blank keeps the top level", "blank", Hooks{PreBuild: "./scripts/prebuild.sh", PostBuild: "echo top"}},
		{"no hooks on the profile", "clear", Hooks{PreBuild: "./scripts/prebuild.sh", PostBuild: "echo top"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := cfg.ResolveProfile(tt.profile)
			if err != nil {
				t.Fatal(err)
			}
			if got.Hooks != tt.want {
				t.Fatalf("hooks = %+v, want %+v", got.Hooks, tt.want)
			}
		})
	}
	// Without any hooks nothing is set, and a blank top-level hook is none.
	cfg.Hooks = &Hooks{PreBuild: "   "}
	if got, err := cfg.ResolveProfile("clear"); err != nil || !got.Hooks.Empty() || got.Hooks.Names() != nil {
		t.Fatalf("blank top-level hooks: %+v %v", got.Hooks, err)
	}
	if names := (Hooks{PreBuild: "a", PostBuild: "b"}).Names(); strings.Join(names, ",") != "preBuild,postBuild" {
		t.Fatalf("Names = %v", names)
	}
	if names := (Hooks{PostBuild: "b"}).Names(); strings.Join(names, ",") != "postBuild" {
		t.Fatalf("Names = %v", names)
	}
}

func TestHooksEncodings(t *testing.T) {
	// Top-level hooks without a profile still travel: an empty name with the
	// hooks, which the workflow reads as "no profile".
	s := BuildSettings{Hooks: Hooks{PreBuild: "echo \"pre\"\nexit 0"}}
	var input struct {
		Name         string
		Env          map[string]string
		Distribution string
		Hooks        map[string]string
	}
	if err := json.Unmarshal([]byte(s.ProfileInput()), &input); err != nil || input.Name != "" || input.Env == nil || input.Hooks["preBuild"] != s.Hooks.PreBuild || len(input.Hooks) != 1 {
		t.Fatalf("profile input without a profile: %q %+v %v", s.ProfileInput(), input, err)
	}
	if strings.Contains(s.ProfileInput(), "\n") || strings.Contains(s.ProfileInput(), "postBuild") {
		t.Fatalf("profile input must be one line and carry only the hooks set: %q", s.ProfileInput())
	}
	if got := s.HooksJSON(); got != `{"preBuild":"echo \"pre\"\nexit 0"}` {
		t.Fatalf("HooksJSON = %q", got)
	}
	// With a profile the hooks ride along with name, env and distribution.
	s = BuildSettings{Profile: "store", Distribution: "store", Hooks: Hooks{PreBuild: "a", PostBuild: "b"}}
	if err := json.Unmarshal([]byte(s.ProfileInput()), &input); err != nil || input.Name != "store" || input.Distribution != "store" || input.Hooks["preBuild"] != "a" || input.Hooks["postBuild"] != "b" {
		t.Fatalf("profile input with hooks: %q %v", s.ProfileInput(), err)
	}
	if got := s.HooksJSON(); got != `{"preBuild":"a","postBuild":"b"}` {
		t.Fatalf("HooksJSON = %q", got)
	}
	if (&BuildSettings{Profile: "store"}).HooksJSON() != "" {
		t.Fatal("no hooks must produce no BUILD_HOOKS")
	}
}

func TestHooksJSONRoundTrip(t *testing.T) {
	raw := `{"project":"App","github":{"owner":"o","repo":"r"},
	  "hooks":{"preBuild":"./scripts/prebuild.sh"},
	  "profiles":{"store":{"distribution":"store","hooks":{"postBuild":"./scripts/notify.sh"}}}}`
	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Hooks == nil || cfg.Hooks.PreBuild != "./scripts/prebuild.sh" || cfg.Profiles["store"].Hooks == nil || cfg.Profiles["store"].Hooks.PostBuild != "./scripts/notify.sh" {
		t.Fatalf("parsed %+v", cfg)
	}
	out, err := json.Marshal(&Config{Project: "App", Profiles: map[string]Profile{"p": {Distribution: "store"}}})
	if err != nil || strings.Contains(string(out), "hooks") {
		t.Fatalf("absent hooks should be omitted: %s %v", out, err)
	}
	out, err = json.Marshal(Hooks{PostBuild: "x"})
	if err != nil || string(out) != `{"postBuild":"x"}` {
		t.Fatalf("hooks encoding: %s %v", out, err)
	}
}
