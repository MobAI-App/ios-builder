package distribute

import (
	"context"
	"io"
	"time"

	"github.com/MobAI-App/ios-builder/internal/asc"
	"github.com/MobAI-App/ios-builder/internal/ipa"
)

// InternalGroupOptions configures ToInternalGroup.
type InternalGroupOptions struct {
	IPAPath string
	// Group is the internal TestFlight group; a missing one is created.
	Group        string
	Notes        string
	NoEncryption bool
	PollInterval time.Duration
	Log          io.Writer
}

// InternalGroupResult is what ToInternalGroup reports.
type InternalGroupResult struct {
	Upload     *UploadResult     `json:"upload"`
	TestFlight *TestFlightResult `json:"testflight,omitempty"`
}

// ToInternalGroup uploads an App Store signed IPA, waits until App Store
// Connect has processed it and adds it to an internal TestFlight group, whose
// testers install it from the TestFlight app without beta review. The group
// is checked before the upload, so an external one costs nothing.
func ToInternalGroup(ctx context.Context, client *asc.Client, opts *InternalGroupOptions) (*InternalGroupResult, error) {
	info, err := ipa.ReadInfo(opts.IPAPath)
	if err != nil {
		return nil, err
	}
	app, err := client.AppByBundleID(ctx, info.BundleID)
	if err != nil {
		return nil, err
	}
	groups, err := client.ListBetaGroups(ctx, app.ID)
	if err != nil {
		return nil, err
	}
	if err := refuseExternal(groups, opts.Group); err != nil {
		return nil, err
	}
	res := &InternalGroupResult{}
	res.Upload, err = Upload(ctx, client, &UploadOptions{IPAPath: opts.IPAPath, Wait: true, NoEncryption: opts.NoEncryption, PollInterval: opts.PollInterval, Log: opts.Log})
	if err != nil {
		return res, err
	}
	res.TestFlight, err = SubmitTestFlight(ctx, client, &TestFlightOptions{
		BundleID: info.BundleID, Version: info.Version, BuildNumber: info.BuildNumber, Groups: []string{opts.Group}, Internal: true,
		Notes: opts.Notes, NoEncryption: opts.NoEncryption, PollInterval: opts.PollInterval, Log: opts.Log,
	})
	return res, err
}
