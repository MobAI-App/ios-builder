package workflow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"text/template"

	"github.com/MobAI-App/ios-builder/internal/signing"
	"github.com/MobAI-App/ios-builder/internal/xcodeproj"
	"go.yaml.in/yaml/v3"
	"howett.net/plist"
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
		// tools as ordinary variables, values intact. BUILD_NUMBER=17 plays
		// Codemagic's own build counter, which a plain build must not stamp
		// on the app.
		buildEnv := `{"API_URL":"https://staging.example.com","NOTES":"line one\nline \"two\""}`
		// The hooks arrive merged as one JSON object; each records the
		// BUILDER_* variables it sees, preBuild before any xcodebuild call
		// and postBuild with the IPA.
		hookLog := filepath.Join(dir, "hook.log")
		buildHooks := `{"preBuild":"printf 'pre|%s|%s|%s|%s|%s\\n' \"$BUILDER_HOOK\" \"$BUILDER_PROJECT_TYPE\" \"$BUILDER_PROFILE\" \"$API_URL\" \"$(ls \"$SCHEME_LOG\" 2>/dev/null)\" >> \"$HOOK_LOG\"\ntest \"$PWD\" = \"$BUILDER_WORKSPACE\"",` +
			`"postBuild":"printf 'post|%s|%s|%s\\n' \"$BUILDER_HOOK\" \"$BUILDER_IPA\" \"$BUILDER_BUILD_NUMBER\" >> \"$HOOK_LOG\"\ntest -f \"$BUILDER_IPA\""}`
		cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "SNAPSHOT_REF="+ref, "SNAPSHOT_SHA="+sha, "BUILD_ID=abcdef12", "IOS_PATH=.", "USE_SIGNING=false", "CONFIGURATION=Debug", "BUILD_NUMBER=17", "SCHEME="+scheme, "SCHEME_LOG="+filepath.Join(dir, "scheme.log"), "BUILDER_CI_DIR="+filepath.Join(dir, "state"),
			"BUILD_ENV="+buildEnv, "DISTRIBUTION=ad-hoc", "ENV_LOG="+filepath.Join(dir, "env.log"),
			"BUILD_PROFILE=preview", "BUILD_HOOKS="+buildHooks, "HOOK_LOG="+hookLog,
			// A GitHub-hosted test run must not point the hooks at its own checkout.
			"GITHUB_WORKSPACE=", "GITHUB_ACTIONS=")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("runner: %s %v", out, err)
		}
		if data, err := os.ReadFile(filepath.Join(dir, "env.log")); err != nil || string(data) != "https://staging.example.com|line one\nline \"two\"|ad-hoc" {
			t.Fatalf("profile env did not reach the build: %q %v", data, err)
		}
		// preBuild saw no scheme.log yet (xcodebuild had not run), the
		// profile's env and name; postBuild got the IPA and no build number.
		// BUILDER_WORKSPACE is $(pwd) after the checkout, so macOS's /var symlink is resolved.
		realClone, err := filepath.EvalSymlinks(clone)
		if err != nil {
			t.Fatal(err)
		}
		wantHooks := "pre|preBuild|native|preview|https://staging.example.com|\npost|postBuild|" + filepath.Join(realClone, "build", "abcdef12.ipa") + "|\n"
		if data, err := os.ReadFile(hookLog); err != nil || string(data) != wantHooks {
			t.Fatalf("hooks did not run as expected: %q %v, want %q", data, err, wantHooks)
		}
		if _, err := os.Stat(filepath.Join(dir, "scheme.log.stamped")); !os.IsNotExist(err) {
			t.Fatal("the provider's BUILD_NUMBER was stamped on a plain build")
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
	var fromRunner string
	for _, fn := range []string{"detect_export_method", "write_export_options"} {
		fromWorkflow, fromRunnerFn := shellFunc(t, string(workflowTemplate), fn), shellFunc(t, string(runner), fn)
		if fromWorkflow != fromRunnerFn {
			t.Fatalf("templates disagree on %s:\n%s\n---\n%s", fn, fromWorkflow, fromRunnerFn)
		}
		if fn == "detect_export_method" {
			fromRunner = fromRunnerFn
		}
	}
	// Both must refuse a Debug distribution build, whose get-task-allow
	// entitlement no distribution profile grants, and both must feed the
	// detected method — not a constant — into ExportOptions.plist.
	wiring := map[string][]string{
		"ios-build.yml": {
			`EXPORT_METHOD=$(detect_export_method "$PROFILE_PLIST")`,
			"write_export_options ExportOptions.plist",
		},
		"runner.sh": {
			`detect_export_method "$signing_dir/profile.plist"`,
			`write_export_options "$signing_dir/ExportOptions.plist"`,
		},
	}
	for name, data := range map[string]string{"ios-build.yml": string(workflowTemplate), "runner.sh": string(runner)} {
		if !strings.Contains(data, `configuration\": \"Release`) {
			t.Errorf("%s: no Debug + distribution guard", name)
		}
		if strings.Contains(data, "<string>development</string>") || strings.Contains(data, "'method': 'development'") {
			t.Errorf("%s: export method still hardcoded", name)
		}
		for _, want := range append(wiring[name], `'method': os.environ['EXPORT_METHOD']`, "options['manageAppVersionAndBuildNumber'] = False") {
			if !strings.Contains(data, want) {
				t.Errorf("%s: export options no longer wired to the profile, missing %q", name, want)
			}
		}
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
		{"explicit ProvisionsAllDevices false", profile("<key>ProvisionsAllDevices</key><false/>" + allow(false)), "app-store"},
		{"no entitlements", profile(devices), "ad-hoc"},
	}
	// signing setup reads the same type locally, from the plist inside the
	// CMS blob, so the Go rules must agree with the shell's on every case;
	// the export method's app-store is the store distribution.
	for _, tc := range cases {
		want := tc.want
		if want == "app-store" {
			want = "store"
		}
		if got, err := signing.ProfileType([]byte("\x30\x82cms" + tc.plist + "\x00\xff")); err != nil || string(got) != want {
			t.Errorf("%s: signing.ProfileType = %q, %v; detect_export_method says %q", tc.name, got, err, tc.want)
		}
	}
	if runtime.GOOS != "darwin" {
		t.Skip("plutil is macOS only")
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

// TestSigningIdentityFollowsProfileType holds both templates to the identity
// the profile's type needs. An archive without an explicit CODE_SIGN_IDENTITY
// keeps the project's default ("Apple Development"), which Xcode refuses to
// pair with a distribution profile: "No signing certificate iOS Development
// found".
func TestSigningIdentityFollowsProfileType(t *testing.T) {
	workflowTemplate, err := GetWorkflowTemplate()
	if err != nil {
		t.Fatal(err)
	}
	runner, err := GetTemplate("runner.sh")
	if err != nil {
		t.Fatal(err)
	}
	var shared string
	for _, fn := range []string{"signing_identities", "signing_identity"} {
		fromWorkflow := shellFunc(t, string(workflowTemplate), fn)
		fromRunner := shellFunc(t, string(runner), fn)
		if fromWorkflow != fromRunner {
			t.Fatalf("templates disagree on %s:\n%s\n---\n%s", fn, fromWorkflow, fromRunner)
		}
		shared += fromRunner + "\n"
	}

	wiring := map[string][]string{
		"ios-build.yml": {
			`CODE_SIGN_IDENTITY=$(signing_identity "$EXPORT_METHOD" "$IDENTITIES")`,
			`echo "CODE_SIGN_IDENTITY=$CODE_SIGN_IDENTITY" >> $GITHUB_ENV`,
		},
		"runner.sh": {
			`CODE_SIGN_IDENTITY=$(signing_identity "$EXPORT_METHOD" "$identities")`,
			"export CODE_SIGN_IDENTITY",
		},
	}
	for name, data := range map[string]string{"ios-build.yml": string(workflowTemplate), "runner.sh": string(runner)} {
		data = strings.ReplaceAll(data, "\r\n", "\n") // Windows checkouts
		for _, want := range wiring[name] {
			if !strings.Contains(data, want) {
				t.Errorf("%s: the identity is not derived from the profile, missing %q", name, want)
			}
		}
		// The identity reaches the app target through apply_signing_to_app_target
		// (TestSigningSettingsOnAppTargetOnly), which reads it from the
		// environment together with CODE_SIGN_STYLE=Manual; a command line that
		// set the style without it would sign with the project's default.
		fn := shellFunc(t, data, "apply_signing_to_app_target")
		if !strings.Contains(fn, "'CODE_SIGN_IDENTITY': os.environ['CODE_SIGN_IDENTITY']") || !strings.Contains(fn, "'CODE_SIGN_STYLE': 'Manual'") {
			t.Errorf("%s: apply_signing_to_app_target does not set the identity with the manual style", name)
		}
		// The imported certificate is checked against that identity, after the
		// import and before the archive, so a distribution set holding a
		// development certificate fails in seconds instead of minutes.
		imported, checked := strings.Index(data, "security import "), strings.Index(data, "security find-identity -v -p codesigning")
		if imported < 0 || checked < 0 {
			t.Errorf("%s: import %d, identity check %d", name, imported, checked)
		} else if checked < imported {
			t.Errorf("%s: the identity check must run after security import (offsets %d, %d)", name, imported, checked)
		}
	}

	if runtime.GOOS == "windows" {
		t.Skip("shell test")
	}
	// security find-identity prints one line per identity; certificates issued
	// before Apple's 2021 rename still say iPhone Developer / iPhone
	// Distribution and sign the same profiles, so they must be accepted.
	line := func(names ...string) string {
		out := ""
		for i, n := range names {
			out += fmt.Sprintf("  %d) DEADBEEF \"%s: Some One (2638BTZ9X7)\"\n", i+1, n)
		}
		return out + fmt.Sprintf("     %d valid identities found", len(names))
	}
	for _, tc := range []struct{ name, method, identities, want string }{
		{"development", "development", line("Apple Development"), "Apple Development"},
		{"legacy development", "development", line("iPhone Developer"), "iPhone Developer"},
		{"both development names", "development", line("iPhone Developer", "Apple Development"), "Apple Development"},
		{"ad-hoc", "ad-hoc", line("Apple Distribution"), "Apple Distribution"},
		{"app-store", "app-store", line("Apple Distribution"), "Apple Distribution"},
		{"enterprise", "enterprise", line("Apple Distribution"), "Apple Distribution"},
		{"legacy distribution", "app-store", line("iPhone Distribution"), "iPhone Distribution"},
		{"both distribution names", "app-store", line("iPhone Distribution", "Apple Distribution"), "Apple Distribution"},
		// A development certificate cannot sign a distribution profile, and
		// an unknown method must never sign with a guess.
		{"development certificate in a store set", "app-store", line("Apple Development", "iPhone Developer"), ""},
		{"distribution certificate in a development set", "development", line("Apple Distribution"), ""},
		{"empty keychain", "app-store", line(), ""},
		{"unknown method", "nonsense", line("Apple Distribution"), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := exec.Command("bash", "-c", shared+"\nsigning_identity \"$1\" \"$2\"", "bash", tc.method, tc.identities).CombinedOutput()
			if tc.want == "" {
				if err == nil {
					t.Fatalf("accepted %q for %s: %s", tc.identities, tc.method, out)
				}
				return
			}
			if err != nil {
				t.Fatalf("%s %v", out, err)
			}
			if got := strings.TrimSpace(string(out)); got != tc.want {
				t.Fatalf("identity = %q, want %q", got, tc.want)
			}
		})
	}
}

// pbxTarget describes one native target of a test project.
type pbxTarget struct {
	name, productType, bundleID string
	extra                       map[string]string // more build settings on both configurations
}

// pbxproj writes a minimal OpenStep-format project.pbxproj with Debug and
// Release configurations for each target, the way Xcode lays one out.
func pbxproj(targets ...pbxTarget) string {
	var b strings.Builder
	b.WriteString("// !$*UTF8*$!\n{\n\tarchiveVersion = 1;\n\tclasses = {\n\t};\n\tobjectVersion = 56;\n\tobjects = {\n")
	b.WriteString("\t\tP0 = {\n\t\t\tisa = PBXProject;\n\t\t\tbuildConfigurationList = L0;\n\t\t\tcompatibilityVersion = \"Xcode 14.0\";\n\t\t\tmainGroup = G0;\n\t\t\tproductRefGroup = G0;\n\t\t\tprojectDirPath = \"\";\n\t\t\tprojectRoot = \"\";\n\t\t\ttargets = (\n")
	for i := range targets {
		fmt.Fprintf(&b, "\t\t\t\tT%d,\n", i)
	}
	b.WriteString("\t\t\t);\n\t\t};\n\t\tG0 = {\n\t\t\tisa = PBXGroup;\n\t\t\tchildren = (\n\t\t\t);\n\t\t\tsourceTree = \"<group>\";\n\t\t};\n")
	configList := func(id string, settings map[string]string) {
		fmt.Fprintf(&b, "\t\t%s = {\n\t\t\tisa = XCConfigurationList;\n\t\t\tbuildConfigurations = (\n\t\t\t\t%sD,\n\t\t\t\t%sR,\n\t\t\t);\n\t\t\tdefaultConfigurationIsVisible = 0;\n\t\t\tdefaultConfigurationName = Release;\n\t\t};\n", id, id, id)
		for _, c := range []struct{ suffix, name string }{{"D", "Debug"}, {"R", "Release"}} {
			fmt.Fprintf(&b, "\t\t%s%s = {\n\t\t\tisa = XCBuildConfiguration;\n\t\t\tbuildSettings = {\n", id, c.suffix)
			for k, v := range settings {
				fmt.Fprintf(&b, "\t\t\t\t%q = %q;\n", k, v)
			}
			fmt.Fprintf(&b, "\t\t\t};\n\t\t\tname = %s;\n\t\t};\n", c.name)
		}
	}
	configList("L0", map[string]string{"SDKROOT": "iphoneos"})
	for i, tg := range targets {
		fmt.Fprintf(&b, "\t\tT%d = {\n\t\t\tisa = PBXNativeTarget;\n\t\t\tbuildConfigurationList = L%d;\n\t\t\tbuildPhases = (\n\t\t\t);\n\t\t\tbuildRules = (\n\t\t\t);\n\t\t\tdependencies = (\n\t\t\t);\n\t\t\tname = %s;\n\t\t\tproductName = %s;\n\t\t\tproductReference = F%d;\n\t\t\tproductType = %q;\n\t\t};\n", i, i+1, tg.name, tg.name, i, tg.productType)
		fmt.Fprintf(&b, "\t\tF%d = {isa = PBXFileReference; explicitFileType = wrapper.application; includeInIndex = 0; path = %s.app; sourceTree = BUILT_PRODUCTS_DIR; };\n", i, tg.name)
		settings := map[string]string{"CODE_SIGN_STYLE": "Automatic", "PRODUCT_BUNDLE_IDENTIFIER": tg.bundleID, "PRODUCT_NAME": "$(TARGET_NAME)"}
		for k, v := range tg.extra {
			settings[k] = v
		}
		configList(fmt.Sprintf("L%d", i+1), settings)
	}
	b.WriteString("\t};\n\trootObject = P0;\n}\n")
	return b.String()
}

// pbxSettings reads the build settings of every configuration of every native
// target of a project, as target → configuration → settings.
func pbxSettings(t *testing.T, project string) map[string]map[string]map[string]string {
	t.Helper()
	out, err := exec.Command("plutil", "-convert", "json", "-o", "-", filepath.Join(project, "project.pbxproj")).CombinedOutput()
	if err != nil {
		t.Fatalf("plutil: %s %v", out, err)
	}
	var parsed struct {
		Objects map[string]struct {
			Isa                    string            `json:"isa"`
			Name                   string            `json:"name"`
			BuildConfigurationList string            `json:"buildConfigurationList"`
			BuildConfigurations    []string          `json:"buildConfigurations"`
			BuildSettings          map[string]string `json:"buildSettings"`
		} `json:"objects"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatal(err)
	}
	result := map[string]map[string]map[string]string{}
	for _, o := range parsed.Objects {
		if o.Isa != "PBXNativeTarget" {
			continue
		}
		result[o.Name] = map[string]map[string]string{}
		for _, id := range parsed.Objects[o.BuildConfigurationList].BuildConfigurations {
			c := parsed.Objects[id]
			result[o.Name][c.Name] = c.BuildSettings
		}
	}
	return result
}

// TestSigningSettingsOnAppTargetOnly holds both templates to writing the
// manual signing settings into the app target's build configurations rather
// than passing them to xcodebuild, where every target in the workspace — the
// CocoaPods framework targets included — would inherit them: "FirebaseCore
// does not support provisioning profiles, but provisioning profile ... has
// been manually specified".
func TestSigningSettingsOnAppTargetOnly(t *testing.T) {
	workflowTemplate, err := GetWorkflowTemplate()
	if err != nil {
		t.Fatal(err)
	}
	runner, err := GetTemplate("runner.sh")
	if err != nil {
		t.Fatal(err)
	}
	fromWorkflow := shellFunc(t, string(workflowTemplate), "apply_signing_to_app_target")
	fromRunner := shellFunc(t, string(runner), "apply_signing_to_app_target")
	if fromWorkflow != fromRunner {
		t.Fatalf("templates disagree on apply_signing_to_app_target:\n%s\n---\n%s", fromWorkflow, fromRunner)
	}
	if n := strings.Count(fromRunner, "\n"); n > 90 {
		t.Errorf("apply_signing_to_app_target has grown to %d lines", n)
	}
	// The runner and the CLI must call the same targets extensions.
	for _, productType := range xcodeproj.ExtensionProductTypes {
		if !strings.Contains(fromRunner, "'"+productType+"'") {
			t.Errorf("apply_signing_to_app_target does not treat %s as an extension", productType)
		}
	}

	call := `apply_signing_to_app_target "$PROFILE_BUNDLE_ID"`
	wiring := map[string][]string{
		"ios-build.yml": {`echo "PROFILE_BUNDLE_ID=$PROFILE_BUNDLE_ID" >> $GITHUB_ENV`, `PROFILE_BUNDLE_ID=${APP_ID#"$TEAM_ID".}`},
		"runner.sh":     {`export PROFILE_BUNDLE_ID="${app_id#"$DEVELOPMENT_TEAM".}"`},
	}
	for name, data := range map[string]string{"ios-build.yml": string(workflowTemplate), "runner.sh": string(runner)} {
		data = strings.ReplaceAll(data, "\r\n", "\n") // Windows checkouts
		for _, want := range wiring[name] {
			if !strings.Contains(data, want) {
				t.Errorf("%s: the profile's app id does not reach the build, missing %q", name, want)
			}
		}
		// No signed archive passes the settings on the command line any more.
		for _, arg := range []string{"PROVISIONING_PROFILE_SPECIFIER=", "CODE_SIGN_STYLE=", `DEVELOPMENT_TEAM='$DEVELOPMENT_TEAM'`, `DEVELOPMENT_TEAM="$DEVELOPMENT_TEAM"`, `CODE_SIGN_IDENTITY='$CODE_SIGN_IDENTITY'`, `CODE_SIGN_IDENTITY="$CODE_SIGN_IDENTITY"`} {
			if strings.Contains(data, arg) {
				t.Errorf("%s: still passes %s to xcodebuild, which applies it to every Pods target too", name, arg)
			}
		}
		// Every archive is preceded by its own call, after the generated
		// projects exist: pod install and flutter build ios come first (the
		// runner's prepare, with expo prebuild, precedes build_ipa; on GitHub
		// the Build IPA step follows every setup step).
		archives, calls := strings.Count(data, `.xcarchive' archive`)+strings.Count(data, `.xcarchive" archive`), strings.Count(data, call)
		if archives == 0 || archives != calls {
			t.Errorf("%s: %d signed archives but %d calls of apply_signing_to_app_target", name, archives, calls)
		}
		steps := []string{"pod install\n", "flutter build ios"}
		if name == "runner.sh" {
			steps = append(steps, "expo prebuild")
		}
		for _, step := range steps {
			if first, applied := strings.Index(data, step), strings.Index(data, call); first < 0 || applied < first {
				t.Errorf("%s: apply_signing_to_app_target (offset %d) must run after %q (offset %d)", name, applied, step, first)
			}
		}
	}

	if runtime.GOOS != "darwin" {
		t.Skip("plutil is macOS only")
	}
	script := "set -euo pipefail\nfail() { echo \"$*\" >&2; exit 1; }\n" + fromRunner + "\ncd \"$1\"\napply_signing_to_app_target \"$2\"\n"
	want := map[string]string{"CODE_SIGN_STYLE": "Manual", "DEVELOPMENT_TEAM": "ABCDE12345", "PROVISIONING_PROFILE_SPECIFIER": "Builder store run.mobai.flicker", "CODE_SIGN_IDENTITY": "Apple Distribution"}
	// run applies the settings for the app id; extra is more environment,
	// such as the EXTENSION_PROFILES map the signing step installs.
	run := func(t *testing.T, dir, appID string, extra ...string) (string, error) {
		t.Helper()
		cmd := exec.Command("bash", "-c", script, "bash", dir, appID)
		cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "DEVELOPMENT_TEAM=" + want["DEVELOPMENT_TEAM"], "PROVISIONING_PROFILE_NAME=" + want["PROVISIONING_PROFILE_SPECIFIER"], "CODE_SIGN_IDENTITY=" + want["CODE_SIGN_IDENTITY"]}, extra...)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	write := func(t *testing.T, dir, project string, targets ...pbxTarget) string {
		t.Helper()
		path := filepath.Join(dir, project)
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "project.pbxproj"), []byte(pbxproj(targets...)), 0644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	// signedWith asserts the four settings, with the given profile, on both
	// configurations of a target and that nothing conditional is left to
	// override them; signed is the app's own profile.
	signedWith := func(t *testing.T, settings map[string]map[string]string, target, profile string) {
		t.Helper()
		for _, config := range []string{"Debug", "Release"} {
			for k, v := range want {
				if k == "PROVISIONING_PROFILE_SPECIFIER" {
					v = profile
				}
				if got := settings[config][k]; got != v {
					t.Errorf("%s %s: %s = %q, want %q", target, config, k, got, v)
				}
			}
			for k := range settings[config] {
				if strings.Contains(k, "[") {
					t.Errorf("%s %s: conditional %s left in place", target, config, k)
				}
			}
		}
	}
	signed := func(t *testing.T, settings map[string]map[string]string, target string) {
		t.Helper()
		signedWith(t, settings, target, want["PROVISIONING_PROFILE_SPECIFIER"])
	}
	untouched := func(t *testing.T, settings map[string]map[string]string, target string) {
		t.Helper()
		for _, config := range []string{"Debug", "Release"} {
			if s := settings[config]; s["CODE_SIGN_STYLE"] != "Automatic" || s["PROVISIONING_PROFILE_SPECIFIER"] != "" || s["DEVELOPMENT_TEAM"] != "" {
				t.Errorf("%s %s was signed: %v", target, config, s)
			}
		}
	}
	app := pbxTarget{"App", "com.apple.product-type.application", "run.mobai.flicker", map[string]string{"CODE_SIGN_IDENTITY[sdk=iphoneos*]": "iPhone Developer"}}
	other := pbxTarget{"Other", "com.apple.product-type.application", "run.mobai.other", nil}
	kit := pbxTarget{"Kit", "com.apple.product-type.framework", "run.mobai.Kit", nil}
	widget := pbxTarget{"Widget", "com.apple.product-type.app-extension", "run.mobai.flicker.widget", nil}

	t.Run("app target only", func(t *testing.T) {
		dir := t.TempDir()
		project := write(t, dir, "App.xcodeproj", app, kit)
		// The pods project sits a level down and is never a candidate.
		pods := write(t, filepath.Join(dir, "Pods"), "Pods.xcodeproj", pbxTarget{"FirebaseCore", "com.apple.product-type.framework", "org.cocoapods.FirebaseCore", nil})
		before, _ := os.ReadFile(filepath.Join(pods, "project.pbxproj"))
		out, err := run(t, dir, "run.mobai.flicker")
		if err != nil {
			t.Fatalf("%s %v", out, err)
		}
		if !strings.Contains(out, "target App in App.xcodeproj: Debug, Release") {
			t.Errorf("log does not say what changed: %s", out)
		}
		settings := pbxSettings(t, project)
		signed(t, settings["App"], "App")
		untouched(t, settings["Kit"], "Kit")
		after, _ := os.ReadFile(filepath.Join(pods, "project.pbxproj"))
		if !bytes.Equal(before, after) {
			t.Error("Pods.xcodeproj was rewritten")
		}
		data, err := os.ReadFile(filepath.Join(project, "project.pbxproj"))
		if err != nil || !strings.HasPrefix(string(data), "<?xml") {
			t.Errorf("project is not an XML plist: %.40q %v", data, err)
		}
		// Xcode must still read the rewritten project.
		if err := exec.Command("xcodebuild", "-version").Run(); err == nil {
			if out, err := exec.Command("xcodebuild", "-project", project, "-list", "-json").CombinedOutput(); err != nil || !strings.Contains(string(out), `"App"`) {
				t.Errorf("xcodebuild cannot read the rewritten project: %s %v", out, err)
			}
		}
	})
	t.Run("extension targets get their own profiles", func(t *testing.T) {
		// A wildcard entry covers the extensions under it, but an exact entry
		// is the more specific one and wins; the app keeps its own profile.
		dir := t.TempDir()
		share := pbxTarget{"Share", "com.apple.product-type.app-extension", "run.mobai.flicker.share", nil}
		project := write(t, dir, "App.xcodeproj", app, widget, share, kit)
		profiles := `{"run.mobai.flicker.widget": "Builder store run.mobai.flicker.widget", "run.mobai.flicker.*": "Wildcard extensions"}`
		out, err := run(t, dir, "run.mobai.flicker", "EXTENSION_PROFILES="+profiles)
		if err != nil {
			t.Fatalf("%s %v", out, err)
		}
		if !strings.Contains(out, "target Widget in App.xcodeproj: Debug, Release (profile Builder store run.mobai.flicker.widget)") {
			t.Errorf("log does not say what changed: %s", out)
		}
		settings := pbxSettings(t, project)
		signed(t, settings["App"], "App")
		signedWith(t, settings["Widget"], "Widget", "Builder store run.mobai.flicker.widget")
		signedWith(t, settings["Share"], "Share", "Wildcard extensions")
		untouched(t, settings["Kit"], "Kit")
	})
	t.Run("extension target without a profile", func(t *testing.T) {
		// The error names the target and its bundle id, says where to list it
		// and which setup to run; the project stays as it was.
		dir := t.TempDir()
		project := write(t, dir, "App.xcodeproj", app, widget)
		for _, env := range [][]string{nil, {"EXTENSION_PROFILES={}"}, {`EXTENSION_PROFILES={"run.mobai.other.widget": "Other"}`}} {
			out, err := run(t, dir, "run.mobai.flicker", append(env, "DISTRIBUTION=store")...)
			if err == nil {
				t.Fatalf("%v: accepted: %s", env, out)
			}
			for _, want := range []string{"Widget in App.xcodeproj (run.mobai.flicker.widget)", "ios.extensions", "builder signing setup --distribution store"} {
				if !strings.Contains(out, want) {
					t.Errorf("%v: error does not say %q: %s", env, want, out)
				}
			}
		}
		if data, _ := os.ReadFile(filepath.Join(project, "project.pbxproj")); !strings.HasPrefix(string(data), "// !$*UTF8*$!") {
			t.Error("project rewritten although the archive cannot be signed")
		}
	})
	t.Run("several apps: the one the profile covers", func(t *testing.T) {
		dir := t.TempDir()
		project := write(t, dir, "App.xcodeproj", other, app, kit)
		if out, err := run(t, dir, "run.mobai.flicker"); err != nil {
			t.Fatalf("%s %v", out, err)
		}
		settings := pbxSettings(t, project)
		signed(t, settings["App"], "App")
		untouched(t, settings["Other"], "Other")
		untouched(t, settings["Kit"], "Kit")
	})
	t.Run("wildcard profile covers every app", func(t *testing.T) {
		for _, appID := range []string{"*", "run.mobai.*"} {
			dir := t.TempDir()
			project := write(t, dir, "App.xcodeproj", app, other, kit)
			if out, err := run(t, dir, appID); err != nil {
				t.Fatalf("%s: %s %v", appID, out, err)
			}
			settings := pbxSettings(t, project)
			signed(t, settings["App"], "App")
			signed(t, settings["Other"], "Other")
			untouched(t, settings["Kit"], "Kit")
		}
	})
	t.Run("prefix wildcard covers only its apps", func(t *testing.T) {
		dir := t.TempDir()
		project := write(t, dir, "App.xcodeproj", app, pbxTarget{"Tool", "com.apple.product-type.application", "com.example.tool", nil})
		if out, err := run(t, dir, "run.mobai.*"); err != nil {
			t.Fatalf("%s %v", out, err)
		}
		settings := pbxSettings(t, project)
		signed(t, settings["App"], "App")
		untouched(t, settings["Tool"], "Tool")
	})
	t.Run("a single app is signed whatever its bundle id", func(t *testing.T) {
		// The export names the mismatch later; the profile's app id may also be
		// resolved from an xcconfig the project does not show.
		dir := t.TempDir()
		project := write(t, dir, "App.xcodeproj", app, kit)
		if out, err := run(t, dir, "com.example.elsewhere"); err != nil {
			t.Fatalf("%s %v", out, err)
		}
		signed(t, pbxSettings(t, project)["App"], "App")
	})
	t.Run("several apps, none covered", func(t *testing.T) {
		dir := t.TempDir()
		project := write(t, dir, "App.xcodeproj", app, other)
		out, err := run(t, dir, "com.example.elsewhere")
		if err == nil {
			t.Fatalf("accepted: %s", out)
		}
		for _, want := range []string{"com.example.elsewhere", "App in App.xcodeproj (run.mobai.flicker)", "Other in App.xcodeproj (run.mobai.other)"} {
			if !strings.Contains(out, want) {
				t.Errorf("error does not name %q: %s", want, out)
			}
		}
		if data, _ := os.ReadFile(filepath.Join(project, "project.pbxproj")); !strings.HasPrefix(string(data), "// !$*UTF8*$!") {
			t.Error("project rewritten although nothing was signed")
		}
	})
	t.Run("no application target", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "Kit.xcodeproj", kit)
		if out, err := run(t, dir, "run.mobai.flicker"); err == nil || !strings.Contains(out, "No application target in Kit.xcodeproj") {
			t.Fatalf("%s %v", out, err)
		}
	})
	t.Run("no project", func(t *testing.T) {
		if out, err := run(t, t.TempDir(), "run.mobai.flicker"); err == nil || !strings.Contains(out, "No .xcodeproj in") {
			t.Fatalf("%s %v", out, err)
		}
	})
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
	// selection and before the certificate touches a keychain, and read the
	// set the resolve step emits.
	for name, data := range map[string]string{"ios-build.yml": string(workflowTemplate), "runner.sh": string(runner)} {
		data = strings.ReplaceAll(data, "\r\n", "\n") // Windows checkouts
		selected, checked, imported := strings.Index(data, "select_signing_set\n"), strings.Index(data, `check_signing_set "$EXPORT_METHOD"`), strings.Index(data, "security import ")
		if selected < 0 || checked < 0 || imported < 0 {
			t.Errorf("%s: selection %d, check %d, import %d", name, selected, checked, imported)
		} else if selected > checked || checked > imported {
			t.Errorf("%s: the profile check must run after the selection and before security import (offsets %d, %d, %d)", name, selected, checked, imported)
		}
	}
	if !strings.Contains(string(workflowTemplate), `SIGNING_SET: ${{ steps.params.outputs.signing_set }}`) || !strings.Contains(string(runner), `SIGNING_SET=$(signing_set "$DISTRIBUTION")`) {
		t.Error("SIGNING_SET is not derived from the distribution")
	}
	for _, set := range []string{"DEVELOPMENT", "AD_HOC", "STORE", "ENTERPRISE"} {
		for _, secret := range []string{"IOS_CERTIFICATE_", "IOS_CERTIFICATE_PASSWORD_", "IOS_PROVISIONING_PROFILE_", "IOS_EXTENSION_PROFILES_"} {
			if line := secret + set + ": ${{ secrets." + secret + set + " }}"; !strings.Contains(string(workflowTemplate), line) {
				t.Errorf("ios-build.yml does not pass %s%s to the signing step", secret, set)
			}
		}
	}

	if runtime.GOOS == "windows" {
		t.Skip("shell test")
	}
	// runner.sh runs under set -u, so an unset secret must not trip the functions.
	script := "set -euo pipefail\nfail() { echo \"$*\" >&2; exit 1; }\n" + shared +
		"SIGNING_SET=$(signing_set \"$DISTRIBUTION\") || fail \"bad distribution $DISTRIBUTION\"\n" +
		"select_signing_set\ncheck_signing_set \"$METHOD\"\n" +
		"printf '%s|%s|%s|%s|%s' \"$IOS_CERTIFICATE\" \"$IOS_CERTIFICATE_PASSWORD\" \"$IOS_PROVISIONING_PROFILE\" \"$IOS_EXTENSION_PROFILES\" \"$SIGNING_SET_USED\"\n"
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
	store := map[string]string{"IOS_CERTIFICATE_STORE": "store-cert", "IOS_CERTIFICATE_PASSWORD_STORE": "store-pw", "IOS_PROVISIONING_PROFILE_STORE": "store-profile"}
	adHoc := map[string]string{"IOS_CERTIFICATE_AD_HOC": "adhoc-cert", "IOS_CERTIFICATE_PASSWORD_AD_HOC": "adhoc-pw", "IOS_PROVISIONING_PROFILE_AD_HOC": "adhoc-profile"}
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
		// The extension profiles follow the set and are optional: an app
		// without extension targets has no such secret.
		{"suffixed set present", with(legacy, store, map[string]string{"IOS_EXTENSION_PROFILES": "legacy-ext", "IOS_EXTENSION_PROFILES_STORE": "store-ext", "DISTRIBUTION": "store", "METHOD": "app-store"}), "store-cert|store-pw|store-profile|store-ext|STORE", nil},
		{"suffixed set without extension profiles", with(legacy, store, map[string]string{"IOS_EXTENSION_PROFILES": "legacy-ext", "DISTRIBUTION": "store", "METHOD": "app-store"}), "store-cert|store-pw|store-profile||STORE", nil},
		{"internal reads the ad-hoc set", with(adHoc, map[string]string{"DISTRIBUTION": "internal", "METHOD": "ad-hoc"}), "adhoc-cert|adhoc-pw|adhoc-profile||AD_HOC", nil},
		{"only legacy, no distribution, any profile type", with(legacy, map[string]string{"IOS_EXTENSION_PROFILES": "legacy-ext", "DISTRIBUTION": "", "METHOD": "ad-hoc"}), "legacy-cert|legacy-pw|legacy-profile|legacy-ext|legacy", nil},
		{"only legacy without a password", map[string]string{"IOS_CERTIFICATE": "legacy-cert", "IOS_PROVISIONING_PROFILE": "legacy-profile", "DISTRIBUTION": "", "METHOD": "development"}, "legacy-cert||legacy-profile||legacy", nil},
		{"no distribution ignores the suffixed sets", with(store, map[string]string{"IOS_CERTIFICATE_DEVELOPMENT": "dev-cert", "IOS_CERTIFICATE_PASSWORD_DEVELOPMENT": "dev-pw", "IOS_PROVISIONING_PROFILE_DEVELOPMENT": "dev-profile", "DISTRIBUTION": "", "METHOD": "development"}), "", []string{"ios.signing needs IOS_CERTIFICATE", "IOS_*_<SET>"}},
		{"requested set absent, legacy present", with(legacy, map[string]string{"DISTRIBUTION": "ad-hoc", "METHOD": "ad-hoc"}), "", []string{"Signing set AD_HOC for distribution ad-hoc is missing IOS_CERTIFICATE_AD_HOC, IOS_CERTIFICATE_PASSWORD_AD_HOC, IOS_PROVISIONING_PROFILE_AD_HOC", "--distribution ad-hoc"}},
		{"suffixed set missing its profile", with(legacy, map[string]string{"IOS_CERTIFICATE_STORE": "store-cert", "IOS_CERTIFICATE_PASSWORD_STORE": "store-pw", "DISTRIBUTION": "store", "METHOD": "app-store"}), "", []string{"missing IOS_PROVISIONING_PROFILE_STORE.", "--distribution store"}},
		{"suffixed set with empty password", with(store, map[string]string{"IOS_CERTIFICATE_PASSWORD_STORE": "", "DISTRIBUTION": "store", "METHOD": "app-store"}), "", []string{"missing IOS_CERTIFICATE_PASSWORD_STORE."}},
		{"suffixed profile of the wrong type", with(store, map[string]string{"DISTRIBUTION": "store", "METHOD": "ad-hoc"}), "", []string{"IOS_PROVISIONING_PROFILE_STORE holds a ad-hoc", "distribution store", "--distribution store", "distribution to ad-hoc"}},
		{"ad-hoc set holding a store profile, requested as internal", with(adHoc, map[string]string{"DISTRIBUTION": "internal", "METHOD": "app-store"}), "", []string{"IOS_PROVISIONING_PROFILE_AD_HOC holds a app-store", "distribution ad-hoc", "distribution to store"}},
		{"unknown distribution", with(legacy, map[string]string{"DISTRIBUTION": "app-store", "METHOD": "app-store"}), "", []string{"bad distribution app-store"}},
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

// TestExtensionProfilesInstalled holds both templates to one
// install_extension_profiles and runs it on the IOS_EXTENSION_PROFILES secret
// builder signing setup writes: every profile lands next to the app's, and
// the printed map (bundle id to profile name) is what the build reads.
func TestExtensionProfilesInstalled(t *testing.T) {
	workflowTemplate, err := GetWorkflowTemplate()
	if err != nil {
		t.Fatal(err)
	}
	runner, err := GetTemplate("runner.sh")
	if err != nil {
		t.Fatal(err)
	}
	fromWorkflow := shellFunc(t, string(workflowTemplate), "install_extension_profiles")
	fromRunner := shellFunc(t, string(runner), "install_extension_profiles")
	if fromWorkflow != fromRunner {
		t.Fatalf("templates disagree on install_extension_profiles:\n%s\n---\n%s", fromWorkflow, fromRunner)
	}
	// The map reaches the build step, right after the app's profile is installed.
	for name, data := range map[string]string{"ios-build.yml": string(workflowTemplate), "runner.sh": string(runner)} {
		data = strings.ReplaceAll(data, "\r\n", "\n") // Windows checkouts
		if !strings.Contains(data, "EXTENSION_PROFILES=$(install_extension_profiles ") {
			t.Errorf("%s: the extension profiles are not installed", name)
		}
	}
	if !strings.Contains(string(workflowTemplate), `echo "EXTENSION_PROFILES=$EXTENSION_PROFILES" >> $GITHUB_ENV`) || !strings.Contains(string(runner), "export EXTENSION_PROFILES") {
		t.Error("EXTENSION_PROFILES does not reach the build")
	}

	if runtime.GOOS != "darwin" {
		t.Skip("plutil is macOS only")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq unavailable")
	}
	// A stub security prints the file as it is: the fixtures are bare plists,
	// not CMS blobs.
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "security"), []byte("#!/bin/bash\n[ \"$1\" = cms ] || exit 2\ncat \"$4\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	profile := func(name, uuid string) string {
		return `<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict><key>Name</key><string>` + name + `</string><key>UUID</key><string>` + uuid + `</string></dict></plist>`
	}
	secret := signing.EncodeExtensionProfiles(map[string][]byte{
		"run.mobai.flicker.widget": []byte(profile("Builder store run.mobai.flicker.widget", "11111111-2222")),
		"run.mobai.flicker.share":  []byte(profile("Builder store run.mobai.flicker.share", "33333333-4444")),
	})
	home := filepath.Join(dir, "home")
	script := "set -euo pipefail\nfail() { echo \"$*\" >&2; exit 1; }\n" + fromRunner + "\ninstall_extension_profiles \"$1\"\n"
	run := func(secret string) (string, error) {
		cmd := exec.Command("bash", "-c", script, "bash", filepath.Join(dir, "extensions"))
		cmd.Env = []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"), "HOME=" + home, "IOS_EXTENSION_PROFILES=" + secret, "SIGNING_SET=STORE"}
		out, err := cmd.Output()
		return string(out), err
	}
	out, err := run(secret)
	if err != nil {
		t.Fatalf("%s %v", out, err)
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(out), &got); err != nil || got["run.mobai.flicker.widget"] != "Builder store run.mobai.flicker.widget" || got["run.mobai.flicker.share"] != "Builder store run.mobai.flicker.share" || len(got) != 2 {
		t.Errorf("map = %s, %v", out, err)
	}
	for _, uuid := range []string{"11111111-2222", "33333333-4444"} {
		if _, err := os.Stat(filepath.Join(home, "Library", "MobileDevice", "Provisioning Profiles", uuid+".mobileprovision")); err != nil {
			t.Errorf("profile %s not installed: %v", uuid, err)
		}
	}
	// No extensions, or no secret at all, is an empty map; a value that is
	// not an object is refused by name.
	for _, empty := range []string{"{}", ""} {
		if out, err := run(empty); err != nil || out != "{}" {
			t.Errorf("secret %q: %q, %v", empty, out, err)
		}
	}
	cmd := exec.Command("bash", "-c", script, "bash", filepath.Join(dir, "extensions"))
	cmd.Env = []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"), "HOME=" + home, "IOS_EXTENSION_PROFILES=[1]", "SIGNING_SET=STORE"}
	if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "IOS_EXTENSION_PROFILES_STORE must be a JSON object") {
		t.Errorf("bad secret: %s %v", out, err)
	}
}

// TestExportOptionsIncludeExtensions: the export maps the app's real bundle
// id to its profile as before, plus one entry per extension.
func TestExportOptionsIncludeExtensions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell test")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	runner, err := GetTemplate("runner.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := "set -euo pipefail\n" + shellFunc(t, string(runner), "write_export_options") + "\nwrite_export_options \"$1\"\n"
	for _, tc := range []struct {
		name, method, extensions string
		want                     map[string]string
		manage                   bool
	}{
		{"app only", "development", "", map[string]string{"run.mobai.flicker": "Builder development run.mobai.flicker"}, false},
		{"with extensions", "app-store", `{"run.mobai.flicker.widget": "Builder store run.mobai.flicker.widget"}`, map[string]string{"run.mobai.flicker": "Builder development run.mobai.flicker", "run.mobai.flicker.widget": "Builder store run.mobai.flicker.widget"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ExportOptions.plist")
			cmd := exec.Command("bash", "-c", script, "bash", path)
			cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "EXPORT_METHOD=" + tc.method, "DEVELOPMENT_TEAM=ABCDE12345", "APP_BUNDLE_ID=run.mobai.flicker", "PROVISIONING_PROFILE_NAME=Builder development run.mobai.flicker"}
			if tc.extensions != "" {
				cmd.Env = append(cmd.Env, "EXTENSION_PROFILES="+tc.extensions)
			}
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%s %v", out, err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var options struct {
				Method   string            `plist:"method"`
				Style    string            `plist:"signingStyle"`
				Team     string            `plist:"teamID"`
				Profiles map[string]string `plist:"provisioningProfiles"`
				Manage   *bool             `plist:"manageAppVersionAndBuildNumber"`
			}
			if _, err := plist.Unmarshal(data, &options); err != nil {
				t.Fatal(err)
			}
			if options.Method != tc.method || options.Style != "manual" || options.Team != "ABCDE12345" || len(options.Profiles) != len(tc.want) {
				t.Errorf("options = %+v", options)
			}
			for id, name := range tc.want {
				if options.Profiles[id] != name {
					t.Errorf("provisioningProfiles[%s] = %q, want %q", id, options.Profiles[id], name)
				}
			}
			if (options.Manage != nil && !*options.Manage) != tc.manage {
				t.Errorf("manageAppVersionAndBuildNumber = %v, want set to false: %v", options.Manage, tc.manage)
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
	fn := shellFunc(t, string(runner), "apply_build_number") + "\n"
	if got := shellFunc(t, string(workflow), "apply_build_number") + "\n"; got != fn {
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
		{name: "brace setting", input: "1.2.3+42", plist: "CFBundleVersion=${CURRENT_PROJECT_VERSION}\nCFBundleShortVersionString=${MARKETING_VERSION}\n", want: "42|1.2.3|CURRENT_PROJECT_VERSION=42 MARKETING_VERSION=1.2.3", wantPlist: "CFBundleVersion=${CURRENT_PROJECT_VERSION}\nCFBundleShortVersionString=${MARKETING_VERSION}\n"},
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
