// Package distribute drives the App Store Connect flows behind
// `builder ios upload` and `builder ios submit`: deliver an IPA, wait for
// processing, hand a build to TestFlight groups, and submit an App Store
// version for review. It only orchestrates; every API call lives in asc.
package distribute

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/MobAI-App/ios-builder/internal/asc"
)

// AppRef identifies the App Store Connect app in results.
type AppRef struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	BundleID string `json:"bundle_id"`
}

// BuildRef describes a build in results.
type BuildRef struct {
	ID                      string `json:"id"`
	Version                 string `json:"version,omitempty"`
	BuildNumber             string `json:"build_number"`
	ProcessingState         string `json:"processing_state"`
	UsesNonExemptEncryption *bool  `json:"uses_non_exempt_encryption"`
	Link                    string `json:"link"`
}

func appRef(a *asc.App) AppRef {
	return AppRef{ID: a.ID, Name: a.Name, BundleID: a.BundleID}
}

func buildRef(appID, version string, b *asc.Build) BuildRef {
	return BuildRef{
		ID:                      b.ID,
		Version:                 version,
		BuildNumber:             b.BuildNumber,
		ProcessingState:         b.ProcessingState,
		UsesNonExemptEncryption: b.UsesNonExemptEncryption,
		Link:                    buildLink(appID, b.ID),
	}
}

func testflightLink(appID string) string {
	return "https://appstoreconnect.apple.com/apps/" + appID + "/testflight/ios"
}

func buildLink(appID, buildID string) string {
	return testflightLink(appID) + "/" + buildID
}

func distributionLink(appID string) string {
	return "https://appstoreconnect.apple.com/apps/" + appID + "/distribution"
}

func logf(w io.Writer, format string, args ...any) {
	if w != nil {
		fmt.Fprintf(w, format+"\n", args...)
	}
}

func pollInterval(d time.Duration) time.Duration {
	if d <= 0 {
		return 15 * time.Second
	}
	return d
}

// pickBuild returns the newest VALID, unexpired build matching the filters.
func pickBuild(ctx context.Context, client *asc.Client, appID, version, buildNumber string) (*asc.Build, error) {
	f := &asc.BuildFilter{AppID: appID, Platform: asc.PlatformIOS, Version: version, BuildNumber: buildNumber, ProcessingState: asc.ProcessingStateValid, ExcludeExpired: true, Limit: 1}
	builds, err := client.ListBuilds(ctx, f)
	if err != nil {
		return nil, err
	}
	if len(builds) > 0 {
		return &builds[0], nil
	}
	// Explain why rather than just "not found": the build may still be processing.
	f.ProcessingState, f.ExcludeExpired = "", false
	matches, err := client.ListBuilds(ctx, f)
	if err != nil {
		return nil, err
	}
	what := "no build"
	if buildNumber != "" {
		what = "build " + buildNumber
	}
	if version != "" {
		what += " of version " + version
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("%s is available in App Store Connect; upload one with builder ios upload --wait", what)
	}
	b := matches[0]
	if b.Expired {
		return nil, fmt.Errorf("%s (%s) has expired; upload a new build", what, b.ID)
	}
	return nil, fmt.Errorf("%s (%s) is %s; wait for processing to finish (builder ios upload --wait) and retry", what, b.ID, b.ProcessingState)
}

// setCompliance answers the export compliance question with "no non-exempt
// encryption" when the caller asked for it and the build is still unanswered.
func setCompliance(ctx context.Context, client *asc.Client, log io.Writer, build *asc.Build, exempt bool) (string, error) {
	switch {
	case build.UsesNonExemptEncryption != nil:
		return "already_set", nil
	case !exempt:
		return "pending", nil
	}
	logf(log, "Setting export compliance: no non-exempt encryption")
	updated, err := client.SetUsesNonExemptEncryption(ctx, build.ID, false)
	if err != nil {
		return "", fmt.Errorf("set export compliance: %w", err)
	}
	build.UsesNonExemptEncryption = updated.UsesNonExemptEncryption
	return "set_exempt", nil
}

// stateErrorHint rephrases App Store Connect's 409 state conflicts, which are
// nearly always incomplete metadata or a version in the wrong state.
func stateErrorHint(err error, what string) error {
	var e *asc.Error
	if errors.As(err, &e) && (e.StatusCode == 409 || e.StatusCode == 422) {
		return fmt.Errorf("%s: %w. App Store Connect needs the version's metadata complete before review (description, screenshots, age rating, pricing, privacy); finish it in App Store Connect or with asc-cli (https://github.com/tddworks/asc-cli), then rerun", what, err)
	}
	return fmt.Errorf("%s: %w", what, err)
}

func joinDetails(details []asc.StateDetail) []string {
	out := make([]string, 0, len(details))
	for _, d := range details {
		out = append(out, d.String())
	}
	return out
}
