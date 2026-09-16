package distribute

import (
	"context"
	"fmt"
	"io"

	"github.com/MobAI-App/ios-builder/internal/asc"
)

// AppStoreOptions configures SubmitAppStore.
type AppStoreOptions struct {
	BundleID string
	// Version is the marketing version to submit (CFBundleShortVersionString).
	Version string
	// BuildNumber narrows the build; empty picks the newest VALID build of Version.
	BuildNumber string
	// ReleaseType is asc.ReleaseTypeManual or asc.ReleaseTypeAfterApproval; empty leaves it as is.
	ReleaseType string
	// NoEncryption answers export compliance with "no" when still unanswered.
	NoEncryption bool
	Log          io.Writer
}

// VersionRef describes the App Store version.
type VersionRef struct {
	ID            string `json:"id"`
	VersionString string `json:"version_string"`
	State         string `json:"state"`
	ReleaseType   string `json:"release_type,omitempty"`
	Created       bool   `json:"created"`
}

// AppStoreResult is what SubmitAppStore reports.
type AppStoreResult struct {
	App        AppRef     `json:"app"`
	Build      BuildRef   `json:"build"`
	Compliance string     `json:"encryption_compliance"`
	Version    VersionRef `json:"version"`
	Submission ReviewRef  `json:"submission"`
	Link       string     `json:"link"`
}

// SubmitAppStore attaches a build to the App Store version and submits it for review.
func SubmitAppStore(ctx context.Context, client *asc.Client, opts AppStoreOptions) (*AppStoreResult, error) {
	if opts.Version == "" {
		return nil, fmt.Errorf("a marketing version is required (--version, or --ipa to read it from the archive)")
	}
	app, err := client.AppByBundleID(ctx, opts.BundleID)
	if err != nil {
		return nil, err
	}
	build, err := pickBuild(ctx, client, app.ID, opts.Version, opts.BuildNumber)
	if err != nil {
		return nil, err
	}
	res := &AppStoreResult{App: appRef(app), Build: buildRef(app.ID, opts.Version, build), Link: distributionLink(app.ID)}
	logf(opts.Log, "Using build %s (%s) for version %s", build.BuildNumber, build.ID, opts.Version)

	res.Compliance, err = setCompliance(ctx, client, opts.Log, build, opts.NoEncryption)
	if err != nil {
		return res, err
	}
	res.Build.UsesNonExemptEncryption = build.UsesNonExemptEncryption

	versions, err := client.ListAppStoreVersions(ctx, app.ID, asc.PlatformIOS, opts.Version)
	if err != nil {
		return res, err
	}
	var version *asc.AppStoreVersion
	if len(versions) > 0 {
		version = &versions[0]
		logf(opts.Log, "App Store version %s exists (%s)", version.VersionString, version.State)
	} else {
		logf(opts.Log, "Creating App Store version %s...", opts.Version)
		version, err = client.CreateAppStoreVersion(ctx, app.ID, asc.PlatformIOS, opts.Version)
		if err != nil {
			return res, stateErrorHint(err, "create App Store version")
		}
		res.Version.Created = true
	}
	res.Version.ID, res.Version.VersionString, res.Version.State, res.Version.ReleaseType = version.ID, version.VersionString, version.State, version.ReleaseType
	switch version.State {
	case asc.ReviewStateWaitingForReview, asc.ReviewStateInReview:
		return res, fmt.Errorf("version %s is already %s; cancel that submission in App Store Connect before submitting another build", version.VersionString, version.State)
	}

	update := asc.AppStoreVersionUpdate{ReleaseType: opts.ReleaseType}
	if version.BuildID != build.ID {
		update.BuildID = build.ID
	}
	if update.BuildID != "" || update.ReleaseType != "" {
		version, err = client.UpdateAppStoreVersion(ctx, version.ID, update)
		if err != nil {
			return res, stateErrorHint(err, "attach build to version")
		}
		res.Version.State, res.Version.ReleaseType = version.State, version.ReleaseType
		logf(opts.Log, "Attached build %s to version %s (release: %s)", build.BuildNumber, version.VersionString, version.ReleaseType)
	}

	// Reuse an open submission: App Store Connect allows one per platform.
	subs, err := client.ListReviewSubmissions(ctx, app.ID, asc.PlatformIOS, []string{asc.ReviewStateReadyForReview, asc.ReviewStateUnresolvedIssues})
	if err != nil {
		return res, err
	}
	var sub *asc.ReviewSubmission
	if len(subs) > 0 {
		sub = &subs[0]
		logf(opts.Log, "Using open review submission %s (%s)", sub.ID, sub.State)
	} else {
		sub, err = client.CreateReviewSubmission(ctx, app.ID, asc.PlatformIOS)
		if err != nil {
			return res, stateErrorHint(err, "create review submission")
		}
	}
	res.Submission = ReviewRef{ID: sub.ID, State: sub.State}

	items, err := client.ListReviewSubmissionItems(ctx, sub.ID)
	if err != nil {
		return res, err
	}
	hasVersion := false
	for _, item := range items {
		if item.AppStoreVersionID == version.ID {
			hasVersion = true
		}
	}
	if !hasVersion {
		if _, err := client.AddAppStoreVersionToReviewSubmission(ctx, sub.ID, version.ID); err != nil {
			return res, stateErrorHint(err, "add version to review submission")
		}
	}

	logf(opts.Log, "Submitting version %s for App Review...", version.VersionString)
	submitted, err := client.SubmitReviewSubmission(ctx, sub.ID)
	if err != nil {
		return res, stateErrorHint(err, "submit for review")
	}
	res.Submission.State = submitted.State
	logf(opts.Log, "Submitted: %s (%s)", res.Link, submitted.State)
	return res, nil
}
