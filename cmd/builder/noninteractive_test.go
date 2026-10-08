package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/MobAI-App/ios-builder/internal/config"
	"github.com/MobAI-App/ios-builder/internal/exitcode"
)

// runNoInput runs builder with stdin reported as not a terminal and fails the
// test if the command has not returned within a few seconds: a command that
// would prompt must fail fast or take its --yes default instead of blocking.
func runNoInput(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	return runWithTerminal(t, false, args...)
}

func runWithTerminal(t *testing.T, terminal bool, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	prev := stdinIsTerminal
	stdinIsTerminal = func() bool { return terminal }
	t.Cleanup(func() { stdinIsTerminal = prev; noInput = false })
	noInput = false
	type result struct {
		stdout, stderr string
		err            error
	}
	done := make(chan result, 1)
	go func() {
		o, e, err := run(t, args...)
		done <- result{o, e, err}
	}()
	select {
	case r := <-done:
		return r.stdout, r.stderr, r.err
	case <-time.After(10 * time.Second):
		t.Fatalf("builder %v blocked instead of failing without a terminal", args)
		return "", "", nil
	}
}

// wantUsage checks err is a usage error (exit 2) naming every flag in flags.
func wantUsage(t *testing.T, err error, flags ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := exitCode(err); got != exitcode.Usage {
		t.Errorf("exit code = %d, want %d (%v)", got, exitcode.Usage, err)
	}
	for _, f := range flags {
		if !strings.Contains(err.Error(), f) {
			t.Errorf("error %q does not name %s", err, f)
		}
	}
}

func TestInteractiveSwitches(t *testing.T) {
	prev := stdinIsTerminal
	stdinIsTerminal = func() bool { return true }
	t.Cleanup(func() { stdinIsTerminal = prev; noInput = false })
	t.Setenv("CI", "")
	t.Setenv("BUILDER_NO_INPUT", "")

	if !interactive(nil) {
		t.Fatal("a terminal with nothing set should be interactive")
	}
	noInput = true
	if interactive(nil) {
		t.Error("--no-input should disable prompts")
	}
	noInput = false
	for _, env := range []string{"CI", "BUILDER_NO_INPUT"} {
		t.Setenv(env, "true")
		if interactive(nil) {
			t.Errorf("%s=true should disable prompts", env)
		}
		t.Setenv(env, "")
	}
	stdinIsTerminal = func() bool { return false }
	if interactive(nil) {
		t.Error("stdin that is not a terminal should disable prompts")
	}
}

func TestSigningCSRWithoutTerminal(t *testing.T) {
	t.Chdir(t.TempDir())

	_, _, err := runNoInput(t, "signing", "csr")
	wantUsage(t, err, "--name")

	_, _, err = runNoInput(t, "signing", "csr", "--name", "A", "--email", "a@example.com")
	if err != nil {
		t.Fatalf("csr with flags: %v", err)
	}
	key, _ := os.ReadFile("ios-signing.key")

	// Replacing the key is destructive: --yes or a terminal.
	_, _, err = runNoInput(t, "signing", "csr", "--name", "A", "--email", "a@example.com")
	wantUsage(t, err, "--yes")
	if again, _ := os.ReadFile("ios-signing.key"); !bytes.Equal(again, key) {
		t.Fatal("the key was replaced without --yes")
	}

	stdout, _, err := runNoInput(t, "signing", "csr", "--name", "A", "--email", "a@example.com", "--yes", "--json")
	if err != nil {
		t.Fatalf("csr --yes --json: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(stdout), &got); err != nil || got["key"] != "ios-signing.key" || got["csr"] != "ios-signing.csr" {
		t.Fatalf("csr --json = %q (%v)", stdout, err)
	}
}

// TestNoInputOverridesATerminal: with a terminal, --no-input still must not
// prompt.
func TestNoInputOverridesATerminal(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("CI", "")
	t.Setenv("BUILDER_NO_INPUT", "")
	_, _, err := runWithTerminal(t, true, "--no-input", "signing", "csr")
	wantUsage(t, err, "--name")

	t.Setenv("BUILDER_NO_INPUT", "1")
	_, _, err = runWithTerminal(t, true, "signing", "p12", "--certificate", "x.cer")
	wantUsage(t, err, "--key", "--yes")
}

func TestSigningSetupManualWithoutTerminal(t *testing.T) {
	t.Chdir(t.TempDir())
	cfg := &config.Config{Project: "App", Platform: "ios", GitHub: config.GitHubConfig{Owner: "o", Repo: "r"}}
	if err := config.NewManager().Save(cfg); err != nil {
		t.Fatal(err)
	}
	prev := signingSecretStore
	signingSecretStore = func() (secretStore, error) { return nil, errors.New("no login in tests") }
	t.Cleanup(func() { signingSecretStore = prev })

	_, _, err := runNoInput(t, "signing", "setup", "--profile", "app.mobileprovision")
	wantUsage(t, err, "--certificate")
}

func TestDevSkipInstallNeedsBundleID(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, sub := range []string{"flutter", "rn", "kmp"} {
		_, _, err := runNoInput(t, "dev", sub, "--skip-install", "--json")
		wantUsage(t, err, "--bundle-id")
	}
	if os.Stdout == os.Stderr {
		t.Fatal("dev --json left os.Stdout pointed at stderr")
	}
}

func TestSigningP12WithoutTerminal(t *testing.T) {
	t.Chdir(t.TempDir())
	_, _, err := runNoInput(t, "signing", "p12")
	wantUsage(t, err, "--certificate")
	_, _, err = runNoInput(t, "signing", "p12", "--certificate", "x.cer", "--yes")
	wantUsage(t, err, "--password")
}
