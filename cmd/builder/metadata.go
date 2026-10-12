package main

import (
	"fmt"

	"github.com/MobAI-App/ios-builder/internal/metadata"
	"github.com/spf13/cobra"
)

var iosMetadataCmd = &cobra.Command{
	Use:   "metadata",
	Short: "Pull and push the App Store listing (text, categories, screenshots)",
	Long: `Syncs the App Store listing with files laid out like fastlane deliver's, so
an existing fastlane setup works unchanged:

  metadata/<locale>/name.txt, subtitle.txt, privacy_url.txt        (app info)
  metadata/<locale>/description.txt, keywords.txt, release_notes.txt,
                    promotional_text.txt, marketing_url.txt, support_url.txt
  metadata/primary_category.txt, secondary_category.txt             (PRODUCTIVITY, ...)
  screenshots/<locale>/*.png|jpg               display type from the pixel size
  screenshots/<locale>/<DISPLAY_TYPE>/*.png    explicit, e.g. APP_IPHONE_67

The version worked on is the one being prepared for submission, or --version.
The app is identified by --bundle-id, --ipa, ios.bundleId in builder.json, or
the newest IPA in ./dist. Needs an App Store Connect API key: builder auth apple.`,
}

var iosMetadataPullCmd = &cobra.Command{
	Use:   "pull",
	Short: "Write the App Store listing into ./metadata (and ./screenshots)",
	Long: `Writes every non-empty field of the version being prepared (else the newest
version) into the metadata directory. Local files App Store Connect has no
value for are kept unless --clean. Screenshots download only with --screenshots,
into screenshots/<locale>/<DISPLAY_TYPE>/NN_<name>.`,
	Args: cobra.NoArgs,
	RunE: runIOSMetadataPull,
}

var iosMetadataPushCmd = &cobra.Command{
	Use:   "push",
	Short: "Update the App Store listing from ./metadata (and ./screenshots)",
	Long: `Compares the files with App Store Connect, prints a "Will ..." line per
difference and, with --yes, writes only those. Fields without a file are left
alone; an empty file clears the field. Lengths (name and subtitle 30,
keywords 100, promotional text 170, description and release notes 4000), URLs,
categories and screenshot sizes are checked before anything is sent.

--version X.Y targets that version, creating it when it does not exist.
Screenshots are compared by checksum and only pushed with --screenshots:
missing ones are appended to their set, and --replace-screenshots deletes a
set's screenshots and uploads the local ones whenever the two differ.`,
	Args: cobra.NoArgs,
	RunE: runIOSMetadataPush,
}

func init() {
	for _, cmd := range []*cobra.Command{iosMetadataPullCmd, iosMetadataPushCmd} {
		cmd.Flags().String("bundle-id", "", "App bundle ID (default: ios.bundleId in builder.json, else the newest IPA in ./dist)")
		cmd.Flags().String("ipa", "", "Read the bundle ID from this IPA")
		cmd.Flags().String("version", "", "App Store version (default: the one being prepared for submission)")
		cmd.Flags().String("metadata-dir", "metadata", "Directory of <locale>/<field>.txt files")
		cmd.Flags().String("screenshots-dir", "screenshots", "Directory of <locale>/ screenshot folders")
		cmd.Flags().Bool("screenshots", false, "Include screenshots")
		cmd.Flags().Bool("json", false, "Print the result as JSON (progress goes to stderr)")
	}
	iosMetadataPullCmd.Flags().Bool("clean", false, "Delete local files App Store Connect has no value for")
	iosMetadataPushCmd.Flags().Bool("yes", false, "Apply the changes (without it the plan is printed and the command fails)")
	iosMetadataPushCmd.Flags().Bool("dry-run", false, "Print the plan and exit successfully without changing anything")
	iosMetadataPushCmd.Flags().Bool("replace-screenshots", false, "Replace a set's screenshots when they differ from the local ones")
	iosMetadataCmd.AddCommand(iosMetadataPullCmd, iosMetadataPushCmd)
	iosCmd.AddCommand(iosMetadataCmd)
}

func metadataOptions(cmd *cobra.Command, log output) (*metadata.Options, error) {
	bundleID, _, err := resolveApp(cmd)
	if err != nil {
		return nil, err
	}
	opts := &metadata.Options{BundleID: bundleID, Log: log.log}
	opts.Version, _ = cmd.Flags().GetString("version")
	opts.MetadataDir, _ = cmd.Flags().GetString("metadata-dir")
	opts.ScreenshotsDir, _ = cmd.Flags().GetString("screenshots-dir")
	opts.Screenshots, _ = cmd.Flags().GetBool("screenshots")
	if cmd.Flags().Lookup("clean") != nil {
		opts.Clean, _ = cmd.Flags().GetBool("clean")
	}
	if cmd.Flags().Lookup("replace-screenshots") != nil {
		opts.ReplaceScreenshots, _ = cmd.Flags().GetBool("replace-screenshots")
	}
	return opts, nil
}

func runIOSMetadataPull(cmd *cobra.Command, _ []string) error {
	client, err := getASCClient()
	if err != nil {
		return err
	}
	out := newOutput(cmd)
	opts, err := metadataOptions(cmd, out)
	if err != nil {
		return err
	}
	ctx, cancel := commandContext(cmd, false)
	defer cancel()
	res, err := metadata.Pull(ctx, client, opts)
	if res != nil {
		for _, w := range res.Warnings {
			logf(out.log, "Warning: %s", w)
		}
	}
	return finish(out, cmd, res, err, func() {
		w := cmd.OutOrStdout()
		fmt.Fprintf(w, "Pulled %s version %s: %d locales, %d files written, %d unchanged", res.App.Name, res.Version.VersionString, len(res.Locales), len(res.Written), res.Unchanged)
		if len(res.Screenshots) > 0 {
			fmt.Fprintf(w, ", %d screenshots downloaded", len(res.Screenshots))
		}
		if len(res.Removed) > 0 {
			fmt.Fprintf(w, ", %d removed", len(res.Removed))
		}
		fmt.Fprintln(w)
	})
}

func runIOSMetadataPush(cmd *cobra.Command, _ []string) error {
	yes, _ := cmd.Flags().GetBool("yes")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	if yes && dryRun {
		return fmt.Errorf("--yes and --dry-run exclude each other")
	}
	client, err := getASCClient()
	if err != nil {
		return err
	}
	out := newOutput(cmd)
	opts, err := metadataOptions(cmd, out)
	if err != nil {
		return err
	}
	ctx, cancel := commandContext(cmd, false)
	defer cancel()
	plan, err := metadata.NewPlan(ctx, client, opts)
	if err != nil {
		return err
	}
	for _, w := range plan.Warnings {
		logf(out.log, "Warning: %s", w)
	}
	if plan.Empty() {
		logf(out.log, "%s version %s already matches %s; nothing to push", plan.App.Name, plan.Version.VersionString, opts.MetadataDir)
		return finish(out, cmd, plan, nil, nil)
	}
	logf(out.log, "%s version %s:", plan.App.Name, plan.Version.VersionString)
	for i := range plan.Changes {
		logf(out.log, "  %s", plan.Changes[i].String())
	}
	if dryRun {
		return finish(out, cmd, plan, nil, nil)
	}
	if !yes {
		return finish(out, cmd, plan, fmt.Errorf("pass --yes to apply these %d changes (or --dry-run to only print them)", len(plan.Changes)), nil)
	}
	err = plan.Apply(ctx)
	return finish(out, cmd, plan, err, func() {
		fmt.Fprintf(cmd.OutOrStdout(), "Updated %s version %s: %d changes\n", plan.App.Name, plan.Version.VersionString, len(plan.Changes))
	})
}
