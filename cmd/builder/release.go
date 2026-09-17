package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/MobAI-App/ios-builder/internal/build"
	"github.com/MobAI-App/ios-builder/internal/config"
	"github.com/MobAI-App/ios-builder/internal/release"
	"github.com/spf13/cobra"
)

var iosReleaseCmd = &cobra.Command{
	Use:   "release",
	Short: "Build, upload to App Store Connect and submit in one command",
	Long: `Builds the working tree with the next build number, uploads the IPA to App
Store Connect, waits for processing and hands the build on:

  --testflight   (default) adds it to the named TestFlight groups (--group,
                 repeatable) with --notes as "What to Test"
  --app-store    attaches it to the App Store version (--version, or the one
                 in the project) and submits it for review (--release
                 manual|after-approval)

The build number is one above the highest CFBundleVersion App Store Connect
holds for the app, so every release uploads; --build-number overrides it and
--version sets the marketing version. The app is identified by --bundle-id,
ios.bundleId in builder.json, or the newest IPA in the output directory.

Needs ios.signing true and ios.configuration "Release" in builder.json, and an
App Store Connect API key (builder auth apple).`,
	Args: cobra.NoArgs,
	RunE: runIOSRelease,
}

func init() {
	f := iosReleaseCmd.Flags()
	f.Bool("testflight", false, "Distribute to TestFlight (default)")
	f.Bool("app-store", false, "Submit an App Store version for review")
	f.StringArray("group", nil, "TestFlight group name to add the build to (repeatable)")
	f.String("notes", "", "What to Test notes for the build")
	f.String("release", "", "App Store release: manual or after-approval")
	f.String("version", "", "Marketing version to build and submit (default: the project's)")
	f.String("build-number", "", "CFBundleVersion to build (default: one above the highest in App Store Connect)")
	f.String("bundle-id", "", "App bundle ID (default: ios.bundleId in builder.json, then the newest IPA in the output directory)")
	f.Bool("no-encryption", false, "Declare the app uses no non-exempt encryption (export compliance)")
	f.StringP("output", "o", "dist", "Output directory for the IPA")
	f.StringP("remote", "r", "origin", "Git remote to push the working-tree snapshot to")
	f.String("provider", "", "Override CI provider (default github or builder.json provider)")
	f.Duration("timeout", 30*time.Minute, "Time limit for the build, and again for App Store Connect processing")
	f.Bool("json", false, "Print the result as JSON (progress goes to stderr)")
	iosCmd.AddCommand(iosReleaseCmd)
}

func runIOSRelease(cmd *cobra.Command, _ []string) error {
	testflight, _ := cmd.Flags().GetBool("testflight")
	appStore, _ := cmd.Flags().GetBool("app-store")
	if testflight && appStore {
		return fmt.Errorf("pass only one of --testflight or --app-store")
	}
	releaseFlag, _ := cmd.Flags().GetString("release")
	releaseType, err := parseReleaseType(releaseFlag)
	if err != nil {
		return err
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}
	bo, err := buildOptionsFromFlags(cmd, cfg)
	if err != nil {
		return err
	}
	opts := &release.Options{Build: bo, AppStore: appStore, ReleaseType: releaseType}
	opts.BundleID, _ = cmd.Flags().GetString("bundle-id")
	opts.BuildNumber, _ = cmd.Flags().GetString("build-number")
	opts.Version, _ = cmd.Flags().GetString("version")
	opts.Groups, _ = cmd.Flags().GetStringArray("group")
	opts.Notes, _ = cmd.Flags().GetString("notes")
	opts.NoEncryption, _ = cmd.Flags().GetBool("no-encryption")
	return runRelease(cmd, cfg, opts)
}

// buildOptionsFromFlags reads the flags `ios build` and `ios release` share.
// Provider is the one that will run the job (--provider, else the profile's,
// else builder.json's), so the GitHub client and signal handling agree with
// the coordinator.
func buildOptionsFromFlags(cmd *cobra.Command, cfg *config.Config) (build.BuildOptions, error) {
	var opts build.BuildOptions
	opts.OutputDir, _ = cmd.Flags().GetString("output")
	opts.Timeout, _ = cmd.Flags().GetDuration("timeout")
	opts.Remote, _ = cmd.Flags().GetString("remote")
	opts.Profile, _ = cmd.Flags().GetString("profile")
	providerFlag, _ := cmd.Flags().GetString("provider")
	provider, err := effectiveProvider(cfg, opts.Profile, providerFlag)
	if err != nil {
		return opts, err
	}
	opts.Provider = provider
	return opts, nil
}

// runRelease is the flow behind `ios release` and `ios build --submit`. The
// API key is checked first; release.Run checks the builder.json preconditions
// before it dispatches anything.
func runRelease(cmd *cobra.Command, cfg *config.Config, opts *release.Options) error {
	client, err := getASCClient()
	if err != nil {
		return err
	}
	ghClient, err := clientForProvider(cfg, opts.Build.Provider)
	if err != nil {
		return err
	}
	out := newOutput(cmd)
	opts.Log = out.log
	ctx, cancel := commandContext(cmd, false)
	defer cancel()

	coordinator := build.NewCoordinatorWithOutput(cfg, ghClient, out.log)
	res, err := release.Run(ctx, cfg, coordinator, client, opts)
	return finish(out, cmd, res, err, func() {
		fmt.Println()
		fmt.Printf("IPA:          %s\n", res.IPAPath)
		fmt.Printf("Build number: %s (version %s)\n", res.BuildNumber, res.Version)
		fmt.Printf("ASC build:    %s\n", res.ASCBuildID)
		if len(res.Groups) > 0 {
			names := make([]string, 0, len(res.Groups))
			for _, g := range res.Groups {
				names = append(names, g.Name)
			}
			fmt.Printf("Groups:       %s\n", strings.Join(names, ", "))
		}
		fmt.Printf("Link:         %s\n", res.Link)
	})
}
