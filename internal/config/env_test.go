package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func envConfig() *Config {
	return &Config{
		Project: "App", GitHub: GitHubConfig{Owner: "o", Repo: "r"},
		Env:     map[string]string{"API_URL": "https://api.example.com", "LOG": "info"},
		Secrets: []string{"SENTRY_TOKEN"},
		Profiles: map[string]Profile{
			"staging":    {Distribution: "internal", Env: map[string]string{"API_URL": "https://staging.example.com"}, Secrets: []string{"MAPS_KEY", "SENTRY_TOKEN"}},
			"production": {Distribution: "store"},
		},
	}
}

func TestResolveProfileMergesTopLevelEnvAndSecrets(t *testing.T) {
	cfg := envConfig()
	s, err := cfg.ResolveProfile("")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.Env, cfg.Env) || !reflect.DeepEqual(s.Secrets, []string{"SENTRY_TOKEN"}) {
		t.Fatalf("no profile: env %v secrets %v", s.Env, s.Secrets)
	}
	// The profile's env wins per key; the rest of the top level stays.
	s, err = cfg.ResolveProfile("staging")
	if err != nil {
		t.Fatal(err)
	}
	if s.Env["API_URL"] != "https://staging.example.com" || s.Env["LOG"] != "info" || len(s.Env) != 2 {
		t.Fatalf("staging env: %v", s.Env)
	}
	if !reflect.DeepEqual(s.Secrets, []string{"MAPS_KEY", "SENTRY_TOKEN"}) {
		t.Fatalf("staging secrets must be the sorted union: %v", s.Secrets)
	}
	// Resolving must not write into builder.json's maps.
	if cfg.Env["API_URL"] != "https://api.example.com" {
		t.Fatal("profile env leaked into the top level")
	}
	s, _ = cfg.ResolveProfile("production")
	if s.Env["API_URL"] != "https://api.example.com" || !reflect.DeepEqual(s.Secrets, []string{"SENTRY_TOKEN"}) {
		t.Fatalf("production inherits the top level: %v %v", s.Env, s.Secrets)
	}
	// Nothing configured: nothing to send.
	s, _ = (&Config{}).ResolveProfile("")
	if s.Env != nil || s.Secrets != nil || s.ProfileInput() != "" || s.EnvJSON() != "" {
		t.Fatalf("empty config: %+v", s)
	}
}

func TestResolveProfileRejectsBadSecrets(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"lower case":            func(c *Config) { c.Secrets = []string{"sentry_token"} },
		"double underscore":     func(c *Config) { c.Secrets = []string{"A__B"} },
		"signing secret":        func(c *Config) { c.Secrets = []string{"IOS_CERTIFICATE_STORE"} },
		"MobAI key":             func(c *Config) { c.Secrets = []string{"MOBAI_API_KEY"} },
		"GitHub namespace":      func(c *Config) { c.Secrets = []string{"GITHUB_TOKEN"} },
		"builder namespace":     func(c *Config) { c.Secrets = []string{"BUILDER_SECRETS"} },
		"leading digit":         func(c *Config) { c.Secrets = []string{"1KEY"} },
		"top-level env":         func(c *Config) { c.Env = map[string]string{"SCHEME": "x"} },
		"secret shadows env":    func(c *Config) { c.Env["SENTRY_TOKEN"] = "plain" },
		"profile secret is env": func(c *Config) { c.Env["MAPS_KEY"] = "plain" },
	} {
		cfg := envConfig()
		mutate(cfg)
		_, err1 := cfg.ResolveProfile("")
		_, err2 := cfg.ResolveProfile("staging")
		if err1 == nil && err2 == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestSecretStorageName(t *testing.T) {
	for _, tt := range []struct{ name, profile, want string }{
		{"SENTRY_TOKEN", "", "SENTRY_TOKEN"},
		{"SENTRY_TOKEN", "production", "SENTRY_TOKEN__PRODUCTION"},
		{"SENTRY_TOKEN", "debug-store", "SENTRY_TOKEN__DEBUG_STORE"},
		{"K", "Pre.view 2", "K__PRE_VIEW_2"},
	} {
		if got := SecretStorageName(tt.name, tt.profile); got != tt.want {
			t.Errorf("SecretStorageName(%q, %q) = %q, want %q", tt.name, tt.profile, got, tt.want)
		}
	}
}

func TestEnvAndSecretEdits(t *testing.T) {
	cfg := envConfig()
	if err := cfg.SetEnv("", "FEATURE", "on"); err != nil || cfg.Env["FEATURE"] != "on" {
		t.Fatalf("top-level set: %v %v", err, cfg.Env)
	}
	if err := cfg.SetEnv("production", "API_URL", "https://prod.example.com"); err != nil || cfg.Profiles["production"].Env["API_URL"] != "https://prod.example.com" {
		t.Fatalf("profile set: %v %v", err, cfg.Profiles["production"])
	}
	// Invalid edits leave the config as it was.
	for name, err := range map[string]error{
		"reserved":         cfg.SetEnv("", "PATH", "/tmp"),
		"bad name":         cfg.SetEnv("", "A-B", "x"),
		"unknown profile":  cfg.SetEnv("nightly", "A", "x"),
		"env over secret":  cfg.SetEnv("", "SENTRY_TOKEN", "x"),
		"secret over env":  cfg.AddSecret("", "LOG"),
		"reserved secret":  cfg.AddSecret("", "IOS_PROVISIONING_PROFILE_STORE"),
		"profile conflict": cfg.SetEnv("staging", "MAPS_KEY", "x"),
	} {
		if err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, ok := cfg.Env["SENTRY_TOKEN"]; ok || cfg.Profiles["staging"].Env["MAPS_KEY"] != "" || len(cfg.Secrets) != 1 {
		t.Fatalf("rejected edit stuck: %+v", cfg)
	}

	if err := cfg.AddSecret("production", "STRIPE_KEY"); err != nil || !reflect.DeepEqual(cfg.Profiles["production"].Secrets, []string{"STRIPE_KEY"}) {
		t.Fatalf("add secret: %v %v", err, cfg.Profiles["production"])
	}
	if err := cfg.AddSecret("", "A_FIRST"); err != nil || !reflect.DeepEqual(cfg.Secrets, []string{"A_FIRST", "SENTRY_TOKEN"}) {
		t.Fatalf("list must stay sorted: %v %v", err, cfg.Secrets)
	}
	if err := cfg.AddSecret("", "A_FIRST"); err != nil || len(cfg.Secrets) != 2 {
		t.Fatalf("re-adding duplicated: %v %v", err, cfg.Secrets)
	}
	if ok, err := cfg.RemoveSecret("production", "STRIPE_KEY"); !ok || err != nil || cfg.Profiles["production"].Secrets != nil {
		t.Fatalf("remove: %v %v %v", ok, err, cfg.Profiles["production"])
	}
	if ok, _ := cfg.RemoveSecret("", "MISSING"); ok {
		t.Fatal("removed a name that was not listed")
	}
	if ok, err := cfg.UnsetEnv("production", "API_URL"); !ok || err != nil || cfg.Profiles["production"].Env != nil {
		t.Fatalf("unset: %v %v %v", ok, err, cfg.Profiles["production"])
	}
	if ok, _ := cfg.UnsetEnv("", "NOPE"); ok {
		t.Fatal("unset a name that was not set")
	}
}

func TestEnvAndSecretsRoundTrip(t *testing.T) {
	t.Chdir(t.TempDir())
	mgr := NewManager()
	cfg := envConfig()
	if err := mgr.Save(cfg); err != nil {
		t.Fatal(err)
	}
	got, err := mgr.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Env, cfg.Env) || !reflect.DeepEqual(got.Secrets, cfg.Secrets) || !reflect.DeepEqual(got.Profiles["staging"], cfg.Profiles["staging"]) {
		t.Fatalf("round trip lost data:\n got %+v\nwant %+v", got, cfg)
	}
	data, _ := os.ReadFile(filepath.Join(".", ConfigFileName))
	if !strings.Contains(string(data), `"secrets": [`) || !strings.Contains(string(data), `"SENTRY_TOKEN"`) {
		t.Fatalf("names not written:\n%s", data)
	}
	// Empty lists and maps are omitted, so configs without them are unchanged.
	out, _ := json.Marshal(&Config{Project: "App", Profiles: map[string]Profile{"p": {}}})
	if strings.Contains(string(out), "secrets") || strings.Contains(string(out), `"env"`) {
		t.Fatalf("empty env/secrets written: %s", out)
	}
}

func TestProfileInputCarriesSecrets(t *testing.T) {
	cfg := envConfig()
	s, _ := cfg.ResolveProfile("staging")
	var in struct {
		Name    string            `json:"name"`
		Env     map[string]string `json:"env"`
		Secrets []string          `json:"secrets"`
	}
	if err := json.Unmarshal([]byte(s.ProfileInput()), &in); err != nil {
		t.Fatal(err)
	}
	if in.Name != "staging" || in.Env["LOG"] != "info" || !reflect.DeepEqual(in.Secrets, []string{"MAPS_KEY", "SENTRY_TOKEN"}) {
		t.Fatalf("profile input: %+v", in)
	}
	// No profile but a top-level env or secret still needs the input.
	s, _ = cfg.ResolveProfile("")
	if err := json.Unmarshal([]byte(s.ProfileInput()), &in); err != nil || in.Name != "" || len(in.Secrets) != 1 {
		t.Fatalf("top-level only: %q %v", s.ProfileInput(), err)
	}
	// A profile without secrets sends no secrets key (older workflows ignore it anyway).
	s = BuildSettings{Profile: "development"}
	if strings.Contains(s.ProfileInput(), "secrets") {
		t.Fatalf("empty secrets sent: %s", s.ProfileInput())
	}
}
