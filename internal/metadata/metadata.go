// Package metadata syncs an app's App Store listing with files laid out like
// fastlane deliver's: metadata/<locale>/<field>.txt, metadata/*_category.txt
// and screenshots/<locale>/. Pull writes what App Store Connect has; Plan
// diffs the files against it and Apply writes the differences.
package metadata

import (
	"context"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/MobAI-App/ios-builder/internal/asc"
)

// Options configures Pull and Plan.
type Options struct {
	BundleID string
	// Version is the App Store version (marketing version) to work on. Empty
	// picks the editable one; pull falls back to the newest. Plan creates a
	// version that does not exist yet.
	Version string
	// MetadataDir defaults to "metadata", ScreenshotsDir to "screenshots".
	MetadataDir    string
	ScreenshotsDir string
	// Screenshots includes screenshots (download on pull, upload on push).
	Screenshots bool
	// ReplaceScreenshots (push) deletes a set's screenshots and uploads the
	// local ones whenever the two differ; without it only missing ones are added.
	ReplaceScreenshots bool
	// Clean (pull) removes local field files and, with Screenshots, images
	// App Store Connect does not have.
	Clean bool
	// PollInterval spaces screenshot processing polls (default 2s).
	PollInterval time.Duration
	Log          io.Writer
}

func (o *Options) metadataDir() string {
	if o.MetadataDir == "" {
		return "metadata"
	}
	return o.MetadataDir
}

func (o *Options) screenshotsDir() string {
	if o.ScreenshotsDir == "" {
		return "screenshots"
	}
	return o.ScreenshotsDir
}

func (o *Options) pollInterval() time.Duration {
	if o.PollInterval <= 0 {
		return 2 * time.Second
	}
	return o.PollInterval
}

// AppRef identifies the app in results.
type AppRef struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	BundleID string `json:"bundle_id"`
}

// VersionRef describes the App Store version worked on.
type VersionRef struct {
	ID            string `json:"id,omitempty"`
	VersionString string `json:"version_string"`
	State         string `json:"state,omitempty"`
	Created       bool   `json:"created,omitempty"`
}

func versionRef(v *asc.AppStoreVersion) VersionRef {
	state := v.State
	if state == "" {
		state = v.AppStoreState
	}
	return VersionRef{ID: v.ID, VersionString: v.VersionString, State: state}
}

func logf(w io.Writer, format string, args ...any) {
	if w != nil {
		fmt.Fprintf(w, format+"\n", args...)
	}
}

// target is the App Store Connect side of a sync.
type target struct {
	app *asc.App
	// version is nil when Options.Version does not exist yet (push creates it).
	version *asc.AppStoreVersion
	// base is what a version that does not exist yet is compared against:
	// the newest version, whose localizations App Store Connect copies.
	base *asc.AppStoreVersion
	info *asc.AppInfo
}

// resolveTarget finds the version and app info to work on. push requires
// them to be editable; pull reads whatever is newest.
func resolveTarget(ctx context.Context, client *asc.Client, app *asc.App, version string, push bool) (*target, error) {
	versions, err := client.ListAppStoreVersions(ctx, app.ID, asc.PlatformIOS, "")
	if err != nil {
		return nil, err
	}
	sort.SliceStable(versions, func(i, j int) bool { return versions[i].CreatedDate.After(versions[j].CreatedDate) })
	t := &target{app: app}
	if len(versions) > 0 {
		t.base = &versions[0]
	}
	switch {
	case version != "":
		for i := range versions {
			if versions[i].VersionString == version {
				t.version = &versions[i]
			}
		}
		if t.version == nil && !push {
			return nil, fmt.Errorf("%s has no App Store version %s", app.Name, version)
		}
		if t.version != nil && push && !t.version.Editable() {
			return nil, fmt.Errorf("version %s is %s and cannot be edited; pass --version with a new version number to create one", version, versionRef(t.version).State)
		}
	default:
		for i := range versions {
			if versions[i].Editable() {
				t.version = &versions[i]
				break
			}
		}
		if t.version == nil {
			if push || t.base == nil {
				return nil, fmt.Errorf("%s has no App Store version being prepared; pass --version X.Y to create one", app.Name)
			}
			t.version = t.base
		}
	}

	infos, err := client.ListAppInfos(ctx, app.ID)
	if err != nil {
		return nil, err
	}
	if len(infos) == 0 {
		return nil, fmt.Errorf("%s has no app info record", app.Name)
	}
	t.info = &infos[0]
	for i := range infos {
		if infos[i].Editable() {
			t.info = &infos[i]
			break
		}
	}
	return t, nil
}

// byLocale indexes localizations by locale.
func byLocale(locs []asc.Localization) map[string]asc.Localization {
	m := make(map[string]asc.Localization, len(locs))
	for _, l := range locs {
		m[l.Locale] = l
	}
	return m
}
