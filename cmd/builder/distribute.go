package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/MobAI-App/ios-builder/internal/config"
	"github.com/MobAI-App/ios-builder/internal/distribute"
	"github.com/MobAI-App/ios-builder/internal/ipa"
	"github.com/MobAI-App/ios-builder/internal/otainstall"
	"github.com/MobAI-App/ios-builder/internal/release"
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
enterprise profile; App Store builds go through --backend testflight.

Backends (--backend, else distribute.backend in builder.json, else github):

  github      a draft release in the project's repository (no tag, not on the
              repository page) holds the IPA, a secret gist the manifest. Links
              live five minutes and are refreshed while the command runs.
  s3          an S3 bucket, or an S3-compatible one through distribute.endpoint
              (Cloudflare R2, MinIO, Google Cloud Storage with HMAC keys):
              distribute.bucket, .region, .prefix. Credentials from
              AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY / AWS_SESSION_TOKEN or
              AWS_PROFILE in ~/.aws/credentials. Presigned links live --ttl.
  azure       an Azure Blob container: distribute.account, .container, .prefix;
              the key from AZURE_STORAGE_KEY or AZURE_STORAGE_CONNECTION_STRING.
              Service SAS links live --ttl.
  testflight  uploads an App Store signed IPA to App Store Connect, waits for
              processing and adds it to the internal TestFlight group --group
              (or distribute.group; created if missing, external groups refused).
              Testers install from the TestFlight app; no link, no QR code.

The uploads are removed when the session ends (q, Ctrl-C or --timeout). --once
prints one link and leaves them (with s3/azure the link lives --ttl, up to
seven days); --cleanup removes what earlier sessions left behind.

Takes the newest IPA in --output, or --ipa. builder ios build --distribute
builds first.`,
	Args: cobra.NoArgs,
	RunE: runIOSDistribute,
}

func init() {
	f := iosDistributeCmd.Flags()
	f.String("ipa", "", "IPA to distribute (default: the newest in the output directory)")
	f.StringP("output", "o", "dist", "Directory holding the IPA")
	f.Duration("timeout", time.Hour, "How long to keep the link alive (testflight: how long to wait for processing)")
	f.Bool("once", false, "Print one link and leave the upload in place (no refresh, no cleanup)")
	f.Bool("cleanup", false, "Remove the uploads earlier sessions left behind, then exit")
	f.Bool("qr", false, "Print the QR code even when stdout is not a terminal")
	f.Bool("no-qr", false, "Never print the QR code")
	f.Bool("qr-invert", false, "Render the QR code for a dark-on-light terminal")
	f.Bool("json", false, "Print one JSON object per link (progress goes to stderr); no QR code, no prompt")
	addDistributeFlags(f.String, f.Duration)
	f.String("notes", "", "What to Test notes for the build (testflight)")
	f.Bool("no-encryption", false, "Declare the app uses no non-exempt encryption (testflight)")
	iosCmd.AddCommand(iosDistributeCmd)

	bf := iosBuildCmd.Flags()
	bf.Bool("distribute", false, "After the build, print an over-the-air install link and QR code (see: ios distribute)")
	addDistributeFlags(bf.String, bf.Duration)
}

// addDistributeFlags declares the backend flags ios distribute and ios build
// --distribute share.
func addDistributeFlags(str func(string, string, string) *string, dur func(string, time.Duration, string) *time.Duration) {
	str("backend", "", "Where the install goes: github, s3, azure or testflight (default: distribute.backend in builder.json, else github)")
	dur("ttl", 0, "Link lifetime for s3 and azure (2m to 168h; default 1h)")
	str("group", "", "Internal TestFlight group for --backend testflight (default: distribute.group in builder.json)")
}

func runIOSDistribute(cmd *cobra.Command, _ []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}
	target, err := distributeTarget(cmd, cfg)
	if err != nil {
		return err
	}
	if cleanup, _ := cmd.Flags().GetBool("cleanup"); cleanup {
		return runDistributeCleanup(cmd, target)
	}
	path, _ := cmd.Flags().GetString("ipa")
	if path == "" {
		dir, _ := cmd.Flags().GetString("output")
		if path, err = ipa.Newest(dir); err != nil {
			return err
		}
	}
	return runDistribute(cmd, target, path)
}

// target is a resolved --backend: Backend for the over-the-air ones, Group
// for testflight.
type target struct {
	Name    string
	Backend otainstall.Backend
	Group   string
}

// distributeTarget resolves the backend and its settings, so a missing bucket,
// credential or group fails before a build is pushed.
func distributeTarget(cmd *cobra.Command, cfg *config.Config) (*target, error) {
	flag, _ := cmd.Flags().GetString("backend")
	name, err := otainstall.BackendName(flag, cfg)
	if err != nil {
		return nil, err
	}
	ttl, _ := cmd.Flags().GetDuration("ttl")
	group, _ := cmd.Flags().GetString("group")
	dc := cfg.Distribute
	if dc == nil {
		dc = &config.DistributeConfig{}
	}
	t := &target{Name: name}
	if ttl != 0 && name != otainstall.BackendS3 && name != otainstall.BackendAzure {
		return nil, fmt.Errorf("--ttl applies to the s3 and azure backends; %s links have a fixed lifetime", name)
	}
	if group != "" && name != otainstall.BackendTestFlight {
		return nil, fmt.Errorf("--group applies to --backend testflight")
	}
	switch name {
	case otainstall.BackendGitHub:
		gh, err := getGitHubClient()
		if err != nil {
			return nil, err
		}
		t.Backend = otainstall.NewGitHub(gh, cfg.GitHub.Owner, cfg.GitHub.Repo)
	case otainstall.BackendS3:
		if t.Backend, err = otainstall.S3FromConfig(dc, ttl, os.Getenv); err != nil {
			return nil, err
		}
	case otainstall.BackendAzure:
		if t.Backend, err = otainstall.AzureFromConfig(dc, ttl, os.Getenv); err != nil {
			return nil, err
		}
	case otainstall.BackendTestFlight:
		t.Group = group
		if t.Group == "" {
			t.Group = dc.Group
		}
		if t.Group == "" {
			return nil, errors.New("--backend testflight needs an internal TestFlight group: pass --group <name> or set distribute.group in builder.json (a missing group is created)")
		}
	}
	return t, nil
}

func runDistributeCleanup(cmd *cobra.Command, t *target) error {
	if t.Backend == nil {
		return fmt.Errorf("--backend %s leaves nothing to clean up; expire old builds with builder asc builds expire", t.Name)
	}
	ctx, cancel := commandContext(cmd, false)
	defer cancel()
	n, err := t.Backend.Cleanup(ctx)
	if err != nil {
		return err
	}
	if out := newOutput(cmd); out.json {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]int{"removed": n})
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Removed %d leftover upload(s).\n", n)
	return nil
}

// runDistribute is the flow behind `ios distribute` and `ios build --distribute`.
func runDistribute(cmd *cobra.Command, t *target, ipaPath string) error {
	app, err := otainstall.Inspect(ipaPath, t.Name)
	if err != nil {
		return err
	}
	if t.Name == otainstall.BackendTestFlight {
		return runDistributeTestFlight(cmd, t, ipaPath)
	}
	out := newOutput(cmd)
	opts := &otainstall.Options{App: app, Backend: t.Backend, Log: out.log, Stdin: cmd.InOrStdin()}
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
		opts.Progress = uploadProgress(out.log)
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

// runDistributeTestFlight uploads an App Store IPA and hands it to the
// internal group: the TestFlight app is the install link.
func runDistributeTestFlight(cmd *cobra.Command, t *target, ipaPath string) error {
	client, err := getASCClient()
	if err != nil {
		return err
	}
	out := newOutput(cmd)
	notes, _ := cmd.Flags().GetString("notes")
	noEncryption, _ := cmd.Flags().GetBool("no-encryption")
	ctx, cancel := commandContext(cmd, true)
	defer cancel()
	res, err := distribute.ToInternalGroup(ctx, client, &distribute.InternalGroupOptions{IPAPath: ipaPath, Group: t.Group, Notes: notes, NoEncryption: noEncryption, Log: out.log})
	return finish(out, cmd, res, err, func() {
		w := cmd.OutOrStdout()
		tf := res.TestFlight
		fmt.Fprintln(w)
		fmt.Fprintf(w, "Build:    %s (build %s)\n", tf.Build.ID, tf.Build.BuildNumber)
		for _, g := range tf.Groups {
			fmt.Fprintf(w, "Group:    %s (internal)\n", g.Name)
		}
		fmt.Fprintf(w, "Link:     %s\n", tf.Link)
		fmt.Fprintf(w, "Testers in %s install it from the TestFlight app on their iPhone.\n", t.Group)
	})
}

// buildDistributeTarget is the preflight of `ios build --distribute`: the
// profile must sign the way the backend needs and the backend must be
// configured, all before anything is pushed.
func buildDistributeTarget(cmd *cobra.Command, cfg *config.Config, profile string) (*target, error) {
	flag, _ := cmd.Flags().GetString("backend")
	name, err := otainstall.BackendName(flag, cfg)
	if err != nil {
		return nil, err
	}
	s, err := cfg.ResolveProfile(profile)
	if err != nil {
		return nil, err
	}
	if err := otainstall.CheckDistribution(&s, name); err != nil {
		return nil, err
	}
	return distributeTarget(cmd, cfg)
}

// runBuildTestFlight is `ios build --distribute --backend testflight`: ios
// release to the one internal group, which also picks the next build number
// App Store Connect will accept.
func runBuildTestFlight(cmd *cobra.Command, cfg *config.Config, t *target, opts *release.Options) error {
	opts.Groups, opts.InternalGroups = []string{t.Group}, true
	return runRelease(cmd, cfg, opts)
}

// uploadProgress draws the IPA upload as a bar on one line, redrawn when the
// percentage changes, and ends the line when the upload does.
func uploadProgress(w io.Writer) func(done, total int64) {
	last := -1
	return func(done, total int64) {
		pct := 100
		if total > 0 {
			pct = int(done * 100 / total)
		}
		if pct == last {
			return
		}
		last = pct
		filled := pct / 5
		fmt.Fprintf(w, "\r\033[K⬆️  Upload: [%s%s] %d%% (%.1f/%.1f MB)",
			strings.Repeat("█", filled), strings.Repeat("░", 20-filled), pct, float64(done)/(1<<20), float64(total)/(1<<20))
		if done >= total {
			fmt.Fprintln(w)
		}
	}
}

// isTerminal reports whether w is a terminal (a *os.File that is a tty).
func isTerminal(w any) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}
