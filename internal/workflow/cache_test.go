package workflow

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"text/template"

	"go.yaml.in/yaml/v3"
)

const (
	ccacheBlockStart = "# >>> ccache"
	ccacheBlockEnd   = "# <<< ccache\n"
	// sourcePackages is where every runner clones Swift packages; the GitHub
	// cache steps spell it with ~, the shell with $HOME.
	sourcePackages = ".ios-builder/SourcePackages"
)

type actionStep struct {
	Name string            `yaml:"name"`
	ID   string            `yaml:"id"`
	If   string            `yaml:"if"`
	Uses string            `yaml:"uses"`
	With map[string]string `yaml:"with"`
	Run  string            `yaml:"run"`
}

func actionSteps(t *testing.T, name string) []actionStep {
	t.Helper()
	data, err := GetTemplate(name)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Jobs map[string]struct {
			Steps []actionStep `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	for _, job := range parsed.Jobs {
		return job.Steps
	}
	t.Fatalf("%s: no job", name)
	return nil
}

// cachePaths normalizes a cache step's path input to its set of lines.
func cachePaths(s actionStep) string {
	var lines []string
	for _, line := range strings.Split(s.With["path"], "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	slices.Sort(lines)
	return strings.Join(lines, "\n")
}

// TestGitHubCachesPaired: a cache/restore step restores and nothing else, so
// every one must have a cache/save step for the same paths under a key it can
// restore later, or later builds stay cold. In the share workflow the saves
// must come before the step that blocks for the whole session.
func TestGitHubCachesPaired(t *testing.T) {
	for _, name := range []string{"ios-build.yml", "ios-share.yml"} {
		t.Run(name, func(t *testing.T) {
			all := actionSteps(t, name)
			share := slices.IndexFunc(all, func(s actionStep) bool { return s.Name == "Share the simulator" })
			for i, restore := range all {
				if !strings.HasPrefix(restore.Uses, "actions/cache/restore@") {
					continue
				}
				j := slices.IndexFunc(all, func(s actionStep) bool {
					return strings.HasPrefix(s.Uses, "actions/cache/save@") && cachePaths(s) == cachePaths(restore)
				})
				if j < 0 {
					t.Errorf("%q has no save step for %q", restore.Name, cachePaths(restore))
					continue
				}
				save := all[j]
				if j < i {
					t.Errorf("%q saves before %q restores", save.Name, restore.Name)
				}
				if share >= 0 && j > share {
					t.Errorf("%q runs after the share step, which blocks until the session ends", save.Name)
				}
				primary := "${{ steps." + restore.ID + ".outputs.cache-primary-key }}"
				if save.With["key"] != restore.With["key"] && (restore.ID == "" || save.With["key"] != primary) {
					t.Errorf("%q saves key %q, %q restores %q", save.Name, save.With["key"], restore.Name, restore.With["key"])
				}
				// A per-run key only ever hits through its prefix.
				if strings.Contains(restore.With["key"], "github.run_id") {
					prefix := strings.TrimSpace(restore.With["restore-keys"])
					if prefix == "" || !strings.HasPrefix(restore.With["key"], prefix) {
						t.Errorf("%q: per-run key %q needs its prefix as restore-keys, got %q", restore.Name, restore.With["key"], prefix)
					}
				}
			}
			for _, save := range all {
				if !strings.HasPrefix(save.Uses, "actions/cache/save@") {
					continue
				}
				if !slices.ContainsFunc(all, func(s actionStep) bool {
					return strings.HasPrefix(s.Uses, "actions/cache/restore@") && cachePaths(s) == cachePaths(save)
				}) {
					t.Errorf("%q saves %q, which nothing restores", save.Name, cachePaths(save))
				}
			}
		})
	}
}

// TestGitHubCacheKeys pins what each GitHub workflow caches and what the keys
// are made of, so a cache cannot silently fall out or lose its lockfile.
func TestGitHubCacheKeys(t *testing.T) {
	type want struct{ path, key, prefix string }
	common := []want{
		{"node_modules", "hashFiles('package-lock.json', 'yarn.lock', 'pnpm-lock.yaml', 'bun.lock', 'bun.lockb')", "node-modules-"},
		{"~/.pub-cache", "hashFiles('pubspec.lock')", "pub-"},
		{"${{ steps.params.outputs.ios_path }}/Pods\n~/.cocoapods/repos", "Podfile.lock", "pods-"},
		{"~/" + sourcePackages, "xcshareddata/swiftpm/Package.resolved", "spm-"},
	}
	for name, perRun := range map[string][]want{
		"ios-build.yml": {{"DerivedData", "github.run_id", "deriveddata-device-"}, {"~/.ccache", "github.run_id", "ccache-device-"}},
		"ios-share.yml": {{"DerivedData", "github.run_id", "deriveddata-sim-"}, {"~/.ccache", "github.run_id", "ccache-sim-"}},
	} {
		t.Run(name, func(t *testing.T) {
			all := actionSteps(t, name)
			for _, w := range append(slices.Clone(common), perRun...) {
				i := slices.IndexFunc(all, func(s actionStep) bool {
					return strings.HasPrefix(s.Uses, "actions/cache") && !strings.Contains(s.Uses, "/save@") && cachePaths(s) == w.path
				})
				if i < 0 {
					t.Errorf("nothing restores %q", w.path)
					continue
				}
				key := all[i].With["key"]
				if !strings.HasPrefix(key, w.prefix) || !strings.Contains(key, w.key) {
					t.Errorf("%q key %q, want prefix %q and %q", all[i].Name, key, w.prefix, w.key)
				}
			}
			// The Swift packages key must not hash package checkouts or
			// node_modules copies, or it changes from run to run.
			spm := all[slices.IndexFunc(all, func(s actionStep) bool { return s.ID == "spm-cache" })]
			for _, exclude := range []string{"'!DerivedData/**'", "'!**/node_modules/**'", "'!**/Pods/**'"} {
				if !strings.Contains(spm.With["key"], exclude) || !strings.Contains(spm.If, exclude) {
					t.Errorf("Swift packages key or gate misses %s", exclude)
				}
			}
			// flutter-action's own pub cache would be a second copy under another key.
			flutter := all[slices.IndexFunc(all, func(s actionStep) bool { return s.Name == "Setup Flutter" })]
			if flutter.With["pub-cache"] != "false" {
				t.Errorf("Setup Flutter pub-cache = %q, want false", flutter.With["pub-cache"])
			}
			// An exact node_modules hit skips the install.
			data, _ := GetTemplate(name)
			if !strings.Contains(string(data), "JS_DEPS_CACHED: ${{ steps.node-modules-cache.outputs.cache-hit }}") {
				t.Error("Install JS dependencies does not learn about a node_modules cache hit")
			}
		})
	}
}

// TestSourcePackagesDirUsed: the Swift packages cache is only worth anything if
// every xcodebuild that resolves the package graph clones into the cached
// directory; one without the flag clones everything again into DerivedData.
func TestSourcePackagesDirUsed(t *testing.T) {
	for _, name := range []string{"ios-build.yml", "ios-share.yml", "runner.sh"} {
		t.Run(name, func(t *testing.T) {
			data, err := GetTemplate(name)
			if err != nil {
				t.Fatal(err)
			}
			text := string(data)
			if !strings.Contains(text, `="$HOME/`+sourcePackages+`"`) {
				t.Fatalf("no $HOME/%s assignment", sourcePackages)
			}
			lines := strings.Split(text, "\n")
			uses := 0
			for i, line := range lines {
				if !strings.Contains(line, "-derivedDataPath") {
					continue
				}
				uses++
				next := ""
				if i+1 < len(lines) {
					next = lines[i+1]
				}
				if !strings.Contains(line+next, "-clonedSourcePackagesDirPath") {
					t.Errorf("line %d passes -derivedDataPath without -clonedSourcePackagesDirPath: %s", i+1, strings.TrimSpace(line))
				}
			}
			if uses == 0 {
				t.Fatal("no xcodebuild with -derivedDataPath")
			}
			// apply_build_number's -showBuildSettings resolves packages too.
			for i, line := range lines {
				if strings.Contains(line, "apply_build_number \"") && !strings.Contains(line, "-clonedSourcePackagesDirPath") {
					t.Errorf("line %d: apply_build_number without -clonedSourcePackagesDirPath", i+1)
				}
			}
		})
	}
}

// ccacheBlocks collects the shared ccache shell block from every runner.
func ccacheBlocks(t *testing.T) map[string]string {
	t.Helper()
	extract := func(label, script string) string {
		start := strings.Index(script, ccacheBlockStart)
		if start < 0 {
			t.Fatalf("%s: no ccache block", label)
		}
		end := strings.Index(script[start:], ccacheBlockEnd)
		if end < 0 {
			t.Fatalf("%s: ccache block is not terminated", label)
		}
		return script[start : start+end+len(ccacheBlockEnd)]
	}
	data, err := GetTemplate("runner.sh")
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]string{"runner.sh": extract("runner.sh", strings.ReplaceAll(string(data), "\r\n", "\n"))}
	for _, name := range []string{"ios-build.yml", "ios-share.yml"} {
		all := actionSteps(t, name)
		i := slices.IndexFunc(all, func(s actionStep) bool { return s.Name == "Enable ccache" })
		if i < 0 {
			t.Fatalf("%s: no Enable ccache step", name)
		}
		found[name] = extract(name, all[i].Run)
		// pod install, where React Native's hook reads USE_CCACHE, is in the build step.
		build := slices.IndexFunc(all, func(s actionStep) bool { return strings.HasPrefix(s.Name, "Build ") })
		if build < i {
			t.Fatalf("%s: Enable ccache runs after the build step", name)
		}
		for _, env := range []string{"USE_CCACHE", "CCACHE_DIR", "CCACHE_MAXSIZE"} {
			if !strings.Contains(all[i].Run, `echo "`+env+`=$`+env+`"`) {
				t.Fatalf("%s: Enable ccache does not export %s to later steps", name, env)
			}
		}
	}
	return found
}

func TestCcacheBlockIdentical(t *testing.T) {
	blocks := ccacheBlocks(t)
	want := blocks["runner.sh"]
	for label, got := range blocks {
		if got != want {
			t.Errorf("%s has drifted from runner.sh:\n%s", label, got)
		}
	}
	runner, _ := GetTemplate("runner.sh")
	prepare := shellFunc(t, string(runner), "prepare")
	setup, install := strings.Index(prepare, "then ccache_setup"), strings.Index(prepare, "\n    pod install")
	if setup < 0 || install < 0 || setup > install {
		t.Fatal("runner.sh must set ccache up before pod install")
	}
}

// TestCcacheOptIn runs the shared block: on only for React Native and Expo with
// cache.ccache true, and then USE_CCACHE and a fixed cache directory.
func TestCcacheOptIn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("macOS/Linux shell test")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq unavailable")
	}
	block := ccacheBlocks(t)["runner.sh"]
	bin := t.TempDir()
	// brew "installs" ccache by dropping the stub on PATH.
	ccache := "#!/bin/sh\necho 'ccache version 4.10'\n"
	brew := "#!/bin/sh\necho \"brew $*\" >> \"$CMD_LOG\"\nprintf '%s' '" + ccache + "' > \"$(dirname \"$0\")/ccache\"\nchmod +x \"$(dirname \"$0\")/ccache\"\n"
	if err := os.WriteFile(filepath.Join(bin, "brew"), []byte(brew), 0755); err != nil {
		t.Fatal(err)
	}
	script := "set -eu\n" + block + `if ccache_enabled "$1"; then ccache_setup; echo "on $USE_CCACHE $CCACHE_DIR $CCACHE_MAXSIZE"; else echo off; fi
`
	for _, tt := range []struct {
		name, kind, config, want string
	}{
		{"react native on", "reactnative", `{"cache":{"ccache":true}}`, "on 1 HOME/.ccache 2G"},
		{"expo on", "expo", `{"cache":{"ccache":true}}`, "on 1 HOME/.ccache 2G"},
		{"off by default", "reactnative", `{}`, "off"},
		{"explicitly off", "reactnative", `{"cache":{"ccache":false}}`, "off"},
		{"string is not true", "reactnative", `{"cache":{"ccache":"yes"}}`, "off"},
		{"no builder.json", "reactnative", "", "off"},
		{"broken builder.json", "reactnative", "{", "off"},
		{"native ignores it", "native", `{"cache":{"ccache":true}}`, "off"},
		{"flutter ignores it", "flutter", `{"cache":{"ccache":true}}`, "off"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			work, home := t.TempDir(), t.TempDir()
			os.Remove(filepath.Join(bin, "ccache"))
			if tt.config != "" {
				if err := os.WriteFile(filepath.Join(work, "builder.json"), []byte(tt.config), 0644); err != nil {
					t.Fatal(err)
				}
			}
			log := filepath.Join(work, "cmd.log")
			cmd := exec.Command("bash", "-c", script, "ccache-test", tt.kind)
			cmd.Dir = work
			cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+"/usr/bin:/bin", "HOME="+home, "CMD_LOG="+log)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%s %v", out, err)
			}
			lines := strings.Split(strings.TrimSpace(string(out)), "\n")
			got := strings.ReplaceAll(lines[len(lines)-1], home, "HOME")
			if got != tt.want {
				t.Fatalf("got %q, want %q\n%s", got, tt.want, out)
			}
			calls, _ := os.ReadFile(log)
			if tt.want == "off" && len(calls) != 0 {
				t.Fatalf("installed ccache while off: %s", calls)
			}
			if tt.want != "off" {
				if string(calls) != "brew install ccache\n" {
					t.Fatalf("brew calls = %q", calls)
				}
				if info, err := os.Stat(filepath.Join(home, ".ccache")); err != nil || !info.IsDir() {
					t.Fatal("cache directory not created")
				}
			}
		})
	}
}

type bitriseStep struct {
	Title       string              `yaml:"title"`
	IsAlwaysRun bool                `yaml:"is_always_run"`
	Inputs      []map[string]string `yaml:"inputs"`
	Outputs     []map[string]string `yaml:"outputs"`
}

func (s bitriseStep) input(name string) string {
	for _, in := range s.Inputs {
		if v, ok := in[name]; ok {
			return v
		}
	}
	return ""
}

// family is a cache key up to its first template action: the part every
// fallback prefix and every save of that cache shares.
func family(key string) string {
	key = strings.TrimSpace(strings.SplitN(key, "\n", 2)[0])
	if i := strings.Index(key, "{{"); i >= 0 {
		key = key[:i]
	}
	return key
}

// TestBitriseCaches pins the Bitrise workflows' key-based caches: the same
// caches the other providers keep, restored after the snapshot checkout (the
// keys checksum its lockfiles) and before the build, each paired with a save
// whose key the restore can find again, saved before the share step.
func TestBitriseCaches(t *testing.T) {
	data, err := GetTemplate("bitrise.yml")
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Workflows map[string]struct {
			Steps []map[string]bitriseStep `yaml:"steps"`
		} `yaml:"workflows"`
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	funcs := template.FuncMap{
		"checksum": func(paths ...string) string { return "sum" },
		"getenv":   func(string) string { return "42" },
	}
	evaluate := func(key string) string {
		tmpl, err := template.New("key").Funcs(funcs).Parse(key)
		if err != nil {
			t.Fatalf("key %q: %v", key, err)
		}
		var out strings.Builder
		if err := tmpl.Execute(&out, map[string]string{"OS": "darwin", "Arch": "arm64"}); err != nil {
			t.Fatalf("key %q: %v", key, err)
		}
		if strings.Contains(out.String(), ",") {
			t.Fatalf("key %q has a comma, which Bitrise refuses", key)
		}
		return out.String()
	}
	for name, dest := range map[string]string{"ios-build": "device", "ios-share": "sim"} {
		t.Run(name, func(t *testing.T) {
			workflow, ok := config.Workflows[name]
			if !ok {
				t.Fatal("missing workflow")
			}
			type entry struct {
				id   string
				step bitriseStep
			}
			var all []entry
			for _, step := range workflow.Steps {
				if len(step) != 1 {
					t.Fatalf("step with %d ids", len(step))
				}
				for id, s := range step {
					all = append(all, entry{id, s})
				}
			}
			index := func(title string) int {
				i := slices.IndexFunc(all, func(e entry) bool { return e.step.Title == title })
				if i < 0 {
					t.Fatalf("no step titled %q", title)
				}
				return i
			}
			checkout := index("Check out the snapshot")
			if !strings.Contains(all[checkout].step.input("content"), `runner.sh" checkout`) {
				t.Fatal("the checkout step does not run runner.sh checkout")
			}
			buildTitle, end := "Build IPA from snapshot", len(all)
			if name == "ios-share" {
				buildTitle, end = "Build for the simulator", index("Share the simulator")
			}
			build := index(buildTitle)
			script := all[build].step.input("content")
			if strings.Contains(script, "cp .builder/ci/runner.sh") {
				t.Fatal("the build step copies runner.sh from the snapshot instead of using the one copied before checkout")
			}
			if !strings.Contains(script, `"${NODE_MODULES_CACHE_HIT:-}" = exact`) || !strings.Contains(script, "JS_DEPS_CACHED=true") {
				t.Fatal("the build step does not skip the install on an exact node_modules hit")
			}

			restores, saves := map[string]entry{}, map[string]entry{}
			for i, e := range all {
				switch {
				case strings.HasPrefix(e.id, "restore-cache@"):
					if i < checkout || i > build {
						t.Errorf("%q must run between the snapshot checkout and the build", e.step.Title)
					}
					restores[family(e.step.input("key"))] = e
				case strings.HasPrefix(e.id, "save-cache@"):
					if i < build || i > end {
						t.Errorf("%q must run after the build and before the share step", e.step.Title)
					}
					if e.step.input("paths") == "" {
						t.Errorf("%q has no paths", e.step.Title)
					}
					saves[family(e.step.input("key"))] = e
				}
			}
			want := []string{"deriveddata-" + dest + "-", "ccache-" + dest + "-", "spm-", "pods-", "node-modules-", "pub-", "gradle-"}
			for _, f := range want {
				restore, ok := restores[f]
				if !ok {
					t.Errorf("no restore-cache for %s", f)
					continue
				}
				save, ok := saves[f]
				if !ok {
					t.Errorf("no save-cache for %s", f)
					continue
				}
				keys := strings.Split(strings.TrimSpace(restore.step.input("key")), "\n")
				saved := evaluate(save.step.input("key"))
				for _, k := range keys {
					if !strings.HasPrefix(saved, evaluate(strings.TrimSpace(k))) {
						t.Errorf("%s: saved key %q cannot be found by restore key %q", f, saved, k)
					}
				}
				perBuild := strings.Contains(save.step.input("key"), "BITRISE_BUILD_NUMBER")
				if perBuild != save.step.IsAlwaysRun {
					t.Errorf("%s: is_always_run = %v; only the per-build caches are saved after a failed build", f, save.step.IsAlwaysRun)
				}
				if !perBuild {
					if strings.TrimSpace(keys[0]) != save.step.input("key") {
						t.Errorf("%s: first restore key %q differs from the save key %q", f, keys[0], save.step.input("key"))
					}
					if len(keys) != 2 || strings.TrimSpace(keys[1]) != f+"{{ .OS }}-{{ .Arch }}-" {
						t.Errorf("%s: restore keys %q want the exact key then the %q prefix", f, keys, f)
					}
					if !strings.Contains(save.step.input("key"), "checksum") {
						t.Errorf("%s: lockfile cache key has no checksum", f)
					}
				}
			}
			if len(restores) != len(want) || len(saves) != len(want) {
				t.Errorf("caches = %d restores, %d saves; want %d each", len(restores), len(saves), len(want))
			}
			if got := saves["spm-"].step.input("paths"); got != "~/"+sourcePackages {
				t.Errorf("Swift packages saved from %q", got)
			}
			if !slices.ContainsFunc(restores["node-modules-"].step.Outputs, func(o map[string]string) bool {
				return o["BITRISE_CACHE_HIT"] == "NODE_MODULES_CACHE_HIT"
			}) {
				t.Error("the node_modules restore does not alias BITRISE_CACHE_HIT to NODE_MODULES_CACHE_HIT")
			}
		})
	}
}

// TestCodemagicCachePaths: Codemagic caches by path only, so the paths are the
// whole contract, and the Swift packages one must be where runner.sh clones.
func TestCodemagicCachePaths(t *testing.T) {
	data, err := GetTemplate("codemagic.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Workflows map[string]struct {
			Cache struct {
				Paths []string `yaml:"cache_paths"`
			} `yaml:"cache"`
		} `yaml:"workflows"`
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ios-build", "ios-share"} {
		paths := config.Workflows[name].Cache.Paths
		for _, want := range []string{"$CM_BUILD_DIR/DerivedData", "$HOME/.gradle/caches", "$HOME/.pub-cache", "$HOME/" + sourcePackages, "$HOME/.ccache"} {
			if !slices.Contains(paths, want) {
				t.Errorf("%s does not cache %s: %v", name, want, paths)
			}
		}
	}
}
