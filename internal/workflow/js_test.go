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
	jsBlockStart = "# >>> js toolchain"
	jsBlockEnd   = "# <<< js toolchain\n"
)

// jsBlock returns the shared package-manager shell block embedded in script.
func jsBlock(t *testing.T, label, script string) string {
	t.Helper()
	script = strings.ReplaceAll(script, "\r\n", "\n")
	start := strings.Index(script, jsBlockStart)
	if start < 0 {
		t.Fatalf("%s: no js toolchain block", label)
	}
	end := strings.Index(script[start:], jsBlockEnd)
	if end < 0 {
		t.Fatalf("%s: js toolchain block is not terminated", label)
	}
	return script[start : start+end+len(jsBlockEnd)]
}

// jsBlocks collects every copy of the block across the three runner templates.
func jsBlocks(t *testing.T) map[string]string {
	t.Helper()
	data, err := GetTemplate("runner.sh")
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]string{"runner.sh": jsBlock(t, "runner.sh", string(data))}
	for _, template := range []string{"ios-build.yml", "ios-share.yml"} {
		copies := 0
		for _, step := range steps(t, template) {
			if !strings.Contains(step.Run, jsBlockStart) {
				continue
			}
			copies++
			label := template + " step " + step.Name
			found[label] = jsBlock(t, label, step.Run)
		}
		// The block is needed twice: once to resolve the manager before
		// setup-node, once to install with it afterwards.
		if copies != 2 {
			t.Fatalf("%s embeds the js toolchain block %d times, want 2", template, copies)
		}
	}
	return found
}

// TestJSToolchainBlockIdentical keeps the three runners installing dependencies
// the same way: a project that builds on GitHub must build on Codemagic and
// Bitrise, and the share workflow must match the build workflow.
func TestJSToolchainBlockIdentical(t *testing.T) {
	blocks := jsBlocks(t)
	want := blocks["runner.sh"]
	for label, got := range blocks {
		if got != want {
			t.Errorf("%s has drifted from runner.sh:\n%s", label, got)
		}
	}
	for _, needle := range []string{"js_detect_manager()", "js_node_version()", "js_install()"} {
		if !strings.Contains(want, needle) {
			t.Fatalf("the shared block is missing %s", needle)
		}
	}
}

// jsInstallScript is the shared block followed by one call, ready to run.
func jsInstallScript(t *testing.T) string {
	t.Helper()
	return "set -eu\n" + jsBlocks(t)["runner.sh"] + "js_install\n"
}

// TestJSInstall runs the shared block against stubbed package managers. Every
// case asserts the exact commands: `npm install` in a pnpm or yarn workspace is
// what fails with EUNSUPPORTEDPROTOCOL Unsupported URL Type "workspace:".
func TestJSInstall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("macOS/Linux shell test")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq unavailable")
	}
	script := jsInstallScript(t)

	// Every stubbed manager records its invocation instead of running.
	const stub = `#!/bin/bash
printf '%s %s\n' "$(basename "$0")" "$*" >> "$CMD_LOG"
`
	for _, tt := range []struct {
		name        string
		files       map[string]string
		cached      bool
		wantManager string
		wantCmds    []string
	}{{
		name:        "packageManager pnpm",
		files:       map[string]string{"package.json": `{"packageManager":"pnpm@12.1.0"}`, "pnpm-lock.yaml": "lockfileVersion: '9.0'\n"},
		wantManager: "pnpm",
		wantCmds:    []string{"corepack enable", "pnpm install --frozen-lockfile"},
	}, {
		name:        "packageManager pnpm without a lockfile",
		files:       map[string]string{"package.json": `{"packageManager":"pnpm@12.1.0"}`},
		wantManager: "pnpm",
		wantCmds:    []string{"corepack enable", "pnpm install"},
	}, {
		name:        "yarn classic lockfile",
		files:       map[string]string{"package.json": `{}`, "yarn.lock": "# yarn lockfile v1\n"},
		wantManager: "yarn",
		wantCmds:    []string{"corepack enable", "yarn install --frozen-lockfile"},
	}, {
		name:        "yarn berry",
		files:       map[string]string{"package.json": `{"packageManager":"yarn@4.1.0"}`, "yarn.lock": "", ".yarnrc.yml": "nodeLinker: node-modules\n"},
		wantManager: "yarn",
		wantCmds:    []string{"corepack enable", "yarn install --immutable"},
	}, {
		name:        "bun lockfile",
		files:       map[string]string{"package.json": `{}`, "bun.lockb": ""},
		wantManager: "bun",
		wantCmds:    []string{"bun install --frozen-lockfile"},
	}, {
		name:        "package-lock.json",
		files:       map[string]string{"package.json": `{}`, "package-lock.json": `{}`},
		wantManager: "npm",
		wantCmds:    []string{"npm ci"},
	}, {
		name:        "no lockfile at all",
		files:       map[string]string{"package.json": `{}`},
		wantManager: "npm",
		wantCmds:    []string{"npm install"},
	}, {
		// The manager still has to land on PATH for `expo prebuild`.
		name:        "restored from cache",
		files:       map[string]string{"package.json": `{"packageManager":"pnpm@12.1.0"}`, "pnpm-lock.yaml": ""},
		cached:      true,
		wantManager: "pnpm",
		wantCmds:    []string{"corepack enable"},
	}} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "bin")
			if err := os.MkdirAll(bin, 0755); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"corepack", "npm", "pnpm", "yarn", "bun"} {
				if err := os.WriteFile(filepath.Join(bin, name), []byte(stub), 0755); err != nil {
					t.Fatal(err)
				}
			}
			for name, content := range tt.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(dir, "install.sh")
			if err := os.WriteFile(path, []byte(script), 0644); err != nil {
				t.Fatal(err)
			}
			log := filepath.Join(dir, "cmd.log")
			cmd := exec.Command("bash", path)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(),
				"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"CMD_LOG="+log, "JS_MANAGER=", "JS_DEPS_CACHED=")
			if tt.cached {
				cmd.Env = append(cmd.Env, "JS_DEPS_CACHED=true")
			}
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("js_install: %s %v", out, err)
			}
			if want := "Package manager: " + tt.wantManager; !strings.Contains(string(out), want) {
				t.Fatalf("output %q does not report %q", out, want)
			}
			recorded, err := os.ReadFile(log)
			if err != nil && len(tt.wantCmds) > 0 {
				t.Fatal("no commands ran:", err)
			}
			got := strings.Split(strings.TrimSpace(string(recorded)), "\n")
			if len(got) == 1 && got[0] == "" {
				got = nil
			}
			if strings.Join(got, "|") != strings.Join(tt.wantCmds, "|") {
				t.Fatalf("commands = %q, want %q", got, tt.wantCmds)
			}
		})
	}
}

// TestResolveJSToolchainStep checks the step that feeds actions/setup-node,
// which cannot choose between node-version and node-version-file itself.
func TestResolveJSToolchainStep(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("macOS/Linux shell test")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq unavailable")
	}
	for _, template := range []string{"ios-build.yml", "ios-share.yml"} {
		all := steps(t, template)
		script := all[indexOfStep(t, all, "Resolve JS toolchain")].Run
		for _, tt := range []struct {
			name  string
			files map[string]string
			want  map[string]string
		}{{
			name:  ".nvmrc wins",
			files: map[string]string{"package.json": `{"engines":{"node":"18"}}`, ".nvmrc": "20.11.1\n"},
			want:  map[string]string{"node_version": "", "node_version_file": ".nvmrc"},
		}, {
			name:  ".node-version",
			files: map[string]string{"package.json": `{}`, ".node-version": "21.6.0\n"},
			want:  map[string]string{"node_version": "", "node_version_file": ".node-version"},
		}, {
			name:  "engines 24.x",
			files: map[string]string{"package.json": `{"engines":{"node":"24.x"},"packageManager":"pnpm@12.1.0"}`},
			want:  map[string]string{"node_version": "24.x", "node_version_file": "", "manager": "pnpm", "exec": "pnpm exec"},
		}, {
			name:  "engines caret range",
			files: map[string]string{"package.json": `{"engines":{"node":"^20.11"}}`},
			want:  map[string]string{"node_version": "20.11", "node_version_file": ""},
		}, {
			name:  "engines comparator range",
			files: map[string]string{"package.json": `{"engines":{"node":">=18.17.0 <21"}}`},
			want:  map[string]string{"node_version": "18.17.0", "node_version_file": ""},
		}, {
			name:  "nothing to go on",
			files: map[string]string{"package.json": `{}`},
			want:  map[string]string{"node_version": "22", "node_version_file": "", "manager": "npm", "exec": "npx"},
		}} {
			t.Run(template+"/"+tt.name, func(t *testing.T) {
				dir := t.TempDir()
				for name, content := range tt.files {
					if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
						t.Fatal(err)
					}
				}
				path := filepath.Join(dir, "resolve.sh")
				if err := os.WriteFile(path, []byte(script), 0644); err != nil {
					t.Fatal(err)
				}
				output := filepath.Join(dir, "github-output")
				cmd := exec.Command("bash", path)
				cmd.Dir = dir
				cmd.Env = append(os.Environ(), "GITHUB_OUTPUT="+output, "JS_MANAGER=")
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("resolve: %s %v", out, err)
				}
				data, err := os.ReadFile(output)
				if err != nil {
					t.Fatal(err)
				}
				got := map[string]string{}
				for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
					key, value, _ := strings.Cut(line, "=")
					got[key] = value
				}
				for key, want := range tt.want {
					if got[key] != want {
						t.Errorf("%s = %q, want %q (all: %v)", key, got[key], want, got)
					}
				}
			})
		}
	}
}
