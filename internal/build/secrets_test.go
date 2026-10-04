package build

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/MobAI-App/ios-builder/internal/config"
)

func TestSecretsReachTheRunners(t *testing.T) {
	cfg := profiledConfig()
	cfg.Secrets = []string{"SENTRY_TOKEN"}
	cfg.Env = map[string]string{"LOG": "debug"}
	p := cfg.Profiles["preview"]
	p.Secrets = []string{"MAPS_KEY"}
	cfg.Profiles["preview"] = p
	c := NewCoordinatorWithOutput(cfg, nil, io.Discard)

	// GitHub: names travel inside the profile input, even with no profile.
	s, _, _ := c.settings("", "", false)
	got := c.buildInputs("abcdef12", "ref", s, "")
	if !strings.Contains(got["profile"], `"secrets":["SENTRY_TOKEN"]`) || !strings.Contains(got["profile"], `"LOG":"debug"`) {
		t.Fatalf("top-level secrets/env not sent: %v", got)
	}
	s, _, _ = c.settings("preview", "", false)
	got = c.buildInputs("abcdef12", "ref", s, "")
	if !strings.Contains(got["profile"], `"secrets":["MAPS_KEY","SENTRY_TOKEN"]`) || len(got) > 10 {
		t.Fatalf("profile secrets not sent: %v", got)
	}

	// Codemagic/Bitrise: the names and the profile's suffix, never a value.
	v := c.inputs("abcdef12", "ref", "sha", s)
	if v["BUILDER_SECRETS"] != "MAPS_KEY SENTRY_TOKEN" || v["BUILDER_SECRET_SUFFIX"] != "PREVIEW" {
		t.Fatalf("runner variables: %v", v)
	}
	s, _, _ = c.settings("", "", false)
	v = c.inputs("abcdef12", "ref", "sha", s)
	if v["BUILDER_SECRETS"] != "SENTRY_TOKEN" || v["BUILDER_SECRET_SUFFIX"] != "" {
		t.Fatalf("no-profile runner variables: %v", v)
	}

	var out bytes.Buffer
	pr := NewProgress(&out)
	pr.Start("abcdef12")
	pr.Settings(s, "github")
	if !strings.Contains(out.String(), "Secrets:       SENTRY_TOKEN") {
		t.Fatalf("secret names not printed:\n%s", out.String())
	}
}

func TestOldWorkflowWithSecretsIsRefused(t *testing.T) {
	t.Chdir(t.TempDir())
	s := &config.BuildSettings{Secrets: []string{"SENTRY_TOKEN"}}
	if err := checkWorkflowExportsSecrets(WorkflowFile, s); err != nil {
		t.Fatalf("no local workflow must not block: %v", err)
	}
	if err := os.MkdirAll(".github/workflows", 0755); err != nil {
		t.Fatal(err)
	}
	write := func(content string) {
		if err := os.WriteFile(".github/workflows/"+WorkflowFile, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("name: iOS Build\n")
	if err := checkWorkflowExportsSecrets(WorkflowFile, s); err == nil || !strings.Contains(err.Error(), "builder init") {
		t.Fatalf("old workflow accepted: %v", err)
	}
	if err := checkWorkflowExportsSecrets(WorkflowFile, &config.BuildSettings{}); err != nil {
		t.Fatalf("a build without secrets must not care: %v", err)
	}
	write("BUILDER_SECRETS_JSON: ${{ toJSON(secrets) }}\n")
	if err := checkWorkflowExportsSecrets(WorkflowFile, s); err != nil {
		t.Fatalf("current workflow refused: %v", err)
	}
}
