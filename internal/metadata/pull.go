package metadata

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/MobAI-App/ios-builder/internal/asc"
)

// PullResult is what Pull reports. Paths are as written, under the
// metadata and screenshots directories.
type PullResult struct {
	App     AppRef     `json:"app"`
	Version VersionRef `json:"version"`
	Locales []string   `json:"locales"`
	Written []string   `json:"written"`
	// Unchanged counts files that already held App Store Connect's value.
	Unchanged   int      `json:"unchanged"`
	Removed     []string `json:"removed"`
	Screenshots []string `json:"screenshots"`
	Warnings    []string `json:"warnings,omitempty"`
}

// Pull writes the App Store listing into the metadata (and, with
// Screenshots, screenshots) directory. Empty fields get no file. Local files
// App Store Connect has no value for are kept unless Clean is set.
func Pull(ctx context.Context, client *asc.Client, opts *Options) (*PullResult, error) {
	app, err := client.AppByBundleID(ctx, opts.BundleID)
	if err != nil {
		return nil, err
	}
	t, err := resolveTarget(ctx, client, app, opts.Version, false)
	if err != nil {
		return nil, err
	}
	res := &PullResult{App: AppRef{ID: app.ID, Name: app.Name, BundleID: app.BundleID}, Version: versionRef(t.version), Locales: []string{}, Written: []string{}, Removed: []string{}, Screenshots: []string{}}
	logf(opts.Log, "Pulling %s version %s (%s)", app.Name, res.Version.VersionString, res.Version.State)
	infoLocs, err := client.ListAppInfoLocalizations(ctx, t.info.ID)
	if err != nil {
		return nil, err
	}
	versionLocs, err := client.ListVersionLocalizations(ctx, t.version.ID)
	if err != nil {
		return res, err
	}
	info, version := byLocale(infoLocs), byLocale(versionLocs)
	locales := map[string]bool{}
	for l := range info {
		locales[l] = true
	}
	for l := range version {
		locales[l] = true
	}
	res.Locales = sortedKeys(locales)

	dir := opts.metadataDir()
	keep := map[string]bool{}
	write := func(path, value string) error {
		keep[path] = true
		if existing, err := os.ReadFile(path); err == nil && normalize(string(existing)) == value {
			res.Unchanged++
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(value+"\n"), 0o644); err != nil {
			return err
		}
		res.Written = append(res.Written, path)
		return nil
	}
	for _, locale := range res.Locales {
		for _, f := range Fields {
			src := version[locale]
			if f.AppInfo {
				src = info[locale]
			}
			if v := normalize(src.Fields[f.Attr]); v != "" {
				if err := write(filepath.Join(dir, locale, f.File+".txt"), v); err != nil {
					return res, err
				}
			}
		}
	}
	for file, id := range map[string]string{PrimaryCategoryFile: t.info.PrimaryCategoryID, SecondaryCategoryFile: t.info.SecondaryCategoryID} {
		if id != "" {
			if err := write(filepath.Join(dir, file+".txt"), id); err != nil {
				return res, err
			}
		}
	}

	if opts.Screenshots {
		if err := pullScreenshots(ctx, client, opts, versionLocs, keep, res); err != nil {
			return res, err
		}
	}
	if opts.Clean {
		if err := clean(opts, keep, res); err != nil {
			return res, err
		}
	}
	sort.Strings(res.Written)
	return res, nil
}

var orderPrefix = regexp.MustCompile(`^\d+_`)

// screenshotName is the local name of the i-th screenshot: an order prefix
// (replacing one the uploaded name already had) and the uploaded file name.
func screenshotName(i int, fileName string) string {
	base := filepath.Base(strings.ReplaceAll(fileName, "\\", "/"))
	if base == "." || base == "/" || base == "" {
		base = "screenshot.png"
	}
	return fmt.Sprintf("%02d_%s", i+1, orderPrefix.ReplaceAllString(base, ""))
}

// pullScreenshots downloads every set into screenshots/<locale>/<DISPLAY_TYPE>/,
// skipping files whose MD5 already matches.
func pullScreenshots(ctx context.Context, client *asc.Client, opts *Options, locs []asc.Localization, keep map[string]bool, res *PullResult) error {
	for _, loc := range locs {
		sets, err := client.ListAppScreenshotSets(ctx, loc.ID)
		if err != nil {
			return err
		}
		for _, set := range sets {
			shots, err := client.ListAppScreenshots(ctx, set.ID)
			if err != nil {
				return err
			}
			for i := range shots {
				s := &shots[i]
				path := filepath.Join(opts.screenshotsDir(), loc.Locale, set.DisplayType, screenshotName(i, s.FileName))
				keep[path] = true
				if s.TemplateURL == "" {
					res.Warnings = append(res.Warnings, fmt.Sprintf("%s %s %s is still processing (%s); not downloaded", loc.Locale, set.DisplayType, s.FileName, s.State))
					continue
				}
				if sum, err := asc.FileMD5(path); err == nil && strings.EqualFold(sum, s.Checksum) {
					res.Unchanged++
					continue
				}
				if err := download(ctx, client, s.ImageURL(), path); err != nil {
					return err
				}
				res.Screenshots = append(res.Screenshots, path)
			}
		}
	}
	return nil
}

// download writes to a temporary file first so an interrupted pull leaves
// no truncated image behind.
func download(ctx context.Context, client *asc.Client, url, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".download-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := client.Download(ctx, url, tmp); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// clean removes the field, category and (with Screenshots) image files that
// Pull did not just write or confirm.
func clean(opts *Options, keep map[string]bool, res *PullResult) error {
	remove := func(path string) error {
		if keep[path] {
			return nil
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		res.Removed = append(res.Removed, path)
		return nil
	}
	dir := opts.metadataDir()
	for _, file := range []string{PrimaryCategoryFile, SecondaryCategoryFile} {
		if path := filepath.Join(dir, file+".txt"); fileExists(path) {
			if err := remove(path); err != nil {
				return err
			}
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() || skippedDirs[e.Name()] || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		for _, f := range Fields {
			if path := filepath.Join(dir, e.Name(), f.File+".txt"); fileExists(path) {
				if err := remove(path); err != nil {
					return err
				}
			}
		}
	}
	if !opts.Screenshots {
		return nil
	}
	err = filepath.WalkDir(opts.screenshotsDir(), func(path string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil || d.IsDir() || !isImage(d.Name()) {
			return err
		}
		return remove(path)
	})
	sort.Strings(res.Removed)
	return err
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}
