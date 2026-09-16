package workflow

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"text/template"

	"go.yaml.in/yaml/v3"
)

func TestProviderYAMLAndPreservation(t *testing.T) {
	for _, name := range []string{"codemagic", "bitrise"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			files, err := ProviderFiles(name)
			if err != nil {
				t.Fatal(err)
			}
			for path, data := range files {
				if strings.HasSuffix(path, ".sh") {
					continue
				}
				var parsed map[string]any
				if err := yaml.Unmarshal(data, &parsed); err != nil {
					t.Fatalf("%s: %v", path, err)
				}
				workflows, ok := parsed["workflows"].(map[string]any)
				if !ok || workflows["ios-build"] == nil || workflows["ios-share"] == nil {
					t.Fatal("missing workflows")
				}
				if err := os.WriteFile(filepath.Join(dir, path), []byte("# my custom pipeline\n"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := WriteProviderFiles(dir, name); err == nil {
				t.Fatal("overwrote unrelated workflow")
			}
			if _, err := os.Stat(filepath.Join(dir, ".builder")); !os.IsNotExist(err) {
				t.Fatal("partial write before collision check")
			}
			dir = t.TempDir()
			if _, err := WriteProviderFiles(dir, name); err != nil {
				t.Fatal(err)
			}
			if _, err := WriteProviderFiles(dir, name); err != nil {
				t.Fatal("idempotent setup:", err)
			}
		})
	}
}

func TestRunnerSnapshotProtocol(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("macOS/Linux shell protocol test")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	data, err := GetTemplate("runner.sh")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "runner.sh")
	if err := os.WriteFile(script, data, 0644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("bash", "-n", script).CombinedOutput(); err != nil {
		t.Fatalf("shell syntax: %s %v", out, err)
	}
	remote := filepath.Join(dir, "remote.git")
	source := filepath.Join(dir, "source")
	clone := filepath.Join(dir, "clone with spaces")
	git := func(wd string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = wd
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
		return strings.TrimSpace(string(out))
	}
	git(dir, "init", "--bare", remote)
	git(dir, "init", source)
	git(source, "config", "user.name", "Builder Test")
	git(source, "config", "user.email", "builder@example.com")
	if err := os.WriteFile(filepath.Join(source, "file.txt"), []byte("base"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(source, "App.xcodeproj"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "App.xcodeproj", "project.pbxproj"), []byte("test project"), 0644); err != nil {
		t.Fatal(err)
	}
	git(source, "add", ".")
	git(source, "commit", "-m", "base")
	git(source, "branch", "-M", "main")
	git(source, "remote", "add", "origin", remote)
	git(source, "push", "origin", "main")
	git(dir, "clone", "--branch", "main", remote, clone)
	if err := os.WriteFile(filepath.Join(source, "file.txt"), []byte("snapshot"), 0644); err != nil {
		t.Fatal(err)
	}
	git(source, "add", ".")
	git(source, "commit", "-m", "snapshot")
	sha := git(source, "rev-parse", "HEAD")
	ref := "refs/ios-builder/jobs/abcdef12"
	git(source, "push", "origin", "HEAD:"+ref)
	run := func(expected string) (string, error) {
		cmd := exec.Command("bash", script, "checkout")
		cmd.Dir = clone
		cmd.Env = append(os.Environ(), "SNAPSHOT_REF="+ref, "SNAPSHOT_SHA="+expected, "BUILDER_CI_DIR="+filepath.Join(dir, "state"))
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := run(strings.Repeat("0", 40)); err == nil || !strings.Contains(out, "SHA mismatch") {
		t.Fatalf("mismatch accepted: %s %v", out, err)
	}
	if out, err := run(sha); err != nil {
		t.Fatalf("checkout: %s %v", out, err)
	}
	got, err := os.ReadFile(filepath.Join(clone, "file.txt"))
	if err != nil || string(got) != "snapshot" {
		t.Fatalf("wrong source: %q %v", got, err)
	}
	t.Run("native runner with quoted scheme", func(t *testing.T) {
		if _, err := exec.LookPath("jq"); err != nil {
			t.Skip("jq unavailable for native runner stub")
		}
		if _, err := exec.LookPath("python3"); err != nil {
			t.Skip("python3 unavailable for native runner stub")
		}
		bin := filepath.Join(dir, "bin")
		if err := os.MkdirAll(bin, 0755); err != nil {
			t.Fatal(err)
		}
		stub := `#!/bin/bash
set -eu
dd=""; prev=""; settings=false
for arg in "$@"; do
  if [ "$prev" = "-derivedDataPath" ]; then dd="$arg"; fi
  if [ "$prev" = "-scheme" ]; then printf '%s' "$arg" > "$SCHEME_LOG"; fi
  if [ "$arg" = "-showBuildSettings" ]; then settings=true; fi
  prev="$arg"
done
app="$dd/Build/Products/Debug-iphoneos/App.app"
if [ "$settings" = true ]; then
  python3 - "$dd/Build/Products/Debug-iphoneos" <<'PY'
import json, sys
print(json.dumps([{'buildSettings': {'PRODUCT_TYPE': 'com.apple.product-type.application', 'TARGET_BUILD_DIR': sys.argv[1], 'FULL_PRODUCT_NAME': 'App.app'}}]))
PY
else
  mkdir -p "$app"
  printf 'plist' > "$app/Info.plist"
fi
`
		if err := os.WriteFile(filepath.Join(bin, "xcodebuild"), []byte(stub), 0755); err != nil {
			t.Fatal(err)
		}
		scheme := `App's $(touch should-not-exist)`
		cmd := exec.Command("/bin/bash", script, "build")
		cmd.Dir = clone
		cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "SNAPSHOT_REF="+ref, "SNAPSHOT_SHA="+sha, "BUILD_ID=abcdef12", "IOS_PATH=.", "USE_SIGNING=false", "CONFIGURATION=Debug", "SCHEME="+scheme, "SCHEME_LOG="+filepath.Join(dir, "scheme.log"), "BUILDER_CI_DIR="+filepath.Join(dir, "state"))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("runner: %s %v", out, err)
		}
		if _, err := os.Stat(filepath.Join(clone, "build", "abcdef12.ipa")); err != nil {
			t.Fatal("runner produced no IPA:", err)
		}
		data, err := os.ReadFile(filepath.Join(dir, "scheme.log"))
		if err != nil || string(data) != scheme {
			t.Fatalf("scheme changed: %q %v", data, err)
		}
		if _, err := os.Stat(filepath.Join(clone, "should-not-exist")); !os.IsNotExist(err) {
			t.Fatal("scheme was evaluated as shell")
		}
	})
	git(source, "push", "origin", "--delete", ref)
	if out, err := run(sha); err == nil {
		t.Fatalf("accepted missing remote ref: %s", out)
	}
}

// shellFunction returns the body of the named shell function from a template,
// dedented, so the same function can be checked for drift between templates
// and run under stubs.
func shellFunction(t *testing.T, data []byte, name string) string {
	t.Helper()
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	for i, line := range lines {
		trimmed := strings.TrimLeft(line, " ")
		if trimmed != name+"() {" {
			continue
		}
		indent := line[:len(line)-len(trimmed)]
		var body []string
		for _, l := range lines[i:] {
			body = append(body, strings.TrimPrefix(l, indent))
			if l == indent+"}" {
				return strings.Join(body, "\n") + "\n"
			}
		}
	}
	t.Fatalf("function %s not found", name)
	return ""
}

func TestApplyBuildNumber(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("macOS/Linux shell test")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq unavailable for the xcodebuild stub")
	}
	runner, err := GetTemplate("runner.sh")
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := GetTemplate("ios-build.yml")
	if err != nil {
		t.Fatal(err)
	}
	fn := shellFunction(t, runner, "apply_build_number")
	if got := shellFunction(t, workflow, "apply_build_number"); got != fn {
		t.Fatalf("apply_build_number differs between runner.sh and ios-build.yml:\n%s\n---\n%s", fn, got)
	}

	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	// xcodebuild answers -showBuildSettings with the app target's plist path
	// and records that it was asked; plutil works on key=value files.
	stubs := map[string]string{
		"xcodebuild": `#!/bin/bash
echo "$@" >> "$STUB_LOG"
printf '[{"buildSettings":{"PRODUCT_TYPE":"com.apple.product-type.application","SRCROOT":"%s","INFOPLIST_FILE":"%s"}}]\n' "$SRCROOT" "$INFOPLIST_FILE"
`,
		"plutil": `#!/bin/bash
echo "plutil $@" >> "$STUB_LOG"
case "$1" in
  -extract) grep "^$2=" "$6" | cut -d= -f2- ;;
  -replace) grep -v "^$2=" "$5" > "$5.tmp"; echo "$2=$4" >> "$5.tmp"; mv "$5.tmp" "$5" ;;
  *) exit 2 ;;
esac
`,
	}
	for name, body := range stubs {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0755); err != nil {
			t.Fatal(err)
		}
	}
	script := filepath.Join(dir, "apply.sh")
	if err := os.WriteFile(script, []byte("set -euo pipefail\n"+fn+`apply_build_number "$@"
printf '%s|%s|%s\n' "$build_number" "$build_name" "$version_settings"
`), 0644); err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		name, input, plist, want, wantPlist string
		noPlist, fail                       bool
	}{
		{name: "unset leaves the build alone", input: "", want: "||"},
		{name: "project version setting", input: "42", plist: "CFBundleVersion=$(CURRENT_PROJECT_VERSION)\n", want: "42||CURRENT_PROJECT_VERSION=42", wantPlist: "CFBundleVersion=$(CURRENT_PROJECT_VERSION)\n"},
		{name: "flutter setting", input: "42", plist: "CFBundleVersion=$(FLUTTER_BUILD_NUMBER)\n", want: "42||CURRENT_PROJECT_VERSION=42", wantPlist: "CFBundleVersion=$(FLUTTER_BUILD_NUMBER)\n"},
		{name: "hardcoded plist", input: "1.2.3+42", plist: "CFBundleVersion=7\nCFBundleShortVersionString=1.0\n", want: "42|1.2.3|CURRENT_PROJECT_VERSION=42 MARKETING_VERSION=1.2.3", wantPlist: "CFBundleVersion=42\nCFBundleShortVersionString=1.2.3\n"},
		{name: "generated plist", input: "42", noPlist: true, want: "42||CURRENT_PROJECT_VERSION=42"},
		{name: "rejects shell metacharacters", input: "42; touch pwned", fail: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			work := t.TempDir()
			plist := filepath.Join(work, "Info.plist")
			infoPlistFile := "Info.plist"
			if tt.noPlist {
				infoPlistFile = ""
			} else if err := os.WriteFile(plist, []byte(tt.plist), 0644); err != nil {
				t.Fatal(err)
			}
			log := filepath.Join(work, "stub.log")
			cmd := exec.Command("bash", script, "-project", "App.xcodeproj", "-scheme", "App")
			cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"BUILD_NUMBER="+tt.input, "SRCROOT="+work, "INFOPLIST_FILE="+infoPlistFile, "STUB_LOG="+log)
			out, err := cmd.CombinedOutput()
			if tt.fail {
				if err == nil {
					t.Fatalf("accepted %q: %s", tt.input, out)
				}
				return
			}
			if err != nil {
				t.Fatalf("%s %v", out, err)
			}
			lines := strings.Split(strings.TrimSpace(string(out)), "\n")
			if got := lines[len(lines)-1]; got != tt.want {
				t.Errorf("result = %q, want %q\n%s", got, tt.want, out)
			}
			calls, _ := os.ReadFile(log)
			if tt.input == "" && len(calls) != 0 {
				t.Errorf("tools invoked without a build number: %s", calls)
			}
			if tt.noPlist && strings.Contains(string(calls), "plutil") {
				t.Errorf("plutil run without a plist: %s", calls)
			}
			if tt.wantPlist != "" {
				if got, _ := os.ReadFile(plist); string(got) != tt.wantPlist {
					t.Errorf("plist = %q, want %q", got, tt.wantPlist)
				}
			}
		})
	}
}

func TestBitriseSSHActivationRequiresKey(t *testing.T) {
	data, err := GetTemplate("bitrise.yml")
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Workflows map[string]struct {
			Steps []map[string]struct {
				RunIf string `yaml:"run_if"`
			}
		}
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	for name, workflow := range config.Workflows {
		gate := workflow.Steps[0]["activate-ssh-key@4"].RunIf
		for _, key := range []string{"", "test-private-key"} {
			tmpl, err := template.New("run_if").Funcs(template.FuncMap{"getenv": func(string) string { return key }}).Parse(gate)
			if err != nil {
				t.Fatal(err)
			}
			var output strings.Builder
			if err := tmpl.Execute(&output, nil); err != nil {
				t.Fatal(err)
			}
			want := "false"
			if key != "" {
				want = "true"
			}
			if output.String() != want {
				t.Fatalf("%s: SSH activation = %q, want %s", name, output.String(), want)
			}
		}
	}
}
