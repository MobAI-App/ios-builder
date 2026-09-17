package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/MobAI-App/ios-builder/internal/asc"
	"github.com/MobAI-App/ios-builder/internal/auth"
	"github.com/MobAI-App/ios-builder/internal/distribute"
	"github.com/MobAI-App/ios-builder/internal/ipa"
	"github.com/spf13/cobra"
)

var iosUploadCmd = &cobra.Command{
	Use:   "upload",
	Short: "Upload the IPA to App Store Connect",
	Long: `Uploads an IPA to App Store Connect through the API, from any platform: no
Mac, Transporter or altool involved. The IPA must be signed with an Apple
Distribution certificate and an App Store provisioning profile.

The bundle ID, version and build number are read from the IPA. With --wait the
command follows processing until the build is usable, and answers the export
compliance question when Info.plist declares ITSAppUsesNonExemptEncryption
false (or --no-encryption is given), so the build does not sit in "Missing
Compliance".

Needs an App Store Connect API key: builder auth apple.`,
	Args: cobra.NoArgs,
	RunE: runIOSUpload,
}

func init() {
	iosUploadCmd.Flags().String("ipa", "", "IPA to upload (default: newest .ipa in ./dist)")
	iosUploadCmd.Flags().Bool("wait", false, "Wait until App Store Connect has processed the build")
	iosUploadCmd.Flags().Duration("timeout", 30*time.Minute, "Give up waiting after this long")
	iosUploadCmd.Flags().Bool("no-encryption", false, "Declare the app uses no non-exempt encryption (export compliance)")
	iosUploadCmd.Flags().Bool("json", false, "Print the result as JSON (progress goes to stderr)")
	iosCmd.AddCommand(iosUploadCmd)
}

// getASCClient builds an App Store Connect client from the saved Apple login
// or the ASC_* environment variables. Tests point it at a fake server.
var getASCClient = func() (*asc.Client, error) {
	creds, _, err := auth.GetAppleCredentials()
	if err != nil {
		if errors.Is(err, auth.ErrNotAuthenticated) {
			return nil, fmt.Errorf("no App Store Connect API key configured. Run: builder auth apple (or set ASC_ISSUER_ID, ASC_KEY_ID and ASC_PRIVATE_KEY or ASC_KEY_PATH)")
		}
		return nil, err
	}
	return asc.NewClient(asc.Credentials{IssuerID: creds.IssuerID, KeyID: creds.KeyID, PrivateKey: creds.PrivateKey})
}

func resolveIPA(path string) (string, error) {
	if path != "" {
		return path, nil
	}
	return ipa.Newest("dist")
}

// commandContext cancels on Ctrl-C and, when waiting, after --timeout.
func commandContext(cmd *cobra.Command, wait bool) (context.Context, context.CancelFunc) {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	if !wait {
		return ctx, stop
	}
	timeout, _ := cmd.Flags().GetDuration("timeout")
	ctx, cancel := context.WithTimeout(ctx, timeout)
	return ctx, func() { cancel(); stop() }
}

// output separates human progress from the machine-readable result.
type output struct {
	json bool
	log  io.Writer
}

func newOutput(cmd *cobra.Command) output {
	asJSON, _ := cmd.Flags().GetBool("json")
	if asJSON {
		return output{json: true, log: cmd.ErrOrStderr()}
	}
	return output{log: cmd.OutOrStdout()}
}

// finish prints the result (JSON, or the human summary on success) and
// returns err with a timeout translated into something actionable. A partial
// result on failure is still printed as JSON so agents see how far it got.
func finish[T any](o output, cmd *cobra.Command, result *T, err error, human func()) error {
	if o.json && result != nil {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
	}
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("timed out waiting for App Store Connect; processing continues server-side, check later with builder ios submit --testflight or raise --timeout")
		}
		return err
	}
	if !o.json && human != nil {
		human()
	}
	return nil
}

func runIOSUpload(cmd *cobra.Command, _ []string) error {
	client, err := getASCClient()
	if err != nil {
		return err
	}
	ipaPath, _ := cmd.Flags().GetString("ipa")
	if ipaPath, err = resolveIPA(ipaPath); err != nil {
		return err
	}
	wait, _ := cmd.Flags().GetBool("wait")
	noEncryption, _ := cmd.Flags().GetBool("no-encryption")
	ctx, cancel := commandContext(cmd, wait)
	defer cancel()
	out := newOutput(cmd)

	res, err := distribute.Upload(ctx, client, &distribute.UploadOptions{IPAPath: ipaPath, Wait: wait, NoEncryption: noEncryption, Log: out.log})
	return finish(out, cmd, res, err, func() {
		fmt.Println()
		fmt.Printf("Upload ID: %s (%s)\n", res.Upload.ID, res.Upload.State)
		if res.Build != nil {
			fmt.Printf("Build ID:  %s (build %s, %s)\n", res.Build.ID, res.Build.BuildNumber, res.Build.ProcessingState)
		} else {
			fmt.Println("Processing continues in App Store Connect; rerun with --wait to follow it.")
		}
		fmt.Printf("Link:      %s\n", res.Link)
	})
}
