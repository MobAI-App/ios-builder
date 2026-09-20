// Package asc exposes the App Store Connect API client to code outside this
// module.
//
// The implementation lives in internal/asc. Types are aliases, so values pass
// between this package, pkg/distribute, pkg/release and pkg/signing without
// conversion; every method of Client is available on the alias.
package asc

import (
	"time"

	"github.com/MobAI-App/ios-builder/internal/asc"
)

// DefaultBaseURL is the production App Store Connect API endpoint.
const DefaultBaseURL = asc.DefaultBaseURL

type (
	Client      = asc.Client
	Credentials = asc.Credentials
	Option      = asc.Option
	Error       = asc.Error
	ErrorDetail = asc.ErrorDetail
	ErrorSource = asc.ErrorSource

	App                     = asc.App
	AppStoreVersion         = asc.AppStoreVersion
	AppStoreVersionUpdate   = asc.AppStoreVersionUpdate
	BetaAppReviewSubmission = asc.BetaAppReviewSubmission
	BetaBuildLocalization   = asc.BetaBuildLocalization
	BetaGroup               = asc.BetaGroup
	BetaGroupSpec           = asc.BetaGroupSpec
	BetaTester              = asc.BetaTester
	BetaTesterFilter        = asc.BetaTesterFilter
	BetaTesterSpec          = asc.BetaTesterSpec
	Build                   = asc.Build
	BuildFilter             = asc.BuildFilter
	BuildUpload             = asc.BuildUpload
	BuildUploadFile         = asc.BuildUploadFile
	BundleID                = asc.BundleID
	Certificate             = asc.Certificate
	Device                  = asc.Device
	Profile                 = asc.Profile
	ReviewSubmission        = asc.ReviewSubmission
	ReviewSubmissionItem    = asc.ReviewSubmissionItem
	StateDetail             = asc.StateDetail
	UploadBuildOptions      = asc.UploadBuildOptions
	UploadFailedError       = asc.UploadFailedError
	UploadOperation         = asc.UploadOperation
	User                    = asc.User
	UserInvitation          = asc.UserInvitation
	UserInvitationSpec      = asc.UserInvitationSpec
)

const (
	PlatformIOS = asc.PlatformIOS

	ProcessingStateProcessing = asc.ProcessingStateProcessing
	ProcessingStateFailed     = asc.ProcessingStateFailed
	ProcessingStateInvalid    = asc.ProcessingStateInvalid
	ProcessingStateValid      = asc.ProcessingStateValid

	UploadStateAwaitingUpload = asc.UploadStateAwaitingUpload
	UploadStateProcessing     = asc.UploadStateProcessing
	UploadStateComplete       = asc.UploadStateComplete
	UploadStateFailed         = asc.UploadStateFailed

	BetaReviewWaiting  = asc.BetaReviewWaiting
	BetaReviewInReview = asc.BetaReviewInReview
	BetaReviewApproved = asc.BetaReviewApproved
	BetaReviewRejected = asc.BetaReviewRejected

	BetaTesterNotInvited = asc.BetaTesterNotInvited
	BetaTesterInvited    = asc.BetaTesterInvited
	BetaTesterAccepted   = asc.BetaTesterAccepted
	BetaTesterInstalled  = asc.BetaTesterInstalled
	BetaTesterRevoked    = asc.BetaTesterRevoked

	ReleaseTypeManual        = asc.ReleaseTypeManual
	ReleaseTypeAfterApproval = asc.ReleaseTypeAfterApproval
	ReleaseTypeScheduled     = asc.ReleaseTypeScheduled

	ReviewStateReadyForReview   = asc.ReviewStateReadyForReview
	ReviewStateWaitingForReview = asc.ReviewStateWaitingForReview
	ReviewStateInReview         = asc.ReviewStateInReview
	ReviewStateUnresolvedIssues = asc.ReviewStateUnresolvedIssues
	ReviewStateCanceling        = asc.ReviewStateCanceling
	ReviewStateCompleting       = asc.ReviewStateCompleting
	ReviewStateComplete         = asc.ReviewStateComplete

	VersionStateWaitingForReview = asc.VersionStateWaitingForReview
	VersionStateInReview         = asc.VersionStateInReview

	RoleCustomerSupport     = asc.RoleCustomerSupport
	CodeNoInstallableBuilds = asc.CodeNoInstallableBuilds
)

// NewClient validates the credentials and returns a client. No network call
// is made.
func NewClient(creds Credentials, opts ...Option) (*Client, error) {
	return asc.NewClient(creds, opts...)
}

// WithBaseURL points the client at another server, e.g. a test server.
func WithBaseURL(baseURL string) Option { return asc.WithBaseURL(baseURL) }

// WithRetryDelay sets the base delay of the exponential backoff on 429/5xx.
func WithRetryDelay(d time.Duration) Option { return asc.WithRetryDelay(d) }

// MatchBetaGroup returns the group called name (case-insensitive), nil when
// there is none, and an error when the name is ambiguous.
func MatchBetaGroup(groups []BetaGroup, name string) (*BetaGroup, error) {
	return asc.MatchBetaGroup(groups, name)
}

// HasCode reports whether err is an App Store Connect error carrying code.
func HasCode(err error, code string) bool { return asc.HasCode(err, code) }

// IsStatus reports whether err is an App Store Connect error with the HTTP status.
func IsStatus(err error, status int) bool { return asc.IsStatus(err, status) }
