// Package release drives `builder ios release` and `builder ios build
// --submit`: pick the next build number from App Store Connect, build with it,
// check the IPA carries it, upload, wait for processing and hand the build to
// TestFlight groups or App Review. It composes build and distribute; every API
// call lives in asc.
package release

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/MobAI-App/ios-builder/internal/asc"
	"github.com/MobAI-App/ios-builder/internal/build"
	"github.com/MobAI-App/ios-builder/internal/config"
	"github.com/MobAI-App/ios-builder/internal/distribute"
	"github.com/MobAI-App/ios-builder/internal/ipa"
)

// Builder runs the remote build; *build.Coordinator implements it.
type Builder interface {
	Build(ctx context.Context, opts *build.BuildOptions) (*build.BuildResult, error)
}

// Options configures Run.
type Options struct {
	// Build carries Provider, Remote, Timeout and OutputDir; Run sets
	// BuildNumber and Unsigned itself.
	Build build.BuildOptions
	// BundleID identifies the app before the IPA exists. Empty falls back to
	// ios.bundleId in builder.json, then the newest IPA in the output directory.
	BundleID string
	// BuildNumber overrides the automatic next number; Version sets the
	// marketing version on the build (and is the App Store version to submit).
	BuildNumber string
	Version     string
	// AppStore submits for App Review with ReleaseType; otherwise the build
	// goes to the TestFlight Groups with Notes.
	AppStore    bool
	ReleaseType string
	Groups      []string
	Notes       string
	// NoEncryption answers export compliance with "no" when still unanswered.
	NoEncryption bool
	PollInterval time.Duration
	// Log receives progress lines; nil discards them.
	Log io.Writer
}

// Result is what Run reports.
type Result struct {
	BuildID     string `json:"build_id"`
	IPAPath     string `json:"ipa,omitempty"`
	WorkflowURL string `json:"workflow_url,omitempty"`
	BundleID    string `json:"bundle_id"`
	Version     string `json:"version,omitempty"`
	BuildNumber string `json:"build_number"`
	// ASCBuildID is set once App Store Connect has processed the build.
	ASCBuildID string                `json:"asc_build_id,omitempty"`
	Groups     []distribute.GroupRef `json:"groups"`
	Link       string                `json:"link,omitempty"`
}

// versionRe matches what Apple accepts for CFBundleVersion and
// CFBundleShortVersionString: one to three period-separated integers. The
// runner validates the same shape, so nothing else reaches its shell.
var versionRe = regexp.MustCompile(`^[0-9]+(\.[0-9]+){0,2}$`)

// Preflight reports the builder.json settings App Store Connect needs before
// anything is dispatched: a signed archive built in Release.
func Preflight(cfg *config.Config) error {
	if !cfg.IOS.Signing {
		return fmt.Errorf("ios.signing is false in builder.json; App Store Connect only accepts signed builds. Run builder signing setup with an Apple Distribution certificate and an App Store provisioning profile, then set \"signing\": true")
	}
	if cfg.IOS.Configuration != "Release" {
		got := cfg.IOS.Configuration
		if got == "" {
			got = "unset (Debug)"
		}
		return fmt.Errorf("ios.configuration is %s in builder.json; App Store Connect rejects Debug archives. Set \"configuration\": \"Release\"", got)
	}
	return nil
}

// Run builds, uploads and submits. A partial Result comes back with the error
// so callers can show how far it got.
func Run(ctx context.Context, cfg *config.Config, builder Builder, client *asc.Client, opts *Options) (*Result, error) {
	if err := Preflight(cfg); err != nil {
		return nil, err
	}
	if opts.BuildNumber != "" && !versionRe.MatchString(opts.BuildNumber) {
		return nil, fmt.Errorf("--build-number must be an integer or up to three period-separated integers, got %q", opts.BuildNumber)
	}
	if opts.Version != "" && !versionRe.MatchString(opts.Version) {
		return nil, fmt.Errorf("--version must be X, X.Y or X.Y.Z, got %q", opts.Version)
	}
	outputDir := opts.Build.OutputDir
	if outputDir == "" {
		outputDir = "dist"
	}
	bundleID, err := resolveBundleID(opts.BundleID, cfg.IOS.BundleID, outputDir)
	if err != nil {
		return nil, err
	}
	res := &Result{BundleID: bundleID, Version: opts.Version, BuildNumber: opts.BuildNumber, Groups: []distribute.GroupRef{}}

	app, err := client.AppByBundleID(ctx, bundleID)
	if err != nil {
		return nil, err
	}
	if res.BuildNumber == "" {
		res.BuildNumber, err = NextBuildNumber(ctx, client, app.ID)
		if err != nil {
			return nil, fmt.Errorf("choose build number: %w", err)
		}
		logf(opts.Log, "Build number %s (next after the builds in App Store Connect for %s)", res.BuildNumber, app.Name)
	} else {
		logf(opts.Log, "Build number %s (--build-number)", res.BuildNumber)
	}

	bo := opts.Build
	bo.OutputDir, bo.BuildNumber, bo.Unsigned = outputDir, buildNumberInput(res.BuildNumber, opts.Version), false
	built, err := builder.Build(ctx, &bo)
	if err != nil {
		return res, err
	}
	res.BuildID, res.IPAPath, res.WorkflowURL = built.BuildID, built.IPAPath, built.WorkflowURL

	info, err := ipa.ReadInfo(built.IPAPath)
	if err != nil {
		return res, err
	}
	if info.BundleID != bundleID {
		return res, fmt.Errorf("%s was built for bundle ID %s, not %s; check --bundle-id or ios.bundleId", built.IPAPath, info.BundleID, bundleID)
	}
	// The runner stamps CURRENT_PROJECT_VERSION and rewrites a hardcoded
	// Info.plist; anything it missed would be rejected by App Store Connect
	// as a duplicate, so refuse here with the reason instead.
	if info.BuildNumber != res.BuildNumber {
		return res, fmt.Errorf("the runner did not apply build number %s: %s has CFBundleVersion %q. Check the Build IPA log for the 'Build number' line; a workflow file predating the build_number input needs builder init and a push", res.BuildNumber, built.IPAPath, info.BuildNumber)
	}
	if opts.Version != "" && info.Version != opts.Version {
		return res, fmt.Errorf("the runner did not apply version %s: %s has CFBundleShortVersionString %q", opts.Version, built.IPAPath, info.Version)
	}
	res.Version = info.Version

	// The build had its own deadline; give App Store Connect the same budget.
	timeout := opts.Build.Timeout
	if timeout <= 0 {
		timeout = build.DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	up, err := distribute.Upload(ctx, client, &distribute.UploadOptions{IPAPath: built.IPAPath, Wait: true, NoEncryption: opts.NoEncryption, PollInterval: opts.PollInterval, Log: opts.Log})
	if up != nil && up.Build != nil {
		res.ASCBuildID, res.Link = up.Build.ID, up.Build.Link
	}
	if err != nil {
		return res, err
	}

	if opts.AppStore {
		sub, err := distribute.SubmitAppStore(ctx, client, &distribute.AppStoreOptions{
			BundleID: bundleID, Version: info.Version, BuildNumber: res.BuildNumber, ReleaseType: opts.ReleaseType, NoEncryption: opts.NoEncryption, Log: opts.Log,
		})
		if sub != nil {
			res.Link = sub.Link
		}
		return res, err
	}
	tf, err := distribute.SubmitTestFlight(ctx, client, &distribute.TestFlightOptions{
		BundleID: bundleID, Version: info.Version, BuildNumber: res.BuildNumber, Groups: opts.Groups, Notes: opts.Notes,
		NoEncryption: opts.NoEncryption, PollInterval: opts.PollInterval, Log: opts.Log,
	})
	if tf != nil {
		res.Groups, res.Link = tf.Groups, tf.Link
	}
	return res, err
}

// buildNumberInput encodes the build number and marketing version the way the
// runner's apply_build_number reads them: "N", or "X.Y.Z+N" when a version is
// given (the pubspec convention).
func buildNumberInput(buildNumber, version string) string {
	if version == "" {
		return buildNumber
	}
	return version + "+" + buildNumber
}

// resolveBundleID picks the flag, then builder.json, then the newest IPA.
func resolveBundleID(flag, configured, outputDir string) (string, error) {
	if flag != "" {
		return flag, nil
	}
	if configured != "" {
		return configured, nil
	}
	path, err := ipa.Newest(outputDir)
	if err != nil {
		return "", fmt.Errorf("cannot tell which app to release: pass --bundle-id or set ios.bundleId in builder.json (%v)", err)
	}
	info, err := ipa.ReadInfo(path)
	if err != nil {
		return "", fmt.Errorf("cannot tell which app to release: pass --bundle-id or set ios.bundleId in builder.json (%v)", err)
	}
	return info.BundleID, nil
}

// NextBuildNumber returns one more than the highest CFBundleVersion App Store
// Connect holds for the app, across every marketing version, since a second
// upload is rejected unless its number is higher. The first build is 1.
func NextBuildNumber(ctx context.Context, client *asc.Client, appID string) (string, error) {
	builds, err := client.ListBuilds(ctx, &asc.BuildFilter{AppID: appID, Platform: asc.PlatformIOS})
	if err != nil {
		return "", err
	}
	numbers := make([]string, 0, len(builds))
	for i := range builds {
		numbers = append(numbers, builds[i].BuildNumber)
	}
	return nextBuildNumber(numbers), nil
}

// nextBuildNumber increments the last component of the largest build number;
// entries that are not period-separated integers are ignored.
func nextBuildNumber(existing []string) string {
	var best []int
	for _, v := range existing {
		parts, ok := parseBuildNumber(v)
		if ok && (best == nil || compareParts(parts, best) > 0) {
			best = parts
		}
	}
	if best == nil {
		return "1"
	}
	best[len(best)-1]++
	strs := make([]string, len(best))
	for i, n := range best {
		strs[i] = strconv.Itoa(n)
	}
	return strings.Join(strs, ".")
}

func parseBuildNumber(v string) ([]int, bool) {
	fields := strings.Split(v, ".")
	parts := make([]int, 0, len(fields))
	for _, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil || n < 0 {
			return nil, false
		}
		parts = append(parts, n)
	}
	return parts, true
}

// compareParts orders component-wise, treating missing components as 0.
func compareParts(a, b []int) int {
	for i := 0; i < len(a) || i < len(b); i++ {
		var x, y int
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			if x > y {
				return 1
			}
			return -1
		}
	}
	return 0
}

func logf(w io.Writer, format string, args ...any) {
	if w != nil {
		fmt.Fprintf(w, format+"\n", args...)
	}
}
