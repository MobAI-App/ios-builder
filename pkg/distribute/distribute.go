// Package distribute exposes the App Store Connect flows behind `builder ios
// upload` and `builder ios submit` to code outside this module: deliver an
// IPA, wait for processing, hand a build to TestFlight groups, submit an App
// Store version for review, and manage testers.
package distribute

import (
	"context"
	"io"

	"github.com/MobAI-App/ios-builder/internal/distribute"
	"github.com/MobAI-App/ios-builder/pkg/asc"
)

type (
	AppRef   = distribute.AppRef
	BuildRef = distribute.BuildRef

	UploadOptions = distribute.UploadOptions
	IPARef        = distribute.IPARef
	UploadRef     = distribute.UploadRef
	UploadResult  = distribute.UploadResult

	TestFlightOptions = distribute.TestFlightOptions
	GroupRef          = distribute.GroupRef
	ReviewRef         = distribute.ReviewRef
	TestFlightResult  = distribute.TestFlightResult

	AppStoreOptions = distribute.AppStoreOptions
	VersionRef      = distribute.VersionRef
	AppStoreResult  = distribute.AppStoreResult

	TesterOptions = distribute.TesterOptions
	TesterResult  = distribute.TesterResult
)

const (
	TesterInvited           = distribute.TesterInvited
	TesterAdded             = distribute.TesterAdded
	TesterTeamInviteSent    = distribute.TesterTeamInviteSent
	TesterTeamInvitePending = distribute.TesterTeamInvitePending
)

// Upload delivers an IPA to App Store Connect and, with Wait, follows
// processing and answers export compliance.
func Upload(ctx context.Context, client *asc.Client, opts *UploadOptions) (*UploadResult, error) {
	return distribute.Upload(ctx, client, opts)
}

// SubmitTestFlight hands a processed build to TestFlight groups, creating
// missing ones, and submits it for beta review when a group is external.
func SubmitTestFlight(ctx context.Context, client *asc.Client, opts *TestFlightOptions) (*TestFlightResult, error) {
	return distribute.SubmitTestFlight(ctx, client, opts)
}

// SubmitAppStore attaches a processed build to the App Store version for
// its marketing version and submits it for review.
func SubmitAppStore(ctx context.Context, client *asc.Client, opts *AppStoreOptions) (*AppStoreResult, error) {
	return distribute.SubmitAppStore(ctx, client, opts)
}

// AddTester puts one tester into a TestFlight group, inviting them to the
// team first when the group is internal and they are not a member.
func AddTester(ctx context.Context, client *asc.Client, opts *TesterOptions) (*TesterResult, error) {
	return distribute.AddTester(ctx, client, opts)
}

// InviteTester sends (or resends) the TestFlight invitation email.
func InviteTester(ctx context.Context, client *asc.Client, log io.Writer, appID string, tester *asc.BetaTester) (*asc.BetaTester, error) {
	return distribute.InviteTester(ctx, client, log, appID, tester)
}
