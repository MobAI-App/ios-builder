// Package release exposes `builder ios release` to code outside this module:
// next build number from App Store Connect, build, verify the IPA, upload,
// submit. The build itself is whatever Builder the caller supplies, so a
// program with its own build backend can release through it.
package release

import (
	"context"
	"io"

	"github.com/MobAI-App/ios-builder/internal/release"
	"github.com/MobAI-App/ios-builder/pkg/asc"
	"github.com/MobAI-App/ios-builder/pkg/config"
)

type (
	Builder = release.Builder
	Options = release.Options
	Result  = release.Result
)

// Preflight returns the profile to release with: the selected one when it
// has distribution store, else the only store profile in builder.json.
func Preflight(cfg *config.Config, profile string, log io.Writer) (string, error) {
	return release.Preflight(cfg, profile, log)
}

// Run builds, uploads and submits. A partial Result comes back with the
// error so callers can show how far it got.
func Run(ctx context.Context, cfg *config.Config, builder Builder, client *asc.Client, opts *Options) (*Result, error) {
	return release.Run(ctx, cfg, builder, client, opts)
}

// NextBuildNumber is one above the highest CFBundleVersion App Store Connect
// holds for the app across every marketing version (1 when none).
func NextBuildNumber(ctx context.Context, client *asc.Client, appID string) (string, error) {
	return release.NextBuildNumber(ctx, client, appID)
}
