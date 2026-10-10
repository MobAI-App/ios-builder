package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/MobAI-App/ios-builder/internal/build"
	"github.com/MobAI-App/ios-builder/internal/config"
	"github.com/MobAI-App/ios-builder/internal/github"
	"github.com/MobAI-App/ios-builder/internal/workflow"
	"github.com/spf13/cobra"
)

func clientForProvider(cfg *config.Config, override string) (*github.Client, error) {
	name, err := cfg.ProviderName(override)
	if err != nil {
		return nil, err
	}
	if name == "github" {
		return getGitHubClient()
	}
	return nil, nil
}

func runProviderInit(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("provider")
	if _, err := (&config.Config{}).ProviderName(name); err != nil {
		return err
	}
	appID, _ := cmd.Flags().GetString("app-id")
	branch, _ := cmd.Flags().GetString("branch")
	if appID == "" || branch == "" {
		return fmt.Errorf("--app-id and --branch are required for %s setup; create and connect the app first: https://github.com/MobAI-App/ios-builder/blob/main/docs/provider-setup.md", name)
	}
	mgr := config.NewManager()
	cfg, err := mgr.Load()
	if err != nil && !errors.Is(err, config.ErrConfigNotFound) {
		return err
	}
	if cfg == nil {
		remote, _ := cmd.Flags().GetString("remote")
		owner, repo, err := detectGitHubRepo(remote)
		if err != nil {
			return err
		}
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		project, _ := cmd.Flags().GetString("project")
		if project == "" {
			project = filepath.Base(cwd)
		}
		iosPath, _ := cmd.Flags().GetString("ios-path")
		if iosPath == "" {
			iosPath, _ = detectIOSPath()
		}
		scheme, _ := cmd.Flags().GetString("scheme")
		cfg = &config.Config{Provider: "github", Project: project, Platform: "ios", GitHub: config.GitHubConfig{Owner: owner, Repo: repo}, IOS: config.IOSConfig{Path: iosPath, Scheme: scheme}}
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	ciCfg := cfg.Codemagic
	if name == "bitrise" {
		ciCfg = cfg.Bitrise
	}
	ciCfg.AppID, ciCfg.Branch = appID, branch
	if err := applyProviderMachine(cmd, name, &ciCfg); err != nil {
		return err
	}
	if ciCfg.BuildWorkflow == "" {
		ciCfg.BuildWorkflow = "ios-build"
	}
	if ciCfg.ShareWorkflow == "" {
		ciCfg.ShareWorkflow = "ios-share"
	}
	if name == "codemagic" {
		cfg.Codemagic = ciCfg
	} else {
		cfg.Bitrise = ciCfg
	}
	if _, err := cfg.ProviderConfig(name); err != nil {
		return err
	}
	setDefault, _ := cmd.Flags().GetBool("set-default")
	if setDefault {
		cfg.Provider = name
	}
	var paths []string
	customWorkflows := ciCfg.BuildWorkflow != "ios-build" || ciCfg.ShareWorkflow != "ios-share"
	if !customWorkflows {
		paths, err = workflow.WriteProviderFiles(".", name, &ciCfg)
		if err != nil {
			return err
		}
	}
	if err := mgr.Save(cfg); err != nil {
		return err
	}
	if customWorkflows {
		fmt.Println("Preserved custom workflow names and CI files. Merge any runner/workflow updates manually (see docs/providers.md).")
	}
	for _, p := range paths {
		fmt.Printf("Created/updated: %s\n", p)
	}
	defaultProvider, _ := cfg.ProviderName("")
	fmt.Printf("Configured %s. Default provider: %s.\n", name, defaultProvider)
	fmt.Printf("Commit the generated files to %s, connect this repository in %s, and run builder auth %s.\n", branch, name, name)
	if name == "codemagic" {
		fmt.Println("Create an environment group named builder (add BUILDER=1 for unsigned builds); put signing/MobAI secrets there.")
	}
	if name == "bitrise" && ciCfg.Stack == "" {
		fmt.Println("Use bitrise.yml from the repository and select a macOS Xcode stack in the app settings (or pass --stack). Put signing/MobAI secrets in the app Secrets tab.")
	} else if name == "bitrise" {
		fmt.Println("Use bitrise.yml from the repository. Put signing/MobAI secrets in the app Secrets tab.")
	}
	fmt.Println("Then: builder ios build --provider " + name)
	fmt.Println("App creation, repository connection, and token guide: https://github.com/MobAI-App/ios-builder/blob/main/docs/provider-setup.md")
	return nil
}

// applyProviderMachine stores --runner as the Codemagic instance_type or the
// Bitrise machine_type_id, and --stack as the Bitrise stack. Values Builder
// does not know are kept with a warning; flags left out keep builder.json.
func applyProviderMachine(cmd *cobra.Command, provider string, ci *config.CIConfig) error {
	runner, _ := cmd.Flags().GetString("runner")
	stack, _ := cmd.Flags().GetString("stack")
	if strings.Contains(runner, ",") {
		return fmt.Errorf("--runner for %s is one machine type, not a list of labels", provider)
	}
	if stack != "" && provider != "bitrise" {
		return fmt.Errorf("--stack applies to Bitrise only; Codemagic selects Xcode with environment.xcode")
	}
	field := "instance_type"
	if provider == "bitrise" {
		field = "machine_type_id"
	}
	for _, f := range []struct{ field, value string }{{field, runner}, {"stack", stack}} {
		warning, err := config.CheckMachine(provider, f.field, f.value)
		if err != nil {
			return err
		}
		if warning != "" {
			fmt.Println("Warning:", warning)
		}
	}
	if runner != "" {
		if provider == "bitrise" {
			ci.MachineTypeID = runner
		} else {
			ci.InstanceType = runner
		}
	}
	if stack != "" {
		ci.Stack = stack
	}
	return nil
}

func init() {
	cancelCmd := &cobra.Command{Use: "cancel", Short: "Cancel a submitted Codemagic or Bitrise run", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		name, _ := cmd.Flags().GetString("provider")
		id, _ := cmd.Flags().GetString("run-id")
		if id == "" {
			return fmt.Errorf("--run-id is required")
		}
		p, _, err := build.RemoteProvider(cfg, name)
		if err != nil {
			return err
		}
		if err := build.CancelRemote(cmd.Context(), p, id); err != nil {
			return fmt.Errorf("cancellation could not be confirmed; check the provider dashboard: %w", err)
		}
		fmt.Println("Run has stopped.")
		return nil
	}}
	cancelCmd.Flags().String("provider", "", "Provider holding the run (codemagic or bitrise)")
	cancelCmd.Flags().String("run-id", "", "Run ID from the provider workflow URL")
	iosCmd.AddCommand(cancelCmd)
}
