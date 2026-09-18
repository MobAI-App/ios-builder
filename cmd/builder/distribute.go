package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/MobAI-App/ios-builder/internal/build"
	"github.com/MobAI-App/ios-builder/internal/config"
	"github.com/MobAI-App/ios-builder/internal/ipa"
	"github.com/MobAI-App/ios-builder/internal/otainstall"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var iosDistributeCmd = &cobra.Command{
	Use:   "distribute",
	Short: "Install a signed build on an iPhone over the air (link + QR code)",
	Long: `Puts the IPA where an iPhone can fetch it and prints an itms-services install
link with a QR code: scan it on the phone and iOS installs the app. No
TestFlight, no cable, no Mac.

The IPA must be signed with a development or ad-hoc profile that lists the
phone (builder signing setup --device <udid> or --devices-from-mobai) or an
enterprise profile; App Store builds cannot be installed this way.

The IPA goes to a draft release in the project's GitHub repository (no tag,
not visible on the repository page) and the manifest to a secret gist. Links
live five minutes; the command refreshes them while it runs and removes both
uploads when it ends (q, Ctrl-C or --timeout). --once prints one link and
leaves them; --cleanup removes what earlier sessions left behind.

Takes the newest IPA in --output, or --ipa. builder ios build --distribute
builds first.`,
	Args: cobra.NoArgs,
	RunE: runIOSDistribute,
}

func init() {
	f := iosDistributeCmd.Flags()
	f.String("ipa", "", "IPA to distribute (default: the newest in the output directory)")
	f.StringP("output", "o", "dist", "Directory holding the IPA")
	f.Duration("timeout", time.Hour, "How long to keep the link alive")
	f.Bool("once", false, "Print one link and leave the upload in place (no refresh, no cleanup)")
	f.Bool("cleanup", false, "Remove the draft releases and manifest gists earlier sessions left, then exit")
	f.Bool("qr", false, "Print the QR code even when stdout is not a terminal")
	f.Bool("no-qr", false, "Never print the QR code")
	f.Bool("qr-invert", false, "Render the QR code for a dark-on-light terminal")
	f.Bool("json", false, "Print one JSON object per link (progress goes to stderr); no QR code, no prompt")
	iosCmd.AddCommand(iosDistributeCmd)

	iosBuildCmd.Flags().Bool("distribute", false, "After the build, print an over-the-air install link and QR code (see: ios distribute)")
}

func runIOSDistribute(cmd *cobra.Command, _ []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}
	if cleanup, _ := cmd.Flags().GetBool("cleanup"); cleanup {
		return runDistributeCleanup(cmd, cfg)
	}
	path, _ := cmd.Flags().GetString("ipa")
	if path == "" {
		dir, _ := cmd.Flags().GetString("output")
		if path, err = ipa.Newest(dir); err != nil {
			return err
		}
	}
	return runDistribute(cmd, cfg, path)
}

func runDistributeCleanup(cmd *cobra.Command, cfg *config.Config) error {
	backend, err := distributeBackend(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := commandContext(cmd, false)
	defer cancel()
	n, err := backend.Cleanup(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Removed %d leftover upload(s).\n", n)
	return nil
}

func distributeBackend(cfg *config.Config) (otainstall.Backend, error) {
	gh, err := getGitHubClient()
	if err != nil {
		return nil, err
	}
	return otainstall.NewGitHub(gh, cfg.GitHub.Owner, cfg.GitHub.Repo), nil
}

// runDistribute is the flow behind `ios distribute` and `ios build --distribute`.
func runDistribute(cmd *cobra.Command, cfg *config.Config, ipaPath string) error {
	app, err := otainstall.Inspect(ipaPath)
	if err != nil {
		return err
	}
	backend, err := distributeBackend(cfg)
	if err != nil {
		return err
	}
	out := newOutput(cmd)
	opts := &otainstall.Options{App: app, Backend: backend, Log: out.log, Stdin: cmd.InOrStdin()}
	if cmd.Name() == "distribute" { // ios build's --timeout bounds the build, not the link
		opts.Timeout, _ = cmd.Flags().GetDuration("timeout")
	}
	opts.Once, _ = cmd.Flags().GetBool("once")
	opts.QRInvert, _ = cmd.Flags().GetBool("qr-invert")
	if out.json {
		opts.JSON = cmd.OutOrStdout()
	} else {
		qr, _ := cmd.Flags().GetBool("qr")
		noQR, _ := cmd.Flags().GetBool("no-qr")
		opts.QR = !noQR && (qr || isTerminal(cmd.OutOrStdout()))
		progress := build.NewProgress(out.log)
		opts.Progress = func(done, total int64) {
			progress.UpdateDownloadProgress(done, total)
			if done >= total {
				fmt.Fprintln(out.log)
			}
		}
	}
	if opts.Stdin != nil && !isTerminal(opts.Stdin) {
		opts.Stdin = nil
	}

	// The link outlives any build context; Ctrl-C ends the session and cleans up.
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	res, err := otainstall.Run(ctx, opts)
	if err != nil {
		return err
	}
	if len(res.Leftovers) > 0 && !opts.Once {
		return fmt.Errorf("cleanup failed; run builder ios distribute --cleanup to remove: %s", strings.Join(res.Leftovers, "; "))
	}
	return nil
}

// isTerminal reports whether w is a terminal (a *os.File that is a tty).
func isTerminal(w any) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}
