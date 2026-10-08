package main

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/MobAI-App/ios-builder/internal/auth"
	"github.com/MobAI-App/ios-builder/internal/exitcode"
)

// stubGitHubToken answers token checks with scopes and records saves.
func stubGitHubToken(t *testing.T, scopes []string) *[]string {
	t.Helper()
	var saved []string
	prevCheck, prevSave := checkGitHubToken, saveGitHubToken
	checkGitHubToken = func(_ context.Context, token string) (*auth.GitHubIdentity, error) {
		return &auth.GitHubIdentity{Login: "octo", Scopes: scopes, ScopesKnown: scopes != nil}, nil
	}
	saveGitHubToken = func(token string) error { saved = append(saved, token); return nil }
	t.Cleanup(func() { checkGitHubToken, saveGitHubToken = prevCheck, prevSave; rootCmd.SetIn(nil) })
	return &saved
}

func TestAuthGitHubWithoutTerminal(t *testing.T) {
	stubGitHubToken(t, nil)
	_, _, err := runNoInput(t, "auth", "github")
	wantUsage(t, err, "--token-stdin", "--device-flow")
}

func TestAuthGitHubTokenStdin(t *testing.T) {
	saved := stubGitHubToken(t, []string{"repo", "workflow", "gist", "read:org"})
	rootCmd.SetIn(strings.NewReader("ghp_secret\n"))
	stdout, _, err := runNoInput(t, "auth", "github", "--token-stdin", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var res githubAuthResult
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("not JSON: %q", stdout)
	}
	if res.Event != "authenticated" || res.Method != "token" || res.Login != "octo" || !res.ScopesChecked || len(res.MissingScopes) != 0 {
		t.Errorf("result = %+v", res)
	}
	if !slices.Equal(*saved, []string{"ghp_secret"}) {
		t.Errorf("saved = %v", *saved)
	}
}

func TestAuthGitHubTokenMissingScopes(t *testing.T) {
	saved := stubGitHubToken(t, []string{"repo"})
	rootCmd.SetIn(strings.NewReader("ghp_secret"))
	stdout, _, err := runNoInput(t, "auth", "github", "--token-stdin", "--json")
	if exitcode.Code(err) != exitcode.Auth || !strings.Contains(err.Error(), "workflow, gist") {
		t.Fatalf("err = %v, want an auth error naming workflow, gist", err)
	}
	var res githubAuthResult
	if err := json.Unmarshal([]byte(stdout), &res); err != nil || !slices.Equal(res.MissingScopes, []string{"workflow", "gist"}) {
		t.Errorf("JSON = %q (%v)", stdout, err)
	}
	if len(*saved) != 0 {
		t.Error("a token without the scopes was saved")
	}
}

func TestAuthGitHubEmptyStdin(t *testing.T) {
	stubGitHubToken(t, nil)
	rootCmd.SetIn(strings.NewReader("  \n"))
	_, _, err := runNoInput(t, "auth", "github", "--token-stdin")
	wantUsage(t, err, "--token-stdin")
}

func TestAuthOthersWithoutTerminal(t *testing.T) {
	t.Setenv("ASC_ISSUER_ID", "")
	_, _, err := runNoInput(t, "auth", "codemagic")
	wantUsage(t, err, "--token-stdin", "CODEMAGIC_API_TOKEN")
	_, _, err = runNoInput(t, "auth", "apple")
	wantUsage(t, err, "--issuer-id")
}
