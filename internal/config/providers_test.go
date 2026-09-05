package config

import (
	"encoding/json"
	"testing"
)

func TestProviderDefaultAndOverride(t *testing.T) {
	var old Config
	if err := json.Unmarshal([]byte(`{"project":"App","github":{"owner":"o","repo":"r"}}`), &old); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		configured, override, want string
		bad                        bool
	}{
		{"", "", "github", false}, {"github", "", "github", false}, {"codemagic", "", "codemagic", false},
		{"codemagic", "github", "github", false}, {"bitrise", "codemagic", "codemagic", false}, {"", "bogus", "", true},
	} {
		old.Provider = tt.configured
		name, err := old.ProviderName(tt.override)
		if name != tt.want || (err != nil) != tt.bad {
			t.Fatalf("%+v => %q %v", tt, name, err)
		}
	}
	old.Codemagic = CIConfig{AppID: "app", Branch: "main"}
	if _, err := old.ProviderConfig("codemagic"); err != nil {
		t.Fatal(err)
	}
	if _, err := old.ProviderConfig("bitrise"); err == nil {
		t.Fatal("accepted incomplete selected provider")
	}
	if _, err := old.ProviderConfig("github"); err != nil {
		t.Fatal("legacy provider affected by incomplete accounts")
	}
}
