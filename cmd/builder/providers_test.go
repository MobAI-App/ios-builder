package main

import (
	"testing"

	"github.com/MobAI-App/ios-builder/internal/config"
	"github.com/spf13/cobra"
)

func providerInitCommand(name string) *cobra.Command {
	cmd := &cobra.Command{}
	cmd.Flags().String("provider", name, "")
	cmd.Flags().String("app-id", name+"-app", "")
	cmd.Flags().String("branch", "ci-main", "")
	cmd.Flags().Bool("set-default", false, "")
	return cmd
}

func TestAddingProvidersPreservesSettingsAndDefault(t *testing.T) {
	chdir(t)
	cfg := &config.Config{Project: "App", Platform: "ios", GitHub: config.GitHubConfig{Owner: "owner", Repo: "repo"}, IOS: config.IOSConfig{Signing: true, Configuration: "Release"}, Flutter: config.FlutterConfig{Version: "3.24.0"}}
	mgr := config.NewManager()
	if err := mgr.Save(cfg); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"codemagic", "bitrise"} {
		if err := runProviderInit(providerInitCommand(name)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := mgr.Load()
	if err != nil {
		t.Fatal(err)
	}
	name, err := got.ProviderName("")
	if err != nil || name != "github" {
		t.Fatal("adding accounts changed default")
	}
	if got.Codemagic.AppID != "codemagic-app" || got.Bitrise.AppID != "bitrise-app" || !got.IOS.Signing || got.IOS.Configuration != "Release" || got.Flutter.Version != "3.24.0" {
		t.Fatalf("lost settings: %+v", got)
	}
	cmd := providerInitCommand("bitrise")
	if err := cmd.Flags().Set("set-default", "true"); err != nil {
		t.Fatal(err)
	}
	if err := runProviderInit(cmd); err != nil {
		t.Fatal(err)
	}
	got, err = mgr.Load()
	if err != nil || got.Provider != "bitrise" {
		t.Fatal("explicit default not saved")
	}
}
