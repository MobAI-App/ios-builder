package main

import (
	"fmt"
	"time"

	"github.com/MobAI-App/ios-builder/internal/asc"
	"github.com/MobAI-App/ios-builder/internal/distribute"
	"github.com/MobAI-App/ios-builder/internal/ipa"
	"github.com/spf13/cobra"
)

var iosSubmitCmd = &cobra.Command{
	Use:   "submit",
	Short: "Hand a processed build to TestFlight groups or App Review",
	Long: `Distributes a build that App Store Connect has already processed.

  --testflight   adds the build to the named TestFlight groups (--group, repeatable),
                 sets the "What to Test" notes (--notes) and, for external groups,
                 submits the build for beta review. Without --group it reports the
                 build and lists the available groups.
  --app-store    finds or creates the App Store version for the marketing version,
                 attaches the build, sets the release type and submits it for review.
                 The version's metadata (description, screenshots, pricing, privacy)
                 must already be complete in App Store Connect.

The app is identified by the IPA in ./dist (or --ipa), or by --bundle-id. The
newest VALID build is used unless --build-number is given.`,
	Args: cobra.NoArgs,
	RunE: runIOSSubmit,
}

func init() {
	iosSubmitCmd.Flags().Bool("testflight", false, "Distribute to TestFlight")
	iosSubmitCmd.Flags().Bool("app-store", false, "Submit an App Store version for review")
	iosSubmitCmd.Flags().String("ipa", "", "IPA whose bundle ID and version identify the app (default: newest .ipa in ./dist)")
	iosSubmitCmd.Flags().String("bundle-id", "", "App bundle ID, instead of reading an IPA")
	iosSubmitCmd.Flags().String("build-number", "", "Build number (CFBundleVersion) to use (default: newest VALID build)")
	iosSubmitCmd.Flags().String("version", "", "Marketing version (default: from the IPA; required with --app-store and --bundle-id)")
	iosSubmitCmd.Flags().StringArray("group", nil, "TestFlight group name to add the build to (repeatable)")
	iosSubmitCmd.Flags().String("notes", "", "What to Test notes for the build")
	iosSubmitCmd.Flags().String("locale", "", "Locale for --notes (default: the app's primary locale)")
	iosSubmitCmd.Flags().String("release", "", "App Store release: manual or after-approval")
	iosSubmitCmd.Flags().Bool("no-encryption", false, "Declare the app uses no non-exempt encryption (export compliance)")
	iosSubmitCmd.Flags().Bool("wait", false, "Wait for the external beta review decision (--testflight)")
	iosSubmitCmd.Flags().Duration("timeout", 30*time.Minute, "Give up waiting after this long")
	iosSubmitCmd.Flags().Bool("json", false, "Print the result as JSON (progress goes to stderr)")
	iosCmd.AddCommand(iosSubmitCmd)
}

func runIOSSubmit(cmd *cobra.Command, _ []string) error {
	testflight, _ := cmd.Flags().GetBool("testflight")
	appStore, _ := cmd.Flags().GetBool("app-store")
	if testflight == appStore {
		return fmt.Errorf("pass exactly one of --testflight or --app-store")
	}
	client, err := getASCClient()
	if err != nil {
		return err
	}
	bundleID, _ := cmd.Flags().GetString("bundle-id")
	version, _ := cmd.Flags().GetString("version")
	if bundleID == "" {
		ipaPath, _ := cmd.Flags().GetString("ipa")
		if ipaPath, err = resolveIPA(ipaPath); err != nil {
			return fmt.Errorf("%w (or pass --bundle-id)", err)
		}
		info, err := ipa.ReadInfo(ipaPath)
		if err != nil {
			return err
		}
		bundleID = info.BundleID
		if version == "" && appStore {
			version = info.Version
		}
	}
	buildNumber, _ := cmd.Flags().GetString("build-number")
	noEncryption, _ := cmd.Flags().GetBool("no-encryption")
	wait, _ := cmd.Flags().GetBool("wait")
	ctx, cancel := commandContext(cmd, wait)
	defer cancel()
	out := newOutput(cmd)

	if testflight {
		groups, _ := cmd.Flags().GetStringArray("group")
		notes, _ := cmd.Flags().GetString("notes")
		locale, _ := cmd.Flags().GetString("locale")
		res, err := distribute.SubmitTestFlight(ctx, client, &distribute.TestFlightOptions{
			BundleID: bundleID, Version: version, BuildNumber: buildNumber, Groups: groups, Notes: notes, Locale: locale,
			NoEncryption: noEncryption, Wait: wait, Log: out.log,
		})
		return finish(out, cmd, res, err, func() {
			fmt.Println()
			fmt.Printf("Build ID: %s (build %s)\n", res.Build.ID, res.Build.BuildNumber)
			if res.BetaReview != nil {
				fmt.Printf("Beta review: %s\n", res.BetaReview.State)
			}
			fmt.Printf("Link:     %s\n", res.Link)
		})
	}

	releaseFlag, _ := cmd.Flags().GetString("release")
	releaseType, err := parseReleaseType(releaseFlag)
	if err != nil {
		return err
	}
	res, err := distribute.SubmitAppStore(ctx, client, &distribute.AppStoreOptions{
		BundleID: bundleID, Version: version, BuildNumber: buildNumber, ReleaseType: releaseType, NoEncryption: noEncryption, Log: out.log,
	})
	return finish(out, cmd, res, err, func() {
		fmt.Println()
		fmt.Printf("Version:    %s (%s)\n", res.Version.VersionString, res.Version.State)
		fmt.Printf("Build ID:   %s (build %s)\n", res.Build.ID, res.Build.BuildNumber)
		fmt.Printf("Submission: %s (%s)\n", res.Submission.ID, res.Submission.State)
		fmt.Printf("Link:       %s\n", res.Link)
	})
}

func parseReleaseType(flag string) (string, error) {
	switch flag {
	case "":
		return "", nil
	case "manual":
		return asc.ReleaseTypeManual, nil
	case "after-approval":
		return asc.ReleaseTypeAfterApproval, nil
	}
	return "", fmt.Errorf("--release must be manual or after-approval, got %q", flag)
}
