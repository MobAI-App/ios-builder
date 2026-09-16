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
printf '%s' "${API_URL:-}|${NOTES:-}|${DISTRIBUTION:-}" > "$ENV_LOG"
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
		// The profile env arrives as one JSON object and must reach the build
		// tools as ordinary variables, values intact.
		buildEnv := `{"API_URL":"https://staging.example.com","NOTES":"line one\nline \"two\""}`
		cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "SNAPSHOT_REF="+ref, "SNAPSHOT_SHA="+sha, "BUILD_ID=abcdef12", "IOS_PATH=.", "USE_SIGNING=false", "CONFIGURATION=Debug", "SCHEME="+scheme, "SCHEME_LOG="+filepath.Join(dir, "scheme.log"), "BUILDER_CI_DIR="+filepath.Join(dir, "state"),
			"BUILD_ENV="+buildEnv, "DISTRIBUTION=ad-hoc", "ENV_LOG="+filepath.Join(dir, "env.log"))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("runner: %s %v", out, err)
		}
		if data, err := os.ReadFile(filepath.Join(dir, "env.log")); err != nil || string(data) != "https://staging.example.com|line one\nline \"two\"|ad-hoc" {
			t.Fatalf("profile env did not reach the build: %q %v", data, err)
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

// shellFunc pulls a shell function body out of a template so the same code the
// runner executes can be exercised here. Works for runner.sh and for the
// indented run: blocks of the workflow YAML.
func shellFunc(t *testing.T, template, name string) string {
	t.Helper()
	// Windows checkouts may have CRLF line endings; the closing brace
	// comparison below needs bare lines.
	lines := strings.Split(strings.ReplaceAll(template, "\r\n", "\n"), "\n")
	start := -1
	indent := ""
	for i, line := range lines {
		if strings.TrimSpace(line) == name+"() {" {
			start = i
			indent = line[:len(line)-len(strings.TrimLeft(line, " "))]
			break
		}
	}
	if start < 0 {
		t.Fatalf("%s not found", name)
	}
	for i := start; i < len(lines); i++ {
		if i > start && lines[i] == indent+"}" {
			body := lines[start : i+1]
			for j, line := range body {
				body[j] = strings.TrimPrefix(line, indent)
			}
			return strings.Join(body, "\n")
		}
	}
	t.Fatalf("%s not terminated", name)
	return ""
}

func TestExportMethodFollowsProfile(t *testing.T) {
	workflowTemplate, err := GetWorkflowTemplate()
	if err != nil {
		t.Fatal(err)
	}
	runner, err := GetTemplate("runner.sh")
	if err != nil {
		t.Fatal(err)
	}
	fromWorkflow := shellFunc(t, string(workflowTemplate), "detect_export_method")
	fromRunner := shellFunc(t, string(runner), "detect_export_method")
	if fromWorkflow != fromRunner {
		t.Fatalf("templates disagree on the export method:\n%s\n---\n%s", fromWorkflow, fromRunner)
	}
	// Both must refuse a Debug distribution build, whose get-task-allow
	// entitlement no distribution profile grants, and both must feed the
	// detected method — not a constant — into ExportOptions.plist.
	wiring := map[string][]string{
		"ios-build.yml": {
			`EXPORT_METHOD=$(detect_export_method "$PROFILE_PLIST")`,
			`"    <string>${EXPORT_METHOD}</string>"`,
			"plutil -insert manageAppVersionAndBuildNumber -bool NO",
		},
		"runner.sh": {
			`detect_export_method "$signing_dir/profile.plist"`,
			`'method': os.environ['EXPORT_METHOD']`,
			"options['manageAppVersionAndBuildNumber'] = False",
		},
	}
	for name, data := range map[string]string{"ios-build.yml": string(workflowTemplate), "runner.sh": string(runner)} {
		if !strings.Contains(data, `configuration\": \"Release`) {
			t.Errorf("%s: no Debug + distribution guard", name)
		}
		if strings.Contains(data, "<string>development</string>") || strings.Contains(data, "'method': 'development'") {
			t.Errorf("%s: export method still hardcoded", name)
		}
		for _, want := range wiring[name] {
			if !strings.Contains(data, want) {
				t.Errorf("%s: export options no longer wired to the profile, missing %q", name, want)
			}
		}
	}

	if runtime.GOOS != "darwin" {
		t.Skip("plutil is macOS only")
	}
	profile := func(body string) string {
		return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>` + body + `</dict></plist>`
	}
	devices := "<key>ProvisionedDevices</key><array><string>00008030-001</string></array>"
	allow := func(v bool) string {
		if v {
			return "<key>Entitlements</key><dict><key>get-task-allow</key><true/></dict>"
		}
		return "<key>Entitlements</key><dict><key>get-task-allow</key><false/></dict>"
	}
	cases := []struct{ name, plist, want string }{
		{"development", profile(devices + allow(true)), "development"},
		{"adhoc", profile(devices + allow(false)), "ad-hoc"},
		{"appstore", profile(allow(false)), "app-store"},
		{"enterprise", profile("<key>ProvisionsAllDevices</key><true/>" + allow(false)), "enterprise"},
		// An enterprise profile ships devices too on some accounts; it still wins.
		{"enterprise with devices", profile("<key>ProvisionsAllDevices</key><true/>" + devices + allow(false)), "enterprise"},
	}
	dir := t.TempDir()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, tc.name+".plist")
			if err := os.WriteFile(path, []byte(tc.plist), 0644); err != nil {
				t.Fatal(err)
			}
			out, err := exec.Command("bash", "-c", fromRunner+"\ndetect_export_method \"$1\"", "bash", path).CombinedOutput()
			if err != nil {
				t.Fatalf("%s %v", out, err)
			}
			if got := strings.TrimSpace(string(out)); got != tc.want {
				t.Fatalf("method = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSigningSetSelection runs the set selection and profile check the way
// the signing step does, with stub secrets, on the function bodies both
// templates carry.
func TestSigningSetSelection(t *testing.T) {
	workflowTemplate, err := GetWorkflowTemplate()
	if err != nil {
		t.Fatal(err)
	}
	runner, err := GetTemplate("runner.sh")
	if err != nil {
		t.Fatal(err)
	}
	var shared string
	for _, name := range []string{"signing_set", "select_signing_set", "check_signing_set"} {
		fromWorkflow := shellFunc(t, string(workflowTemplate), name)
		fromRunner := shellFunc(t, string(runner), name)
		if fromWorkflow != fromRunner {
			t.Fatalf("templates disagree on %s:\n%s\n---\n%s", name, fromWorkflow, fromRunner)
		}
		shared += fromRunner + "\n"
	}
	// Both templates feed the detected method into the check, after the
	// selection, and read the set the resolve step emits.
	for name, data := range map[string]string{"ios-build.yml": string(workflowTemplate), "runner.sh": string(runner)} {
		for _, want := range []string{"select_signing_set\n", `check_signing_set "$EXPORT_METHOD"`} {
			if !strings.Contains(data, want) {
				t.Errorf("%s: missing %q", name, want)
			}
		}
	}
	if !strings.Contains(string(workflowTemplate), `SIGNING_SET: ${{ steps.params.outputs.signing_set }}`) || !strings.Contains(string(runner), `SIGNING_SET=$(signing_set "$DISTRIBUTION")`) {
		t.Error("SIGNING_SET is not derived from the distribution")
	}
	for _, set := range []string{"DEVELOPMENT", "AD_HOC", "APP_STORE", "ENTERPRISE"} {
		for _, secret := range []string{"IOS_CERTIFICATE_", "IOS_CERTIFICATE_PASSWORD_", "IOS_PROVISIONING_PROFILE_"} {
			if line := secret + set + ": ${{ secrets." + secret + set + " }}"; !strings.Contains(string(workflowTemplate), line) {
				t.Errorf("ios-build.yml does not pass %s%s to the signing step", secret, set)
			}
		}
	}

	if runtime.GOOS == "windows" {
		t.Skip("shell test")
	}
	script := "set -e\nfail() { echo \"$*\" >&2; exit 1; }\n" + shared +
		"SIGNING_SET=$(signing_set \"$DISTRIBUTION\") || fail \"bad distribution $DISTRIBUTION\"\n" +
		"select_signing_set\ncheck_signing_set \"$METHOD\"\n" +
		"printf '%s|%s|%s|%s' \"$IOS_CERTIFICATE\" \"$IOS_CERTIFICATE_PASSWORD\" \"$IOS_PROVISIONING_PROFILE\" \"$SIGNING_SET_USED\"\n"
	run := func(env map[string]string) (string, error) {
		cmd := exec.Command("bash", "-c", script)
		// A bare environment: none of the secrets can leak in from the host.
		cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
		for k, v := range env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	legacy := map[string]string{"IOS_CERTIFICATE": "legacy-cert", "IOS_CERTIFICATE_PASSWORD": "legacy-pw", "IOS_PROVISIONING_PROFILE": "legacy-profile"}
	appStore := map[string]string{"IOS_CERTIFICATE_APP_STORE": "store-cert", "IOS_CERTIFICATE_PASSWORD_APP_STORE": "store-pw", "IOS_PROVISIONING_PROFILE_APP_STORE": "store-profile"}
	with := func(sets ...map[string]string) map[string]string {
		env := map[string]string{}
		for _, s := range sets {
			for k, v := range s {
				env[k] = v
			}
		}
		return env
	}

	for _, tc := range []struct {
		name string
		env  map[string]string
		want string // "" expects a failure whose message holds wantErr
		errs []string
	}{
		{"suffixed set present", with(legacy, appStore, map[string]string{"DISTRIBUTION": "app-store", "METHOD": "app-store"}), "store-cert|store-pw|store-profile|APP_STORE", nil},
		{"suffixed set with empty password", with(appStore, map[string]string{"IOS_CERTIFICATE_PASSWORD_APP_STORE": "", "DISTRIBUTION": "app-store", "METHOD": "app-store"}), "store-cert||store-profile|APP_STORE", nil},
		{"only legacy, no distribution, any profile type", with(legacy, map[string]string{"DISTRIBUTION": "", "METHOD": "ad-hoc"}), "legacy-cert|legacy-pw|legacy-profile|legacy", nil},
		{"only legacy, requested distribution matches", with(legacy, map[string]string{"DISTRIBUTION": "app-store", "METHOD": "app-store"}), "legacy-cert|legacy-pw|legacy-profile|legacy", nil},
		{"development set for no distribution", with(legacy, map[string]string{"IOS_CERTIFICATE_DEVELOPMENT": "dev-cert", "IOS_CERTIFICATE_PASSWORD_DEVELOPMENT": "dev-pw", "IOS_PROVISIONING_PROFILE_DEVELOPMENT": "dev-profile", "DISTRIBUTION": "", "METHOD": "development"}), "dev-cert|dev-pw|dev-profile|DEVELOPMENT", nil},
		{"requested set absent, legacy absent", map[string]string{"DISTRIBUTION": "ad-hoc", "METHOD": "ad-hoc"}, "", []string{"IOS_CERTIFICATE_AD_HOC", "IOS_CERTIFICATE_PASSWORD_AD_HOC", "IOS_PROVISIONING_PROFILE_AD_HOC", "unsuffixed IOS_CERTIFICATE", "--type ad-hoc"}},
		{"suffixed set missing its profile", with(map[string]string{"IOS_CERTIFICATE_APP_STORE": "store-cert", "DISTRIBUTION": "app-store", "METHOD": "app-store"}), "", []string{"incomplete", "IOS_PROVISIONING_PROFILE_APP_STORE"}},
		{"legacy profile of the wrong type", with(legacy, map[string]string{"DISTRIBUTION": "app-store", "METHOD": "development"}), "", []string{"unsuffixed IOS_PROVISIONING_PROFILE", "development provisioning profile", "distribution app-store", "APP_STORE", "--type app-store"}},
		{"suffixed profile of the wrong type", with(appStore, map[string]string{"DISTRIBUTION": "app-store", "METHOD": "ad-hoc"}), "", []string{"IOS_PROVISIONING_PROFILE_APP_STORE holds a ad-hoc", "distribution app-store"}},
		{"development set holding a distribution profile", with(map[string]string{"IOS_CERTIFICATE_DEVELOPMENT": "c", "IOS_PROVISIONING_PROFILE_DEVELOPMENT": "p", "DISTRIBUTION": "", "METHOD": "app-store"}), "", []string{"IOS_PROVISIONING_PROFILE_DEVELOPMENT", "distribution development"}},
		{"unknown distribution", with(legacy, map[string]string{"DISTRIBUTION": "adhoc", "METHOD": "ad-hoc"}), "", []string{"bad distribution adhoc"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := run(tc.env)
			if tc.want != "" {
				if err != nil {
					t.Fatalf("%v\n%s", err, out)
				}
				if !strings.HasSuffix(out, tc.want) {
					t.Fatalf("selected %q, want suffix %q", out, tc.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("accepted:\n%s", out)
			}
			for _, want := range tc.errs {
				if !strings.Contains(out, want) {
					t.Errorf("error does not mention %q:\n%s", want, out)
				}
			}
		})
	}
}

func TestWorkflowTemplatesParse(t *testing.T) {
	for _, name := range []string{"ios-build.yml", "ios-share.yml"} {
		data, err := GetTemplate(name)
		if err != nil {
			t.Fatal(err)
		}
		var parsed struct {
			Jobs map[string]struct {
				Steps []map[string]any
			}
		}
		if err := yaml.Unmarshal(data, &parsed); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(parsed.Jobs) == 0 {
			t.Fatalf("%s: no jobs", name)
		}
		for job, spec := range parsed.Jobs {
			if len(spec.Steps) == 0 {
				t.Fatalf("%s: job %s has no steps", name, job)
			}
		}
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
