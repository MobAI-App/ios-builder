package main

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/MobAI-App/ios-builder/internal/config"
	"github.com/spf13/cobra"
)

func runnerFlagCommand() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.Flags().String("runner", "", "")
	cmd.Flags().String("stack", "", "")
	return cmd
}

func TestApplyRunnerFlag(t *testing.T) {
	cfg := &config.Config{Runner: config.Runner{"macos-15"}}
	if err := applyRunnerFlag(runnerFlagCommand(), cfg); err != nil || !reflect.DeepEqual(cfg.Runner, config.Runner{"macos-15"}) {
		t.Fatalf("no flag changed the runner: %v %v", cfg.Runner, err)
	}
	cmd := runnerFlagCommand()
	_ = cmd.Flags().Set("runner", "self-hosted,macOS,ARM64")
	if err := applyRunnerFlag(cmd, cfg); err != nil || !reflect.DeepEqual(cfg.Runner, config.Runner{"self-hosted", "macOS", "ARM64"}) {
		t.Fatalf("runner = %v, %v", cfg.Runner, err)
	}
	cmd = runnerFlagCommand()
	_ = cmd.Flags().Set("runner", "macos-latest")
	if err := applyRunnerFlag(cmd, cfg); err != nil || !reflect.DeepEqual(cfg.Runner, config.Runner{"macos-latest"}) {
		t.Fatalf("runner = %v, %v", cfg.Runner, err)
	}
	cmd = runnerFlagCommand()
	_ = cmd.Flags().Set("runner", "self-hosted,,x")
	if err := applyRunnerFlag(cmd, cfg); err == nil {
		t.Fatal("empty label accepted")
	}
	cmd = runnerFlagCommand()
	_ = cmd.Flags().Set("stack", "osx-xcode-16.2.x")
	if err := applyRunnerFlag(cmd, cfg); err == nil {
		t.Fatal("--stack accepted for GitHub")
	}
}

func TestWriteGitHubWorkflowsRendersRunner(t *testing.T) {
	dir := t.TempDir()
	paths, err := writeGitHubWorkflows(dir, config.Runner{"self-hosted", "macOS"})
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 {
		t.Fatalf("paths = %v", paths)
	}
	build, _ := os.ReadFile(paths[0])
	share, _ := os.ReadFile(paths[1])
	if !strings.Contains(string(build), `runs-on: ${{ fromJSON(inputs.profile || '{}').runner || fromJSON('["self-hosted","macOS"]') }}`) {
		t.Error("ios-build.yml runs-on not rendered")
	}
	if !strings.Contains(string(share), `runs-on: ["self-hosted","macOS"]`) {
		t.Error("ios-share.yml runs-on not rendered")
	}
}

func TestProviderInitMachine(t *testing.T) {
	chdir(t)
	mgr := config.NewManager()
	if err := mgr.Save(&config.Config{Project: "App", GitHub: config.GitHubConfig{Owner: "owner", Repo: "repo"}}); err != nil {
		t.Fatal(err)
	}
	cmd := providerInitCommand("codemagic")
	_ = cmd.Flags().Set("runner", "mac_mini_m4")
	if err := runProviderInit(cmd); err != nil {
		t.Fatal(err)
	}
	cmd = providerInitCommand("bitrise")
	_ = cmd.Flags().Set("runner", "g2.mac.large")
	_ = cmd.Flags().Set("stack", "osx-xcode-16.2.x")
	if err := runProviderInit(cmd); err != nil {
		t.Fatal(err)
	}
	got, err := mgr.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Codemagic.InstanceType != "mac_mini_m4" || got.Bitrise.MachineTypeID != "g2.mac.large" || got.Bitrise.Stack != "osx-xcode-16.2.x" {
		t.Fatalf("machines not saved: %+v %+v", got.Codemagic, got.Bitrise)
	}
	cm, _ := os.ReadFile("codemagic.yaml")
	if strings.Count(string(cm), "instance_type: mac_mini_m4") != 2 {
		t.Errorf("codemagic.yaml:\n%s", cm)
	}
	br, _ := os.ReadFile("bitrise.yml")
	if strings.Count(string(br), "machine_type_id: g2.mac.large") != 3 || strings.Count(string(br), "stack: osx-xcode-16.2.x") != 3 {
		t.Errorf("bitrise.yml:\n%s", br)
	}

	// Re-running without the flags keeps what builder.json has.
	if err := runProviderInit(providerInitCommand("codemagic")); err != nil {
		t.Fatal(err)
	}
	cm, _ = os.ReadFile("codemagic.yaml")
	if !strings.Contains(string(cm), "instance_type: mac_mini_m4") {
		t.Error("re-init dropped the instance type")
	}

	for _, tt := range []struct{ provider, runner, stack string }{
		{"github-labels", "self-hosted,macOS", ""},
		{"codemagic", "", "osx-xcode-16.2.x"},
		{"codemagic", "mac mini", ""},
	} {
		provider := tt.provider
		if provider == "github-labels" {
			provider = "codemagic"
		}
		cmd := providerInitCommand(provider)
		_ = cmd.Flags().Set("runner", tt.runner)
		_ = cmd.Flags().Set("stack", tt.stack)
		if err := runProviderInit(cmd); err == nil {
			t.Errorf("%+v accepted", tt)
		}
	}
}
