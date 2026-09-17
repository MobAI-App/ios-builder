package distribute

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/MobAI-App/ios-builder/internal/asc"
	"github.com/MobAI-App/ios-builder/internal/ipa"
)

// UploadOptions configures Upload.
type UploadOptions struct {
	IPAPath string
	// Wait polls until the delivery completes and the build is VALID.
	Wait bool
	// NoEncryption answers the export compliance question with "no", even
	// when the IPA's Info.plist does not declare ITSAppUsesNonExemptEncryption.
	NoEncryption bool
	PollInterval time.Duration
	// Log receives progress lines; nil discards them.
	Log io.Writer
}

// IPARef describes the uploaded archive.
type IPARef struct {
	Path                    string `json:"path"`
	Version                 string `json:"version"`
	BuildNumber             string `json:"build_number"`
	UsesNonExemptEncryption *bool  `json:"uses_non_exempt_encryption"`
}

// UploadRef describes the delivery.
type UploadRef struct {
	ID       string   `json:"id"`
	State    string   `json:"state"`
	Errors   []string `json:"errors,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// UploadResult is what Upload reports.
type UploadResult struct {
	App    AppRef    `json:"app"`
	IPA    IPARef    `json:"ipa"`
	Upload UploadRef `json:"upload"`
	// Build is set once processing finished (Wait).
	Build *BuildRef `json:"build,omitempty"`
	// Compliance is set_exempt, already_set, pending or skipped.
	Compliance string `json:"encryption_compliance"`
	Link       string `json:"link"`
}

// Upload delivers the IPA to App Store Connect and, with Wait, follows it
// until the build is VALID and its export compliance is answered.
func Upload(ctx context.Context, client *asc.Client, opts *UploadOptions) (*UploadResult, error) {
	info, err := ipa.ReadInfo(opts.IPAPath)
	if err != nil {
		return nil, err
	}
	if info.Version == "" || info.BuildNumber == "" {
		return nil, fmt.Errorf("%s: Info.plist lacks CFBundleShortVersionString or CFBundleVersion", opts.IPAPath)
	}
	app, err := client.AppByBundleID(ctx, info.BundleID)
	if err != nil {
		return nil, err
	}
	res := &UploadResult{
		App:        appRef(app),
		IPA:        IPARef{Path: opts.IPAPath, Version: info.Version, BuildNumber: info.BuildNumber, UsesNonExemptEncryption: info.UsesNonExemptEncryption},
		Compliance: "skipped",
		Link:       testflightLink(app.ID),
	}
	exempt := opts.NoEncryption || (info.UsesNonExemptEncryption != nil && !*info.UsesNonExemptEncryption)

	logf(opts.Log, "Uploading %s (%s build %s) to %s...", opts.IPAPath, info.Version, info.BuildNumber, app.Name)
	var lastPercent int64 = -1
	upload, err := client.UploadBuild(ctx, &asc.UploadBuildOptions{
		AppID: app.ID, Version: info.Version, BuildNumber: info.BuildNumber, Platform: asc.PlatformIOS, Path: opts.IPAPath,
		Progress: func(sent, total int64) {
			if total == 0 {
				return
			}
			if pct := sent * 100 / total; pct/10 > lastPercent/10 || pct == 100 {
				lastPercent = pct
				logf(opts.Log, "  %d%% (%d/%d MB)", pct, sent>>20, total>>20)
			}
		},
	})
	if err != nil {
		return nil, err
	}
	res.Upload = UploadRef{ID: upload.ID, State: upload.State, Errors: joinDetails(upload.Errors), Warnings: joinDetails(upload.Warnings)}
	logf(opts.Log, "Upload %s accepted; App Store Connect is processing it.", upload.ID)

	if !opts.Wait {
		if exempt {
			res.Compliance = "pending"
			logf(opts.Log, "Export compliance will be set once the build exists: rerun with --wait, or pass --no-encryption to builder ios submit.")
		}
		return res, nil
	}

	interval := pollInterval(opts.PollInterval)
	lastState := ""
	upload, err = client.WaitForBuildUpload(ctx, upload.ID, interval, func(u *asc.BuildUpload) {
		if u.State != lastState {
			lastState = u.State
			logf(opts.Log, "  delivery: %s", u.State)
		}
	})
	if upload != nil {
		res.Upload = UploadRef{ID: upload.ID, State: upload.State, Errors: joinDetails(upload.Errors), Warnings: joinDetails(upload.Warnings)}
	}
	if err != nil {
		var failed *asc.UploadFailedError
		if errors.As(err, &failed) {
			return res, err
		}
		return res, fmt.Errorf("wait for delivery: %w", err)
	}
	for _, w := range res.Upload.Warnings {
		logf(opts.Log, "  warning: %s", w)
	}

	logf(opts.Log, "Waiting for build %s to finish processing...", info.BuildNumber)
	lastState = ""
	build, err := client.WaitForBuild(ctx, app.ID, info.Version, info.BuildNumber, interval, func(b *asc.Build) {
		state := "not visible yet"
		if b != nil {
			state = b.ProcessingState
		}
		if state != lastState {
			lastState = state
			logf(opts.Log, "  build: %s", state)
		}
	})
	if build != nil {
		ref := buildRef(app.ID, info.Version, build)
		res.Build = &ref
		res.Link = ref.Link
	}
	if err != nil {
		return res, err
	}
	res.Compliance, err = setCompliance(ctx, client, opts.Log, build, exempt)
	if err != nil {
		return res, err
	}
	res.Build.UsesNonExemptEncryption = build.UsesNonExemptEncryption
	if res.Compliance == "pending" {
		logf(opts.Log, "Export compliance is unanswered; TestFlight shows the build as Missing Compliance until it is. Declare ITSAppUsesNonExemptEncryption in Info.plist, or pass --no-encryption.")
	}
	logf(opts.Log, "Build %s (%s) is VALID: %s", build.BuildNumber, build.ID, res.Link)
	return res, nil
}
