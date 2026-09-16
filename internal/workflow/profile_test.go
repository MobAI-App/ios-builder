package workflow

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// resolveStep returns the shell of the "Resolve parameters" step of a workflow
// template, which is where builder.json profiles are applied on the runner.
func resolveStep(t *testing.T, file string) string {
	t.Helper()
	data, err := GetTemplate(file)
	if err != nil {
		t.Fatal(err)
	}
	var wf struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string `yaml:"name"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &wf); err != nil {
		t.Fatal(err)
	}
	for _, job := range wf.Jobs {
		for _, step := range job.Steps {
			if step.Name == "Resolve parameters" {
				return step.Run
			}
		}
	}
	t.Fatalf("%s has no Resolve parameters step", file)
	return ""
}

type resolved struct {
	outputs map[string]string
	env     map[string]string
	log     string
	err     error
}

// runResolve executes the step the way the runner does: bash, GITHUB_OUTPUT and
// GITHUB_ENV files, builder.json in the working directory.
func runResolve(t *testing.T, script, builderJSON string, env map[string]string) resolved {
	t.Helper()
	dir := t.TempDir()
	if builderJSON != "" {
		if err := os.WriteFile(filepath.Join(dir, "builder.json"), []byte(builderJSON), 0644); err != nil {
			t.Fatal(err)
		}
	}
	scriptPath := filepath.Join(dir, "resolve.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0644); err != nil {
		t.Fatal(err)
	}
	outPath, envPath := filepath.Join(dir, "output"), filepath.Join(dir, "env")
	cmd := exec.Command("bash", "-e", scriptPath)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GITHUB_OUTPUT="+outPath, "GITHUB_ENV="+envPath, "GITHUB_REF_NAME=ios-build/abcdef12")
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	r := resolved{outputs: map[string]string{}, env: map[string]string{}, log: string(out), err: err}
	if data, err := os.ReadFile(outPath); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if k, v, ok := strings.Cut(line, "="); ok {
				r.outputs[k] = v
			}
		}
	}
	if data, err := os.ReadFile(envPath); err == nil {
		lines := strings.Split(string(data), "\n")
		for i := 0; i < len(lines); i++ {
			// GitHub's heredoc form: NAME<<DELIM, value lines, DELIM.
			name, delim, ok := strings.Cut(lines[i], "<<")
			if !ok || delim == "" {
				continue
			}
			var value []string
			for i++; i < len(lines) && lines[i] != delim; i++ {
				value = append(value, lines[i])
			}
			r.env[name] = strings.Join(value, "\n")
		}
	}
	return r
}

const profiledBuilderJSON = `{
  "project": "App", "github": {"owner": "o", "repo": "r"},
  "ios": {"path": "ios", "scheme": "Top", "signing": true, "configuration": "Debug"},
  "defaultProfile": "preview",
  "profiles": {
    "preview": {"configuration": "Release", "signing": false, "distribution": "ad-hoc",
                "env": {"API_URL": "https://staging.example.com", "NOTES": "line one\n__BUILDER_ENV__\nline \"two\""}}
  }
}`

func TestResolveParametersApplyProfiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell test")
	}
	for _, tool := range []string{"bash", "jq", "base64"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s unavailable", tool)
		}
	}
	build := resolveStep(t, "ios-build.yml")
	share := resolveStep(t, "ios-share.yml")

	t.Run("tag build applies defaultProfile", func(t *testing.T) {
		r := runResolve(t, build, profiledBuilderJSON, map[string]string{"GITHUB_EVENT_NAME": "push"})
		if r.err != nil {
			t.Fatalf("%v\n%s", r.err, r.log)
		}
		want := map[string]string{"build_id": "abcdef12", "ios_path": "ios", "scheme": "Top", "use_signing": "false",
			"configuration": "Release", "profile": "preview", "distribution": "ad-hoc", "signing_set": "AD_HOC", "jdk_version": "17"}
		for k, v := range want {
			if r.outputs[k] != v {
				t.Errorf("%s = %q, want %q\n%s", k, r.outputs[k], v, r.log)
			}
		}
		if r.env["API_URL"] != "https://staging.example.com" || r.env["NOTES"] != "line one\n__BUILDER_ENV__\nline \"two\"" {
			t.Fatalf("env not exported verbatim: %q\n%s", r.env, r.log)
		}
	})

	t.Run("tag build without profiles is unchanged", func(t *testing.T) {
		plain := `{"ios": {"scheme": "Top", "signing": true}}`
		r := runResolve(t, build, plain, map[string]string{"GITHUB_EVENT_NAME": "push"})
		if r.err != nil {
			t.Fatalf("%v\n%s", r.err, r.log)
		}
		if r.outputs["scheme"] != "Top" || r.outputs["use_signing"] != "true" || r.outputs["configuration"] != "Debug" || r.outputs["profile"] != "" || r.outputs["signing_set"] != "DEVELOPMENT" || len(r.env) != 0 {
			t.Fatalf("outputs %v env %v\n%s", r.outputs, r.env, r.log)
		}
	})

	t.Run("dispatch uses the profile input", func(t *testing.T) {
		env := map[string]string{"GITHUB_EVENT_NAME": "workflow_dispatch", "IN_BUILD_ID": "12345678", "IN_SCHEME": "Dispatched",
			"IN_USE_SIGNING": "true", "IN_CONFIGURATION": "Release",
			"IN_PROFILE": `{"name":"production","env":{"API_URL":"https://api.example.com"},"distribution":"app-store"}`}
		// builder.json on disk must be ignored for a dispatch.
		r := runResolve(t, build, profiledBuilderJSON, env)
		if r.err != nil {
			t.Fatalf("%v\n%s", r.err, r.log)
		}
		if r.outputs["build_id"] != "12345678" || r.outputs["scheme"] != "Dispatched" || r.outputs["use_signing"] != "true" ||
			r.outputs["profile"] != "production" || r.outputs["distribution"] != "app-store" || r.outputs["signing_set"] != "APP_STORE" || r.env["API_URL"] != "https://api.example.com" {
			t.Fatalf("outputs %v env %v\n%s", r.outputs, r.env, r.log)
		}
		// Without a selected profile the input carries its default.
		env["IN_PROFILE"] = "{}"
		r = runResolve(t, build, "", env)
		if r.err != nil || r.outputs["profile"] != "" || r.outputs["distribution"] != "" || r.outputs["signing_set"] != "DEVELOPMENT" || len(r.env) != 0 {
			t.Fatalf("default profile input: %v %v %v\n%s", r.err, r.outputs, r.env, r.log)
		}
	})

	t.Run("share exports env and profile scheme", func(t *testing.T) {
		withScheme := strings.Replace(profiledBuilderJSON, `"configuration": "Release",`, `"configuration": "Release", "scheme": "Preview",`, 1)
		r := runResolve(t, share, withScheme, map[string]string{"GITHUB_EVENT_NAME": "push"})
		if r.err != nil {
			t.Fatalf("%v\n%s", r.err, r.log)
		}
		if r.outputs["scheme"] != "Preview" || r.outputs["profile"] != "preview" || r.outputs["duration"] != "30m" || r.env["API_URL"] == "" {
			t.Fatalf("outputs %v env %v\n%s", r.outputs, r.env, r.log)
		}
	})

	t.Run("bad profiles fail the job", func(t *testing.T) {
		for name, tt := range map[string]struct {
			json string
			env  map[string]string
		}{
			"unknown defaultProfile": {`{"defaultProfile": "nightly", "profiles": {"preview": {}}}`, map[string]string{"GITHUB_EVENT_NAME": "push"}},
			"bad distribution":       {`{"defaultProfile": "p", "profiles": {"p": {"distribution": "adhoc"}}}`, map[string]string{"GITHUB_EVENT_NAME": "push"}},
			"bad env name":           {``, map[string]string{"GITHUB_EVENT_NAME": "workflow_dispatch", "IN_PROFILE": `{"name":"p","env":{"A B":"x"}}`}},
			"env not an object":      {``, map[string]string{"GITHUB_EVENT_NAME": "workflow_dispatch", "IN_PROFILE": `{"name":"p","env":"A=x"}`}},
			"profile not JSON":       {``, map[string]string{"GITHUB_EVENT_NAME": "workflow_dispatch", "IN_PROFILE": `preview`}},
		} {
			if r := runResolve(t, build, tt.json, tt.env); r.err == nil {
				t.Errorf("%s accepted:\n%s", name, r.log)
			}
		}
	})
}
