package workflow

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const (
	hooksBlockStart = "# >>> build hooks"
	hooksBlockEnd   = "# <<< build hooks\n"
)

// hooksBlock returns the shared run_hook shell block embedded in script.
func hooksBlock(t *testing.T, label, script string) string {
	t.Helper()
	script = strings.ReplaceAll(script, "\r\n", "\n")
	start := strings.Index(script, hooksBlockStart)
	if start < 0 {
		t.Fatalf("%s: no build hooks block", label)
	}
	end := strings.Index(script[start:], hooksBlockEnd)
	if end < 0 {
		t.Fatalf("%s: build hooks block is not terminated", label)
	}
	return script[start : start+end+len(hooksBlockEnd)]
}

// hooksBlocks collects the block from runner.sh and from the Build IPA step of
// ios-build.yml, the only two places that run hooks: the simulator workflow
// and `runner.sh simulator` never do.
func hooksBlocks(t *testing.T) map[string]string {
	t.Helper()
	data, err := GetTemplate("runner.sh")
	if err != nil {
		t.Fatal(err)
	}
	all := steps(t, "ios-build.yml")
	build := all[indexOfStep(t, all, "Build IPA")]
	found := map[string]string{
		"runner.sh":               hooksBlock(t, "runner.sh", string(data)),
		"ios-build.yml Build IPA": hooksBlock(t, "ios-build.yml Build IPA", build.Run),
	}
	for _, step := range all {
		if step.Name != "Build IPA" && strings.Contains(step.Run, hooksBlockStart) {
			t.Errorf("ios-build.yml step %q embeds the hooks block; only Build IPA runs hooks", step.Name)
		}
	}
	share, err := GetTemplate("ios-share.yml")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(share), "run_hook") || strings.Contains(strings.ToLower(string(share)), "hook") {
		t.Error("ios-share.yml mentions hooks; simulator builds never run them")
	}
	return found
}

// TestBuildHooksBlockIdentical keeps the GitHub workflow and runner.sh running
// hooks the same way: same working directory, same variables, same failure.
func TestBuildHooksBlockIdentical(t *testing.T) {
	blocks := hooksBlocks(t)
	want := blocks["runner.sh"]
	for label, got := range blocks {
		if got != want {
			t.Errorf("%s has drifted from runner.sh:\n%s\n--- runner.sh:\n%s", label, got, want)
		}
	}
	if !strings.Contains(want, "run_hook()") || !strings.Contains(want, "bash -eo pipefail -c") {
		t.Fatalf("the shared block is missing run_hook or its shell invocation:\n%s", want)
	}
}

// TestRunHook runs the shared block the way both runners call it: the hook
// command is a multi-line script that records where it ran and what it saw.
func TestRunHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("macOS/Linux shell test")
	}
	block := hooksBlocks(t)["runner.sh"]
	// Both runners define fail; the stub keeps the message and the exit.
	script := "set -eu\nfail() { echo \"FAIL: $*\" >&2; exit 1; }\n" + block + `run_hook "$@"
echo "after $1"
`
	workspace := t.TempDir()
	iosDir := filepath.Join(workspace, "ios")
	if err := os.MkdirAll(iosDir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "hook.sh")
	if err := os.WriteFile(path, []byte(script), 0644); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"GITHUB_WORKSPACE": workspace, "BUILD_ID": "abcdef12", "BUILD_PROFILE": "store", "CONFIGURATION": "Release",
		"DISTRIBUTION": "store", "IOS_PATH": "ios", "PROJECT_TYPE": "flutter", "BUILD_NUMBER": "1.2.3+42", "API_URL": "https://staging.example.com",
	}
	run := func(t *testing.T, cwd string, extra map[string]string, args ...string) (string, error) {
		t.Helper()
		cmd := exec.Command("bash", append([]string{path}, args...)...)
		cmd.Dir = cwd
		cmd.Env = append(os.Environ(), "GITHUB_ACTIONS=", "BUILDER_WORKSPACE=")
		for k, v := range env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		for k, v := range extra {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	record := `set -u
pwd > "$HOOK_LOG"
for v in BUILDER_HOOK BUILDER_BUILD_ID BUILDER_PROFILE BUILDER_CONFIGURATION BUILDER_DISTRIBUTION BUILDER_IOS_PATH BUILDER_PROJECT_TYPE BUILDER_BUILD_NUMBER API_URL; do
  printf '%s=%s\n' "$v" "${!v}" >> "$HOOK_LOG"
done
printf 'BUILDER_IPA=%s\n' "${BUILDER_IPA:-unset}" >> "$HOOK_LOG"
echo "recorded"
`
	read := func(t *testing.T, log string) map[string]string {
		t.Helper()
		data, err := os.ReadFile(log)
		if err != nil {
			t.Fatal("the hook did not run:", err)
		}
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		got := map[string]string{"pwd": lines[0]}
		for _, line := range lines[1:] {
			k, v, _ := strings.Cut(line, "=")
			got[k] = v
		}
		return got
	}

	t.Run("preBuild from the iOS directory", func(t *testing.T) {
		log := filepath.Join(t.TempDir(), "hook.log")
		// The runners are inside ios_path when preBuild runs; the hook is not.
		out, err := run(t, iosDir, map[string]string{"HOOK_LOG": log}, "preBuild", record)
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		got := read(t, log)
		want := map[string]string{"BUILDER_HOOK": "preBuild", "BUILDER_BUILD_ID": "abcdef12", "BUILDER_PROFILE": "store",
			"BUILDER_CONFIGURATION": "Release", "BUILDER_DISTRIBUTION": "store", "BUILDER_IOS_PATH": "ios", "BUILDER_PROJECT_TYPE": "flutter",
			"BUILDER_BUILD_NUMBER": "1.2.3+42", "API_URL": "https://staging.example.com", "BUILDER_IPA": "unset"}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s = %q, want %q", k, got[k], v)
			}
		}
		if real, _ := filepath.EvalSymlinks(workspace); got["pwd"] != workspace && got["pwd"] != real {
			t.Errorf("hook ran in %q, want the workspace %q", got["pwd"], workspace)
		}
		if !strings.Contains(out, "== preBuild hook ==") || !strings.Contains(out, "recorded") || !strings.Contains(out, "after preBuild") || strings.Contains(out, "::group::") {
			t.Errorf("output:\n%s", out)
		}
	})

	t.Run("postBuild gets the IPA and the GitHub group", func(t *testing.T) {
		log := filepath.Join(t.TempDir(), "hook.log")
		ipa := filepath.Join(workspace, "build", "abcdef12.ipa")
		out, err := run(t, workspace, map[string]string{"HOOK_LOG": log, "GITHUB_ACTIONS": "true"}, "postBuild", record, ipa)
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		got := read(t, log)
		if got["BUILDER_HOOK"] != "postBuild" || got["BUILDER_IPA"] != ipa {
			t.Errorf("postBuild variables: %v", got)
		}
		if !strings.Contains(out, "::group::postBuild hook\n") || !strings.Contains(out, "::endgroup::\n") || strings.Contains(out, "== postBuild") {
			t.Errorf("output:\n%s", out)
		}
	})

	t.Run("an empty command is no hook", func(t *testing.T) {
		out, err := run(t, workspace, nil, "preBuild", "")
		if err != nil || strings.Contains(out, "hook") {
			t.Fatalf("%v\n%s", err, out)
		}
	})

	t.Run("a multi-line script stops at the first failure", func(t *testing.T) {
		marker := filepath.Join(t.TempDir(), "reached")
		out, err := run(t, workspace, nil, "preBuild", "echo first\ntrue | true\nexit 3\ntouch '"+marker+"'\n")
		if err == nil {
			t.Fatalf("a failing hook passed:\n%s", out)
		}
		if !strings.Contains(out, "FAIL: preBuild hook failed (exit 3)") || strings.Contains(out, "after preBuild") {
			t.Errorf("output:\n%s", out)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Error("the script went on after the failing line")
		}
		// pipefail: a failing producer fails the pipeline.
		out, err = run(t, workspace, nil, "postBuild", "false | cat\necho unreachable\n", "x.ipa")
		if err == nil || !strings.Contains(out, "postBuild hook failed (exit 1)") || strings.Contains(out, "unreachable") {
			t.Errorf("pipefail: %v\n%s", err, out)
		}
	})
}

// TestBuildHooksPlacement pins where the workflow runs the hooks: preBuild
// after the Pods install and before anything is stamped or archived, postBuild
// after the IPA exists and before the artifact upload.
func TestBuildHooksPlacement(t *testing.T) {
	all := steps(t, "ios-build.yml")
	build := all[indexOfStep(t, all, "Build IPA")]
	run := build.Run
	pre := strings.Index(run, `run_hook preBuild "${BUILDER_HOOK_PRE_BUILD:-}"`)
	post := strings.Index(run, `run_hook postBuild "${BUILDER_HOOK_POST_BUILD:-}" "$GITHUB_WORKSPACE/build/$BUILD_ID.ipa"`)
	pods := strings.Index(run, "pod install")
	stamp := strings.Index(run, "apply_build_number\n")
	if pre < 0 || post < 0 || pods < 0 || stamp < 0 {
		t.Fatalf("hook calls or landmarks missing: pre=%d post=%d pods=%d stamp=%d", pre, post, pods, stamp)
	}
	if pre < pods || stamp < pre {
		t.Errorf("preBuild must run after pod install and before the build number is applied: pods=%d pre=%d stamp=%d", pods, pre, stamp)
	}
	if last := strings.LastIndex(run, "Created "); post < last {
		t.Errorf("postBuild must run after the IPA is created: created=%d post=%d", last, post)
	}
	if _, ok := build.Env["BUILD_PROFILE"]; !ok {
		t.Error("Build IPA lacks BUILD_PROFILE in its env, so BUILDER_PROFILE would be empty")
	}
	if indexOfStep(t, all, "Upload IPA artifact") != indexOfStep(t, all, "Build IPA")+1 {
		t.Error("the artifact upload should directly follow the build step that runs postBuild")
	}
	// runner.sh: preBuild opens build_ipa (not prepare, which the simulator
	// mode shares) and postBuild closes it.
	data, err := GetTemplate("runner.sh")
	if err != nil {
		t.Fatal(err)
	}
	fn := shellFunc(t, string(data), "build_ipa")
	if !strings.Contains(fn, `run_hook preBuild "$(hook_command preBuild)"`) || !strings.Contains(fn, `run_hook postBuild "$(hook_command postBuild)" "$BUILDER_WORKSPACE/build/$BUILD_ID.ipa"`) {
		t.Errorf("build_ipa does not run both hooks:\n%s", fn)
	}
	if strings.Index(fn, "run_hook preBuild") > strings.Index(fn, "install_signing") {
		t.Error("runner.sh preBuild must run before signing is installed")
	}
	for _, name := range []string{"prepare", "build_simulator"} {
		if strings.Contains(shellFunc(t, string(data), name), "run_hook") {
			t.Errorf("%s runs a hook; simulator builds must not", name)
		}
	}
}
