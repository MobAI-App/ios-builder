package workflow

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

type workflowStep struct {
	Name string         `yaml:"name"`
	If   string         `yaml:"if"`
	Env  map[string]any `yaml:"env"`
	Run  string         `yaml:"run"`
}

// steps parses a workflow template and returns the steps of its single job.
func steps(t *testing.T, template string) []workflowStep {
	t.Helper()
	data, err := GetTemplate(template)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Jobs map[string]struct {
			Steps []workflowStep `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("%s: %v", template, err)
	}
	if len(parsed.Jobs) != 1 {
		t.Fatalf("%s: expected one job, got %d", template, len(parsed.Jobs))
	}
	for _, job := range parsed.Jobs {
		return job.Steps
	}
	return nil
}

func indexOfStep(t *testing.T, all []workflowStep, name string) int {
	t.Helper()
	i := slices.IndexFunc(all, func(s workflowStep) bool { return s.Name == name })
	if i < 0 {
		t.Fatalf("no step named %q", name)
	}
	return i
}

// TestExpoPrebuildStep pins the managed-Expo contract in both GitHub
// workflows: prebuild runs for Expo projects only, after the node dependencies
// it needs and before the Pods steps that read the Podfile it writes, and it is
// non-interactive so a missing bundle identifier fails instead of hanging.
func TestExpoPrebuildStep(t *testing.T) {
	for _, template := range []string{"ios-build.yml", "ios-share.yml"} {
		t.Run(template, func(t *testing.T) {
			all := steps(t, template)
			prebuild := indexOfStep(t, all, "Expo prebuild")
			step := all[prebuild]

			if step.If != "steps.detect.outputs.type == 'expo'" {
				t.Fatalf("prebuild gate = %q", step.If)
			}
			if got := step.Env["CI"]; got != "1" {
				t.Fatalf("CI env = %v, want \"1\"; prebuild would prompt", got)
			}
			for _, want := range []string{"expo prebuild --platform ios --no-install", "bundleIdentifier", "::error::"} {
				if !strings.Contains(step.Run, want) {
					t.Fatalf("prebuild script is missing %q", want)
				}
			}
			if prebuild < indexOfStep(t, all, "Install npm dependencies") {
				t.Fatal("prebuild runs before the node dependencies it needs")
			}
			if template == "ios-build.yml" && prebuild > indexOfStep(t, all, "Restore Pods cache") {
				t.Fatal("prebuild runs after the Pods cache, so the Podfile it writes is cached too late")
			}

			// The steps that walk the iOS directory run before prebuild, so
			// they must tolerate it not existing yet.
			for _, name := range []string{"Generate Xcode project (XcodeGen)", "Check base configuration files"} {
				if !strings.Contains(all[indexOfStep(t, all, name)].Run, `if [ ! -d "$IOS_PATH" ]`) {
					t.Fatalf("%q does not skip a missing iOS directory", name)
				}
			}
		})
	}
}

// expoPrebuildCases runs a prebuild script against stubbed tooling, so its
// decisions are checked without a macOS CI machine: skip an ejected project,
// refuse a project without a bundle identifier, and otherwise prebuild
// non-interactively.
func expoPrebuildCases(t *testing.T, script string) {
	t.Helper()

	// npx answers `expo config` from NPX_CONFIG_JSON and records a prebuild
	// instead of running one.
	const npx = `#!/bin/bash
set -eu
if [ "${2:-}" = "config" ]; then
  printf '%s' "${NPX_CONFIG_JSON:-{\}}"
  exit 0
fi
printf '%s' "${CI:-unset}" > "$CI_LOG"
mkdir -p "$IOS_PATH/App.xcodeproj"
`

	for _, tt := range []struct {
		name       string
		appJSON    string
		configJSON string
		ejected    bool
		wantErr    string
		wantRun    bool
	}{
		{name: "bundle id in app.json", appJSON: `{"expo":{"ios":{"bundleIdentifier":"com.example.app"}}}`, wantRun: true},
		{name: "bundle id from app.config.js", appJSON: `{"expo":{}}`, configJSON: `{"ios":{"bundleIdentifier":"com.example.app"}}`, wantRun: true},
		{name: "no bundle id", appJSON: `{"expo":{"name":"app"}}`, wantErr: "bundleIdentifier"},
		{name: "ejected project", appJSON: `{"expo":{}}`, ejected: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "bin")
			if err := os.MkdirAll(bin, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(bin, "npx"), []byte(npx), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "app.json"), []byte(tt.appJSON), 0644); err != nil {
				t.Fatal(err)
			}
			if tt.ejected {
				if err := os.MkdirAll(filepath.Join(dir, "ios", "Ejected.xcodeproj"), 0755); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(dir, "prebuild.sh")
			if err := os.WriteFile(path, []byte(script), 0644); err != nil {
				t.Fatal(err)
			}
			ciLog := filepath.Join(dir, "ci.log")
			cmd := exec.Command("bash", path)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(),
				"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"IOS_PATH=ios", "CI=1", "CI_LOG="+ciLog, "NPX_CONFIG_JSON="+tt.configJSON)
			out, err := cmd.CombinedOutput()

			if tt.wantErr != "" {
				if err == nil || !strings.Contains(string(out), tt.wantErr) {
					t.Fatalf("expected failure mentioning %q, got: %s %v", tt.wantErr, out, err)
				}
			} else if err != nil {
				t.Fatalf("prebuild: %s %v", out, err)
			}
			ci, ciErr := os.ReadFile(ciLog)
			if tt.wantRun {
				if ciErr != nil {
					t.Fatal("prebuild did not run:", ciErr)
				}
				if string(ci) != "1" {
					t.Fatalf("prebuild ran with CI=%q, want \"1\"", ci)
				}
			} else if ciErr == nil {
				t.Fatal("prebuild ran when it should not have")
			}
		})
	}
}

// TestRunnerExpoPrebuild exercises the Codemagic/Bitrise runner's
// expo_prebuild function.
func TestRunnerExpoPrebuild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("macOS/Linux shell test")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq unavailable")
	}
	data, err := GetTemplate("runner.sh")
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(data), "\nexpo_prebuild() {\n")
	if start < 0 {
		t.Fatal("runner.sh has no expo_prebuild function")
	}
	body := string(data)[start:]
	end := strings.Index(body, "\n}\n")
	if end < 0 {
		t.Fatal("expo_prebuild is not terminated")
	}
	expoPrebuildCases(t, "set -euo pipefail\n"+body[:end+3]+"\nexpo_prebuild\n")
}

// TestWorkflowExpoPrebuild exercises the same decisions in the GitHub
// workflows, whose step scripts are a separate copy of that logic.
func TestWorkflowExpoPrebuild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("macOS/Linux shell test")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq unavailable")
	}
	for _, template := range []string{"ios-build.yml", "ios-share.yml"} {
		t.Run(template, func(t *testing.T) {
			all := steps(t, template)
			expoPrebuildCases(t, all[indexOfStep(t, all, "Expo prebuild")].Run)
		})
	}
}

// TestWorkflowRunScriptsParse syntax-checks the shell in every step whose
// script holds no workflow expression.
func TestWorkflowRunScriptsParse(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("macOS/Linux shell syntax test")
	}
	dir := t.TempDir()
	for _, template := range []string{"ios-build.yml", "ios-share.yml"} {
		for i, step := range steps(t, template) {
			if step.Run == "" || strings.Contains(step.Run, "${{") {
				continue
			}
			script := filepath.Join(dir, strings.TrimSuffix(template, ".yml")+"-"+string(rune('a'+i))+".sh")
			if err := os.WriteFile(script, []byte(step.Run), 0644); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command("bash", "-n", script).CombinedOutput(); err != nil {
				t.Fatalf("%s step %q: %s %v", template, step.Name, out, err)
			}
		}
	}
}
