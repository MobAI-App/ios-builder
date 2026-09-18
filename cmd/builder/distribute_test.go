package main

import (
	"strings"
	"testing"

	"github.com/MobAI-App/ios-builder/internal/config"
)

// TestBuildDistributePreflight: --distribute is checked against the profile
// before anything is pushed, and cannot be combined with --submit or --unsigned.
func TestBuildDistributePreflight(t *testing.T) {
	t.Chdir(t.TempDir())
	cfg := &config.Config{Project: "App", Platform: "ios", GitHub: config.GitHubConfig{Owner: "o", Repo: "r"}, Profiles: map[string]config.Profile{
		"store": {Distribution: "store"},
		"plain": {},
	}}
	if err := config.NewManager().Save(cfg); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--submit", "--distribute"}, "only one of --submit"},
		{[]string{"--distribute", "--unsigned"}, "drop --unsigned"},
		{[]string{"--distribute", "--profile", "store"}, `profile "store" has distribution store`},
		{[]string{"--distribute", "--profile", "plain"}, `profile "plain" has no distribution`},
		{[]string{"--distribute"}, "pass --profile"},
	} {
		_, _, err := run(t, append([]string{"ios", "build"}, tc.args...)...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: err = %v, want %q", tc.args, err, tc.want)
		}
	}
}

func TestDistributeNeedsAnIPA(t *testing.T) {
	t.Chdir(t.TempDir())
	cfg := &config.Config{Project: "App", Platform: "ios", GitHub: config.GitHubConfig{Owner: "o", Repo: "r"}}
	if err := config.NewManager().Save(cfg); err != nil {
		t.Fatal(err)
	}
	_, _, err := run(t, "ios", "distribute")
	if err == nil || !strings.Contains(err.Error(), "no .ipa found in dist") {
		t.Errorf("err = %v", err)
	}
	_, _, err = run(t, "ios", "distribute", "--ipa", "missing.ipa")
	if err == nil || !strings.Contains(err.Error(), "open IPA") {
		t.Errorf("err = %v", err)
	}
}
