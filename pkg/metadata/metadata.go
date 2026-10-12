// Package metadata exposes the App Store listing sync behind `builder ios
// metadata pull` and `push` to code outside this module: fastlane
// deliver-style files (metadata/<locale>/<field>.txt, category files,
// screenshots/<locale>/) against an app's editable App Store version.
package metadata

import (
	"context"

	"github.com/MobAI-App/ios-builder/internal/metadata"
	"github.com/MobAI-App/ios-builder/pkg/asc"
)

type (
	Options          = metadata.Options
	AppRef           = metadata.AppRef
	VersionRef       = metadata.VersionRef
	Field            = metadata.Field
	Local            = metadata.Local
	LocalScreenshots = metadata.LocalScreenshots
	Plan             = metadata.Plan
	Change           = metadata.Change
	PullResult       = metadata.PullResult
)

const (
	ChangeCreateVersion = metadata.ChangeCreateVersion
	ChangeCreateLocale  = metadata.ChangeCreateLocale
	ChangeUpdate        = metadata.ChangeUpdate
	ChangeCategory      = metadata.ChangeCategory
	ChangeScreenshots   = metadata.ChangeScreenshots

	MaxScreenshotsPerSet = metadata.MaxScreenshotsPerSet
)

// Fields lists every localized field with its file name, attribute and limit.
var Fields = metadata.Fields

// Pull writes the App Store listing into the metadata (and screenshots) directory.
func Pull(ctx context.Context, client *asc.Client, opts *Options) (*PullResult, error) {
	return metadata.Pull(ctx, client, opts)
}

// NewPlan validates the local files and diffs them against App Store
// Connect without writing; Plan.Apply writes the differences.
func NewPlan(ctx context.Context, client *asc.Client, opts *Options) (*Plan, error) {
	return metadata.NewPlan(ctx, client, opts)
}

// LoadLocal reads a metadata directory.
func LoadLocal(dir string) (*Local, error) { return metadata.LoadLocal(dir) }

// LoadScreenshots reads a screenshots directory, inferring display types
// from pixel sizes.
func LoadScreenshots(dir string) (LocalScreenshots, []string, error) {
	return metadata.LoadScreenshots(dir)
}

// DisplayTypeForSize infers a screenshot display type from its pixel size.
func DisplayTypeForSize(width, height int) string { return metadata.DisplayTypeForSize(width, height) }
