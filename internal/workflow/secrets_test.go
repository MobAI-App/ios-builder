package workflow

import (
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// fakeSecrets stands in for ${{ toJSON(secrets) }}: every secret of the
// repository, listed in builder.json or not.
func fakeSecrets(t *testing.T, secrets map[string]string) string {
	t.Helper()
	data, err := json.Marshal(secrets)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

const secretsBuilderJSON = `{
  "project": "App", "github": {"owner": "o", "repo": "r"},
  "env": {"API_URL": "https://api.example.com", "LOG": "info"},
  "secrets": ["SENTRY_TOKEN"],
  "defaultProfile": "staging",
  "profiles": {
    "staging": {"distribution": "internal", "env": {"API_URL": "https://staging.example.com"}, "secrets": ["MAPS_KEY"]},
    "plain": {}
  }
}`

func requireShellTools(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell test")
	}
	for _, tool := range []string{"bash", "jq", "base64", "tr"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s unavailable", tool)
		}
	}
}

func TestResolveParametersExportListedSecrets(t *testing.T) {
	requireShellTools(t)
	build := resolveStep(t, "ios-build.yml")
	all := map[string]string{
		"SENTRY_TOKEN":          "sentry-top",
		"MAPS_KEY":              "maps-top",
		"MAPS_KEY__STAGING":     "maps-staging\nsecond line",
		"UNLISTED":              "must-not-leak",
		"IOS_CERTIFICATE_STORE": "certificate-must-not-leak",
		"github_token":          "ghs_must-not-leak",
	}

	t.Run("tag build exports exactly the listed secrets", func(t *testing.T) {
		r := runResolve(t, build, secretsBuilderJSON, map[string]string{"GITHUB_EVENT_NAME": "push", "BUILDER_SECRETS_JSON": fakeSecrets(t, all)})
		if r.err != nil {
			t.Fatalf("%v\n%s", r.err, r.log)
		}
		want := map[string]string{
			"API_URL":      "https://staging.example.com", // the profile wins per key
			"LOG":          "info",                        // the top level fills the rest
			"SENTRY_TOKEN": "sentry-top",                  // top-level secret, no profile value
			"MAPS_KEY":     "maps-staging\nsecond line",   // the profile's value wins
		}
		if len(r.env) != len(want) {
			t.Fatalf("exported %v, want exactly %v\n%s", keys(r.env), keys(want), r.log)
		}
		for k, v := range want {
			if r.env[k] != v {
				t.Errorf("%s = %q, want %q", k, r.env[k], v)
			}
		}
		// Every exported secret line is masked; nothing else is.
		for _, line := range []string{"::add-mask::sentry-top", "::add-mask::maps-staging", "::add-mask::second line"} {
			if !strings.Contains(r.log, line+"\n") {
				t.Errorf("missing %q in log:\n%s", line, r.log)
			}
		}
		if strings.Count(r.log, "::add-mask::") != 3 {
			t.Errorf("unexpected masks:\n%s", r.log)
		}
		for _, leak := range []string{"must-not-leak", "maps-top"} {
			if strings.Contains(r.log, leak) && !strings.Contains(r.log, "::add-mask::"+leak) {
				t.Errorf("%q printed:\n%s", leak, r.log)
			}
		}
		if !strings.Contains(r.log, "secret: MAPS_KEY (from MAPS_KEY__STAGING)") || !strings.Contains(r.log, "secret: SENTRY_TOKEN (from SENTRY_TOKEN)") {
			t.Errorf("sources not logged:\n%s", r.log)
		}
	})

	t.Run("dispatch exports the names in the profile input", func(t *testing.T) {
		env := map[string]string{"GITHUB_EVENT_NAME": "workflow_dispatch", "IN_BUILD_ID": "12345678",
			"IN_PROFILE":           `{"name":"","env":{},"distribution":"","secrets":["MAPS_KEY"]}`,
			"BUILDER_SECRETS_JSON": fakeSecrets(t, all)}
		r := runResolve(t, build, secretsBuilderJSON, env)
		if r.err != nil {
			t.Fatalf("%v\n%s", r.err, r.log)
		}
		// No profile: the base name only, never a profile's value.
		if len(r.env) != 1 || r.env["MAPS_KEY"] != "maps-top" || !strings.Contains(r.log, "::add-mask::maps-top") {
			t.Fatalf("env %v\n%s", r.env, r.log)
		}
		// A profile name with other characters maps onto the suffix.
		env["IN_PROFILE"] = `{"name":"pre-view","env":{},"secrets":["MAPS_KEY"]}`
		r = runResolve(t, build, "", map[string]string{"GITHUB_EVENT_NAME": "workflow_dispatch", "IN_PROFILE": env["IN_PROFILE"],
			"BUILDER_SECRETS_JSON": fakeSecrets(t, map[string]string{"MAPS_KEY": "a", "MAPS_KEY__PRE_VIEW": "b"})})
		if r.err != nil || r.env["MAPS_KEY"] != "b" {
			t.Fatalf("suffix lookup: %v %v\n%s", r.err, r.env, r.log)
		}
	})

	t.Run("no secrets listed touches nothing", func(t *testing.T) {
		r := runResolve(t, build, `{"defaultProfile": "plain", "profiles": {"plain": {}}}`, map[string]string{"GITHUB_EVENT_NAME": "push", "BUILDER_SECRETS_JSON": fakeSecrets(t, all)})
		if r.err != nil || len(r.env) != 0 || strings.Contains(r.log, "add-mask") {
			t.Fatalf("%v %v\n%s", r.err, r.env, r.log)
		}
	})

	t.Run("bad or missing secrets fail the job", func(t *testing.T) {
		for name, tt := range map[string]struct {
			profile string
			secrets map[string]string
			want    string
		}{
			"missing":         {`{"name":"staging","secrets":["NOPE"]}`, all, "builder secret set NOPE --profile staging"},
			"lower case":      {`{"name":"","secrets":["nope"]}`, map[string]string{"nope": "x"}, "upper case"},
			"double __":       {`{"name":"","secrets":["A__B"]}`, map[string]string{"A__B": "x"}, "single underscores"},
			"env and secret":  {`{"name":"","env":{"A":"1"},"secrets":["A"]}`, map[string]string{"A": "x"}, "both a plain env value and a secret"},
			"not an array":    {`{"name":"","secrets":"A"}`, map[string]string{"A": "x"}, "JSON array"},
			"no secrets JSON": {`{"name":"","secrets":["A"]}`, nil, "builder secret set A"},
		} {
			env := map[string]string{"GITHUB_EVENT_NAME": "workflow_dispatch", "IN_PROFILE": tt.profile}
			if tt.secrets != nil {
				env["BUILDER_SECRETS_JSON"] = fakeSecrets(t, tt.secrets)
			}
			r := runResolve(t, build, "", env)
			if r.err == nil || !strings.Contains(r.log, tt.want) {
				t.Errorf("%s: err %v, want %q in:\n%s", name, r.err, tt.want, r.log)
			}
			if _, ok := r.env["A"]; ok && name != "env and secret" {
				t.Errorf("%s: exported anyway", name)
			}
		}
	})
}

// The step must see the secrets through the toJSON(secrets) expression; that
// is the only way to read secrets whose names are not in the workflow file.
func TestResolveStepReceivesAllSecrets(t *testing.T) {
	data, err := GetTemplate("ios-build.yml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "BUILDER_SECRETS_JSON: ${{ toJSON(secrets) }}") {
		t.Fatal("Resolve parameters does not receive toJSON(secrets)")
	}
	// Only that one step: every other step would hold every secret too.
	if n := strings.Count(string(data), "${{ toJSON(secrets) }}"); n != 1 {
		t.Fatalf("toJSON(secrets) appears %d times", n)
	}
}

// runner.sh: Codemagic and Bitrise already put the app's secure variables in
// the environment, so the function only picks the profile's value, checks
// presence, and refuses a name that is also plain env.
func TestRunnerExportsListedSecrets(t *testing.T) {
	requireShellTools(t)
	runner, err := GetTemplate("runner.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(runner), "  export_build_env\n  export_build_secrets\n") {
		t.Fatal("prepare() must export the secrets right after the env")
	}
	fn := shellFunc(t, string(runner), "export_build_secrets")
	run := func(env map[string]string) (string, error) {
		script := "set -euo pipefail\nfail() { echo \"$*\" >&2; exit 1; }\nBUILD_ENV=\"${BUILD_ENV:-}\"\n" + fn +
			"\nexport_build_secrets\nfor n in SENTRY_TOKEN MAPS_KEY; do printf '%s=[%s]\\n' \"$n\" \"${!n:-}\"; done\n"
		cmd := exec.Command("bash", "-c", script)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
		for k, v := range env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	out, err := run(map[string]string{"BUILDER_SECRETS": "MAPS_KEY SENTRY_TOKEN", "BUILDER_SECRET_SUFFIX": "STAGING",
		"SENTRY_TOKEN": "top", "MAPS_KEY": "maps-top", "MAPS_KEY__STAGING": "maps\nstaging"})
	if err != nil || !strings.Contains(out, "SENTRY_TOKEN=[top]") || !strings.Contains(out, "MAPS_KEY=[maps\nstaging]") ||
		!strings.Contains(out, "secret: MAPS_KEY (from MAPS_KEY__STAGING)") {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Contains(out, "maps-top") {
		t.Fatalf("the profile value must win:\n%s", out)
	}

	// Nothing listed: nothing checked, nothing exported.
	if out, err := run(map[string]string{"SENTRY_TOKEN": "x"}); err != nil || strings.Contains(out, "secret:") {
		t.Fatalf("%v\n%s", err, out)
	}

	for name, env := range map[string]map[string]string{
		"missing":        {"BUILDER_SECRETS": "SENTRY_TOKEN MAPS_KEY", "SENTRY_TOKEN": "x"},
		"empty":          {"BUILDER_SECRETS": "SENTRY_TOKEN", "SENTRY_TOKEN": ""},
		"lower case":     {"BUILDER_SECRETS": "sentry", "sentry": "x"},
		"injection":      {"BUILDER_SECRETS": "A$(touch${IFS}x)", "A": "x"},
		"bad suffix":     {"BUILDER_SECRETS": "SENTRY_TOKEN", "SENTRY_TOKEN": "x", "BUILDER_SECRET_SUFFIX": "a b"},
		"env and secret": {"BUILDER_SECRETS": "SENTRY_TOKEN", "SENTRY_TOKEN": "x", "BUILD_ENV": `{"SENTRY_TOKEN":"plain"}`},
	} {
		out, err := run(env)
		if err == nil {
			t.Errorf("%s accepted:\n%s", name, out)
		}
		if name == "missing" && !strings.Contains(out, "builder secret set") {
			t.Errorf("missing secret not explained:\n%s", out)
		}
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
