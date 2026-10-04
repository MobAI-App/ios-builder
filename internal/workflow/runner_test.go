package workflow

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/MobAI-App/ios-builder/internal/config"
	"go.yaml.in/yaml/v3"
)

type parsedWorkflow struct {
	Jobs map[string]struct {
		RunsOn any `yaml:"runs-on"`
		Steps  []struct {
			Name string `yaml:"name"`
			If   string `yaml:"if"`
			Uses string `yaml:"uses"`
			Run  string `yaml:"run"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

func parseWorkflow(t *testing.T, data []byte) parsedWorkflow {
	t.Helper()
	var w parsedWorkflow
	if err := yaml.Unmarshal(data, &w); err != nil {
		t.Fatalf("workflow does not parse: %v", err)
	}
	return w
}

func TestRenderWorkflowRunsOn(t *testing.T) {
	const prefix = "${{ fromJSON(inputs.profile || '{}').runner || "
	for _, tt := range []struct {
		name   string
		runner config.Runner
		want   string
	}{
		{"default", nil, prefix + "'macos-latest' }}"},
		{"hosted image", config.Runner{"macos-15"}, prefix + "'macos-15' }}"},
		{"self-hosted", config.Runner{"self-hosted"}, prefix + "'self-hosted' }}"},
		{"labels", config.Runner{"self-hosted", "macOS", "ARM64"}, prefix + `fromJSON('["self-hosted","macOS","ARM64"]') }}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data, err := RenderWorkflow(tt.runner)
			if err != nil {
				t.Fatal(err)
			}
			w := parseWorkflow(t, data)
			if got := w.Jobs["build"].RunsOn; got != tt.want {
				t.Fatalf("runs-on = %v, want %s", got, tt.want)
			}
		})
	}
	// The embedded template is the default rendering.
	raw, _ := GetWorkflowTemplate()
	def, _ := RenderWorkflow(nil)
	if !bytes.Equal(raw, def) {
		t.Fatal("RenderWorkflow(nil) differs from the embedded template")
	}
	if _, err := RenderWorkflow(config.Runner{"x' }}"}); err == nil {
		t.Fatal("an unsafe label was rendered")
	}
}

func TestRenderShareWorkflowRunsOn(t *testing.T) {
	for _, tt := range []struct {
		runner config.Runner
		want   any
	}{
		{nil, "macos-latest"},
		{config.Runner{"macos-15"}, "macos-15"},
		{config.Runner{"self-hosted", "macOS"}, []any{"self-hosted", "macOS"}},
	} {
		data, err := RenderShareWorkflow(tt.runner)
		if err != nil {
			t.Fatal(err)
		}
		got := parseWorkflow(t, data).Jobs["simulator"].RunsOn
		if s, ok := tt.want.(string); ok {
			if got != s {
				t.Errorf("%v: runs-on = %v", tt.runner, got)
			}
			continue
		}
		list, ok := got.([]any)
		if !ok || len(list) != 2 || list[0] != "self-hosted" || list[1] != "macOS" {
			t.Errorf("%v: runs-on = %#v", tt.runner, got)
		}
	}
}

// A self-hosted Mac keeps its Xcode selection, has no passwordless sudo for
// setup-xcode, and has tools installed or not: brew runs only for a missing one.
func TestTemplatesSafeOnPersistentRunner(t *testing.T) {
	for _, name := range []string{"ios-build.yml", "ios-share.yml"} {
		data, err := GetTemplate(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, job := range parseWorkflow(t, data).Jobs {
			var check bool
			for _, s := range job.Steps {
				if strings.HasPrefix(s.Uses, "maxim-lobanov/setup-xcode") && s.If != "runner.environment == 'github-hosted'" {
					t.Errorf("%s: setup-xcode runs on self-hosted runners (if: %q)", name, s.If)
				}
				if s.Name == "Check Xcode" && s.If == "runner.environment != 'github-hosted'" && strings.Contains(s.Run, "xcodebuild -version") {
					check = true
				}
			}
			if !check {
				t.Errorf("%s: no Check Xcode step for self-hosted runners", name)
			}
		}
		for i, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, "brew install") && !strings.Contains(line, "command -v") {
				t.Errorf("%s:%d: unconditional brew install: %s", name, i+1, strings.TrimSpace(line))
			}
		}
	}
}

func buildStep(t *testing.T, name string) string {
	t.Helper()
	data, err := GetWorkflowTemplate()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range parseWorkflow(t, data).Jobs["build"].Steps {
		if s.Name == name {
			return s.Run
		}
	}
	t.Fatalf("no step %q", name)
	return ""
}

func TestPreviousOutputsCleared(t *testing.T) {
	run := buildStep(t, "Clear previous outputs")
	for _, path := range []string{"build/App.xcarchive", "build/export", "build/*.ipa", "Payload"} {
		if !strings.Contains(run, path) {
			t.Errorf("Clear previous outputs leaves %s", path)
		}
	}
	signing := buildStep(t, "Install certificate and provisioning profile")
	for _, want := range []string{`"$RUNNER_TEMP/keychains-before"`, `echo "$PROFILE_UUID" >> "$RUNNER_TEMP/installed-profiles"`} {
		if !strings.Contains(signing, want) {
			t.Errorf("signing step does not record %s", want)
		}
	}
	if strings.Contains(signing, `list-keychain -d user -s "$KEYCHAIN_PATH"`+"\n") {
		t.Error("signing step replaces the keychain search list")
	}
}

// Runs the cleanup step against a fake `security` and checks that it leaves
// the machine as the job found it: search list restored, keychain deleted,
// every installed profile removed and nothing else touched.
func TestCleanupSigningRestoresMachine(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("macOS/Linux shell test")
	}
	run := buildStep(t, "Cleanup signing")
	tmp := t.TempDir()
	home, runnerTemp, bin := filepath.Join(tmp, "home"), filepath.Join(tmp, "runner"), filepath.Join(tmp, "bin")
	profiles := filepath.Join(home, "Library", "MobileDevice", "Provisioning Profiles")
	for _, d := range []string{profiles, filepath.Join(runnerTemp, "extensions"), bin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	log := filepath.Join(tmp, "security.log")
	write(filepath.Join(bin, "security"), "#!/bin/bash\nprintf '%s|' \"$@\" >> "+log+"\necho >> "+log+"\n")
	keychain := filepath.Join(runnerTemp, "app-signing.keychain-db")
	login := "/Users/runner/Library/Keychains/login.keychain-db"
	write(keychain, "")
	write(filepath.Join(runnerTemp, "keychains-before"), login+"\n/Library/Keychains/System.keychain\n")
	write(filepath.Join(runnerTemp, "installed-profiles"), "APP-UUID\n")
	write(filepath.Join(runnerTemp, "extensions", "installed"), "EXT-UUID\n")
	for _, uuid := range []string{"APP-UUID", "EXT-UUID", "OWNER-UUID"} {
		write(filepath.Join(profiles, uuid+".mobileprovision"), "")
	}

	cmd := exec.Command("bash", "-e", "-c", run)
	cmd.Env = append(os.Environ(), "HOME="+home, "RUNNER_TEMP="+runnerTemp, "PATH="+bin+":"+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cleanup failed: %v\n%s", err, out)
	}
	calls, _ := os.ReadFile(log)
	want := "list-keychains|-d|user|-s|" + login + "|/Library/Keychains/System.keychain|\ndelete-keychain|" + keychain + "|\n"
	if string(calls) != want {
		t.Fatalf("security calls:\n%s\nwant:\n%s", calls, want)
	}
	for uuid, kept := range map[string]bool{"APP-UUID": false, "EXT-UUID": false, "OWNER-UUID": true} {
		_, err := os.Stat(filepath.Join(profiles, uuid+".mobileprovision"))
		if (err == nil) != kept {
			t.Errorf("%s: kept=%v, want %v", uuid, err == nil, kept)
		}
	}
	if _, err := os.Stat(filepath.Join(runnerTemp, "extensions")); !os.IsNotExist(err) {
		t.Error("extension profiles left in RUNNER_TEMP")
	}

	// Signing never started: nothing to restore, nothing fails.
	empty := t.TempDir()
	cmd = exec.Command("bash", "-e", "-c", run)
	cmd.Env = append(os.Environ(), "HOME="+home, "RUNNER_TEMP="+empty, "PATH="+bin+":"+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cleanup with nothing installed failed: %v\n%s", err, out)
	}
}

// The signing step prepends its keychain to the search list as `security
// list-keychains` prints it (indented, quoted) instead of replacing the list.
func TestSigningKeepsKeychainSearchList(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("macOS/Linux shell test")
	}
	signing := buildStep(t, "Install certificate and provisioning profile")
	start := strings.Index(signing, "security list-keychains -d user | sed")
	end := strings.Index(signing, `security list-keychains -d user -s "$KEYCHAIN_PATH" "${KEYCHAINS[@]}"`)
	if start < 0 || end < start {
		t.Fatal("keychain search list snippet not found")
	}
	snippet := signing[start:end] + `security list-keychains -d user -s "$KEYCHAIN_PATH" "${KEYCHAINS[@]}"`
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(tmp, "security.log")
	fake := `#!/bin/bash
if [ "$4" = "-s" ]; then printf '%s|' "$@" > ` + log + `; exit 0; fi
printf '    "%s"\n' "/Users/me/Library/Keychains/login.keychain-db" "/Users/me/Library/Keychains/My Team.keychain-db"
`
	if err := os.WriteFile(filepath.Join(bin, "security"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-e", "-c", snippet)
	cmd.Env = append(os.Environ(), "RUNNER_TEMP="+tmp, "KEYCHAIN_PATH=/tmp/app-signing.keychain-db", "PATH="+bin+":"+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	got, _ := os.ReadFile(log)
	want := "-d|user|-s|/tmp/app-signing.keychain-db|/Users/me/Library/Keychains/login.keychain-db|/Users/me/Library/Keychains/My Team.keychain-db|"
	if !strings.HasPrefix(string(got), "list-keychains|") || strings.TrimPrefix(string(got), "list-keychains|") != want {
		t.Fatalf("search list set to %q", got)
	}
}

func TestProviderMachines(t *testing.T) {
	files, err := ProviderFiles("codemagic", &config.CIConfig{InstanceType: "mac_mini_m4"})
	if err != nil {
		t.Fatal(err)
	}
	var cm struct {
		Workflows map[string]struct {
			InstanceType string `yaml:"instance_type"`
		}
	}
	if err := yaml.Unmarshal(files["codemagic.yaml"], &cm); err != nil {
		t.Fatal(err)
	}
	for name, w := range cm.Workflows {
		if w.InstanceType != "mac_mini_m4" {
			t.Errorf("codemagic %s instance_type = %q", name, w.InstanceType)
		}
	}

	files, err = ProviderFiles("bitrise", &config.CIConfig{MachineTypeID: "g2.mac.large", Stack: "osx-xcode-16.2.x"})
	if err != nil {
		t.Fatal(err)
	}
	type meta struct {
		Bitrise struct {
			Machine string `yaml:"machine_type_id"`
			Stack   string `yaml:"stack"`
		} `yaml:"bitrise.io"`
	}
	var br struct {
		Meta      meta
		Workflows map[string]struct{ Meta meta }
	}
	if err := yaml.Unmarshal(files["bitrise.yml"], &br); err != nil {
		t.Fatal(err)
	}
	metas := []meta{br.Meta}
	for _, w := range br.Workflows {
		metas = append(metas, w.Meta)
	}
	for _, m := range metas {
		if m.Bitrise.Machine != "g2.mac.large" || m.Bitrise.Stack != "osx-xcode-16.2.x" {
			t.Errorf("bitrise meta = %+v", m.Bitrise)
		}
	}

	// Defaults leave the templates as embedded.
	for provider, name := range map[string]string{"codemagic": "codemagic.yaml", "bitrise": "bitrise.yml"} {
		files, err := ProviderFiles(provider, &config.CIConfig{})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := GetTemplate(name)
		if !bytes.Equal(files[name], raw) {
			t.Errorf("%s: default rendering differs from the template", name)
		}
	}
	if _, err := ProviderFiles("bitrise", &config.CIConfig{Stack: "x\nworkflows: {}"}); err == nil {
		t.Fatal("an unsafe stack was rendered")
	}
}
