package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestRunnerJSON(t *testing.T) {
	for _, tt := range []struct {
		name, in string
		want     Runner
		out      string
	}{
		{"string", `{"runner":"macos-15"}`, Runner{"macos-15"}, `"runner":"macos-15"`},
		{"array", `{"runner":["self-hosted","macOS","ARM64"]}`, Runner{"self-hosted", "macOS", "ARM64"}, `"runner":["self-hosted","macOS","ARM64"]`},
		{"one-element array writes back as a string", `{"runner":["self-hosted"]}`, Runner{"self-hosted"}, `"runner":"self-hosted"`},
		{"empty string is unset", `{"runner":""}`, nil, ""},
		{"absent", `{}`, nil, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var cfg Config
			if err := json.Unmarshal([]byte(tt.in), &cfg); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(cfg.Runner, tt.want) {
				t.Fatalf("runner = %#v, want %#v", cfg.Runner, tt.want)
			}
			data, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if tt.out == "" {
				if strings.Contains(string(data), `"runner"`) {
					t.Fatalf("empty runner written: %s", data)
				}
			} else if !strings.Contains(string(data), tt.out) {
				t.Fatalf("marshalled %s, want %s", data, tt.out)
			}
		})
	}
	var cfg Config
	if err := json.Unmarshal([]byte(`{"runner":3}`), &cfg); err == nil {
		t.Fatal("a number was accepted as a runner")
	}
	if err := json.Unmarshal([]byte(`{"profiles":{"ci":{"runner":["self-hosted","ARM64"]}}}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.Profiles["ci"].Runner, Runner{"self-hosted", "ARM64"}) {
		t.Fatalf("profile runner = %#v", cfg.Profiles["ci"].Runner)
	}
}

func TestParseRunner(t *testing.T) {
	for in, want := range map[string]Runner{
		"":                         nil,
		"macos-latest":             {"macos-latest"},
		"self-hosted, macOS,ARM64": {"self-hosted", "macOS", "ARM64"},
	} {
		got, err := ParseRunner(in)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("ParseRunner(%q) = %#v, %v; want %#v", in, got, err, want)
		}
	}
	for _, bad := range []string{"self-hosted,,macOS", "mac os", `macos"`, "-x", "a,${{ secrets.X }}"} {
		if _, err := ParseRunner(bad); err == nil {
			t.Errorf("ParseRunner(%q) accepted", bad)
		}
	}
}

func TestRunnerWarning(t *testing.T) {
	for _, r := range []Runner{nil, {"macos-latest"}, {"macos-15-xlarge"}, {"self-hosted"}, {"self-hosted", "Linux"}} {
		if w := r.Warning(); w != "" {
			t.Errorf("%v: unexpected warning %q", r, w)
		}
	}
	for _, r := range []Runner{{"ubuntu-latest"}, {"macOS", "ARM64"}} {
		if r.Warning() == "" {
			t.Errorf("%v: no warning", r)
		}
	}
}

func TestResolveProfileRunner(t *testing.T) {
	cfg := &Config{
		Runner: Runner{"macos-15"},
		Profiles: map[string]Profile{
			"plain":  {},
			"office": {Runner: Runner{"self-hosted", "macOS"}},
			"bad":    {Runner: Runner{"a b"}},
		},
	}
	for name, want := range map[string]Runner{"": {"macos-15"}, "plain": {"macos-15"}, "office": {"self-hosted", "macOS"}} {
		s, err := cfg.ResolveProfile(name)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(s.Runner, want) {
			t.Errorf("profile %q runner = %v, want %v", name, s.Runner, want)
		}
	}
	if _, err := cfg.ResolveProfile("bad"); err == nil || !strings.Contains(err.Error(), `profile "bad"`) {
		t.Fatalf("bad profile runner: %v", err)
	}
	cfg.Runner = Runner{"x y"}
	if _, err := cfg.ResolveProfile(""); err == nil {
		t.Fatal("bad top-level runner accepted")
	}
}

func TestProfileInputCarriesRunner(t *testing.T) {
	type input struct {
		Name   string          `json:"name"`
		Runner json.RawMessage `json:"runner"`
	}
	decode := func(s string) input {
		t.Helper()
		var in input
		if err := json.Unmarshal([]byte(s), &in); err != nil {
			t.Fatalf("%q: %v", s, err)
		}
		return in
	}
	if got := (&BuildSettings{}).ProfileInput(); got != "" {
		t.Fatalf("no profile and no runner sent %q", got)
	}
	if in := decode((&BuildSettings{Profile: "dev"}).ProfileInput()); in.Runner != nil {
		t.Fatalf("runner sent without one configured: %s", in.Runner)
	}
	in := decode((&BuildSettings{Runner: Runner{"self-hosted", "macOS"}}).ProfileInput())
	if in.Name != "" || string(in.Runner) != `["self-hosted","macOS"]` {
		t.Fatalf("runner without profile = %+v", in)
	}
	in = decode((&BuildSettings{Profile: "ci", Runner: Runner{"macos-15"}}).ProfileInput())
	if in.Name != "ci" || string(in.Runner) != `"macos-15"` {
		t.Fatalf("profile runner = %+v", in)
	}
}

func TestCheckMachine(t *testing.T) {
	for _, tt := range []struct {
		provider, field, value string
		warn, err              bool
	}{
		{"codemagic", "instance_type", "", false, false},
		{"codemagic", "instance_type", "mac_mini_m2", false, false},
		{"codemagic", "instance_type", "mac_mini_m9", true, false},
		{"bitrise", "machine_type_id", "g2.mac.large", false, false},
		{"bitrise", "machine_type_id", "g9.mac.huge", true, false},
		{"bitrise", "stack", "osx-xcode-16.2.x", false, false},
		{"bitrise", "machine_type_id", "g2 mac", false, true},
		{"codemagic", "instance_type", "mac\nscripts:", false, true},
	} {
		w, err := CheckMachine(tt.provider, tt.field, tt.value)
		if (w != "") != tt.warn || (err != nil) != tt.err {
			t.Errorf("CheckMachine(%s, %s, %q) = %q, %v", tt.provider, tt.field, tt.value, w, err)
		}
	}
}

func TestRunnerName(t *testing.T) {
	cfg := &Config{}
	if got := cfg.RunnerName("github", &BuildSettings{}); got != "macos-latest" {
		t.Errorf("github default = %q", got)
	}
	if got := cfg.RunnerName("github", &BuildSettings{Runner: Runner{"self-hosted", "ARM64"}}); got != "self-hosted,ARM64" {
		t.Errorf("github = %q", got)
	}
	if got := cfg.RunnerName("codemagic", nil); got != "mac_mini_m2" {
		t.Errorf("codemagic default = %q", got)
	}
	cfg.Bitrise = CIConfig{MachineTypeID: "g2.mac.large", Stack: "osx-xcode-16.2.x"}
	if got := cfg.RunnerName("bitrise", nil); got != "g2.mac.large (osx-xcode-16.2.x)" {
		t.Errorf("bitrise = %q", got)
	}
}
