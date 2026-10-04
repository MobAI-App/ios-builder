package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// initRepo makes the current directory a git repository with a GitHub origin.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", "https://github.com/octo/app.git"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func TestInitWithoutTerminalNamesTheFlags(t *testing.T) {
	initRepo(t)

	_, _, err := runNoInput(t, "init")
	wantUsage(t, err, "--project", "--yes")

	_, _, err = runNoInput(t, "init", "--project", "App")
	wantUsage(t, err, "--ios-path")

	_, _, err = runNoInput(t, "init", "--project", "App", "--ios-path", "")
	wantUsage(t, err, "--commit")

	_, _, err = runNoInput(t, "init", "--project", "App", "--ios-path", "", "--commit=false")
	wantUsage(t, err, "--build")

	if _, err := os.Stat("builder.json"); err == nil {
		t.Fatal("init wrote builder.json before every question was answered")
	}

	if _, _, err = runNoInput(t, "init", "--project", "App", "--ios-path", "", "--commit=false", "--build=false"); err != nil {
		t.Fatalf("init with every answer as a flag: %v", err)
	}
	if _, err := os.Stat("builder.json"); err != nil {
		t.Fatal("builder.json was not written")
	}
}

func TestInitYesJSON(t *testing.T) {
	dir := initRepo(t)
	if err := os.MkdirAll(filepath.Join("ios", "App.xcodeproj"), 0o755); err != nil {
		t.Fatal(err)
	}

	// The detected path is a question too.
	_, _, err := runNoInput(t, "init", "--project", "App")
	wantUsage(t, err, "--ios-path", "--yes")

	stdout, _, err := runNoInput(t, "init", "--yes", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var res initResult
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("stdout is not one JSON object: %q", stdout)
	}
	want := []string{filepath.Join(".github", "workflows", "ios-build.yml"), filepath.Join(".github", "workflows", "ios-share.yml"), "builder.json"}
	if res.Project != filepath.Base(dir) || res.Repository != "octo/app" || res.IOSPath != "ios" || res.Framework != "React Native/Expo" ||
		!slices.Equal(res.Files, want) || res.Committed || res.Pushed || res.Build != nil {
		t.Errorf("init --yes --json = %+v", res)
	}
}
