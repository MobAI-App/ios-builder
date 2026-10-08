package metadata

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/MobAI-App/ios-builder/internal/asc"
)

// Change kinds.
const (
	ChangeCreateVersion = "create_version"
	ChangeCreateLocale  = "create_locale"
	ChangeUpdate        = "update"
	ChangeCategory      = "category"
	ChangeScreenshots   = "screenshots"
)

// Change is one line of the plan.
type Change struct {
	Kind   string `json:"kind"`
	Locale string `json:"locale,omitempty"`
	// Field is the fastlane file name (description, keywords, ...), the
	// category file for a category change, and "app_info" or "version" for
	// a new locale.
	Field       string   `json:"field,omitempty"`
	From        string   `json:"from,omitempty"`
	To          string   `json:"to,omitempty"`
	DisplayType string   `json:"display_type,omitempty"`
	Upload      []string `json:"upload,omitempty"`
	Delete      int      `json:"delete,omitempty"`
}

// longFields are summarized by length rather than printed in full.
var longFields = map[string]bool{"description": true, "release_notes": true, "promotional_text": true}

func quote(s string) string {
	if s == "" {
		return "(empty)"
	}
	return fmt.Sprintf("%q", s)
}

// String renders the change as a "Will ..." line.
func (c *Change) String() string {
	switch c.Kind {
	case ChangeCreateVersion:
		return "Will create App Store version " + c.To
	case ChangeCreateLocale:
		if c.Field == "app_info" {
			return "Will add locale " + c.Locale + " to the app info (name, subtitle, privacy URL)"
		}
		return "Will add locale " + c.Locale + " to the version"
	case ChangeUpdate:
		if longFields[c.Field] {
			return fmt.Sprintf("Will update %s %s (%d → %d characters)", c.Locale, c.Field, utf8.RuneCountInString(c.From), utf8.RuneCountInString(c.To))
		}
		return fmt.Sprintf("Will update %s %s: %s → %s", c.Locale, c.Field, quote(c.From), quote(c.To))
	case ChangeCategory:
		return fmt.Sprintf("Will set %s: %s → %s", strings.ReplaceAll(c.Field, "_", " "), quote(c.From), quote(c.To))
	case ChangeScreenshots:
		var parts []string
		if c.Delete > 0 {
			parts = append(parts, fmt.Sprintf("delete %d", c.Delete))
		}
		if len(c.Upload) > 0 {
			parts = append(parts, fmt.Sprintf("upload %d", len(c.Upload)))
		}
		noun := "screenshots"
		if c.Delete+len(c.Upload) == 1 {
			noun = "screenshot"
		}
		return fmt.Sprintf("Will %s %s in %s %s", strings.Join(parts, " and "), noun, c.Locale, c.DisplayType)
	}
	return c.Kind
}

type locWrite struct {
	appInfo bool
	locale  string
	id      string // empty: create
	fields  map[string]string
}

type shotWrite struct {
	locale      string
	displayType string
	setID       string // empty: create
	deleteIDs   []string
	upload      []string
}

// Plan is what a push would change. Build it with NewPlan, print Changes,
// then Apply.
type Plan struct {
	App      AppRef     `json:"app"`
	Version  VersionRef `json:"version"`
	Changes  []Change   `json:"changes"`
	Warnings []string   `json:"warnings,omitempty"`
	Applied  bool       `json:"applied"`

	client        *asc.Client
	opts          *Options
	local         *Local
	shots         LocalScreenshots
	target        *target
	createVersion string
	writes        []locWrite
	primary       *string
	secondary     *string
	shotWrites    []shotWrite
	versionLocIDs map[string]string
}

// Empty reports whether the plan changes nothing.
func (p *Plan) Empty() bool { return len(p.Changes) == 0 }

// NewPlan reads and validates the local files, then diffs them against App
// Store Connect. Nothing is written.
func NewPlan(ctx context.Context, client *asc.Client, opts *Options) (*Plan, error) {
	local, err := LoadLocal(opts.metadataDir())
	if err != nil {
		return nil, err
	}
	if err := local.Validate(); err != nil {
		return nil, err
	}
	p := &Plan{client: client, opts: opts, local: local, Changes: []Change{}}
	if opts.Screenshots {
		if p.shots, p.Warnings, err = LoadScreenshots(opts.screenshotsDir()); err != nil {
			return nil, err
		}
	}
	if len(local.Locales) == 0 && local.Primary == nil && local.Secondary == nil && len(p.shots) == 0 {
		return nil, fmt.Errorf("nothing to push: %s has no <locale>/<field>.txt files", opts.metadataDir())
	}
	app, err := client.AppByBundleID(ctx, opts.BundleID)
	if err != nil {
		return nil, err
	}
	p.App = AppRef{ID: app.ID, Name: app.Name, BundleID: app.BundleID}
	t, err := resolveTarget(ctx, client, app, opts.Version, true)
	if err != nil {
		return nil, err
	}
	if err := p.compute(ctx, t); err != nil {
		return nil, err
	}
	return p, nil
}

// compute fills Changes and the writes from the target's current values.
func (p *Plan) compute(ctx context.Context, t *target) error {
	p.target, p.writes, p.shotWrites, p.primary, p.secondary = t, nil, nil, nil, nil
	p.Changes = p.Changes[:0]
	p.versionLocIDs = map[string]string{}
	versionForDiff := t.version
	if t.version != nil {
		p.Version = versionRef(t.version)
	} else {
		p.createVersion = p.opts.Version
		p.Version = VersionRef{VersionString: p.opts.Version}
		p.Changes = append(p.Changes, Change{Kind: ChangeCreateVersion, To: p.opts.Version})
		versionForDiff = t.base
	}

	infoLocs, err := p.client.ListAppInfoLocalizations(ctx, t.info.ID)
	if err != nil {
		return err
	}
	var versionLocs []asc.Localization
	if versionForDiff != nil {
		if versionLocs, err = p.client.ListVersionLocalizations(ctx, versionForDiff.ID); err != nil {
			return err
		}
	}
	if t.version != nil {
		for _, l := range versionLocs {
			p.versionLocIDs[l.Locale] = l.ID
		}
	}
	infoChanged := false
	for _, appInfo := range []bool{true, false} {
		remote := byLocale(versionLocs)
		if appInfo {
			remote = byLocale(infoLocs)
		}
		for _, locale := range p.local.LocaleNames() {
			w, changes, err := diffLocale(locale, appInfo, p.local.Locales[locale], remote)
			if err != nil {
				return err
			}
			if w == nil {
				continue
			}
			if t.version == nil && !appInfo {
				w.id = "" // the new version's localizations get new IDs; Apply re-plans
			}
			infoChanged = infoChanged || appInfo
			p.writes = append(p.writes, *w)
			p.Changes = append(p.Changes, changes...)
		}
	}

	for _, c := range []struct {
		file   string
		local  *string
		remote string
		dst    **string
	}{{PrimaryCategoryFile, p.local.Primary, t.info.PrimaryCategoryID, &p.primary}, {SecondaryCategoryFile, p.local.Secondary, t.info.SecondaryCategoryID, &p.secondary}} {
		if c.local != nil && *c.local != c.remote {
			*c.dst = c.local
			infoChanged = true
			p.Changes = append(p.Changes, Change{Kind: ChangeCategory, Field: c.file, From: c.remote, To: *c.local})
		}
	}
	if infoChanged && t.version != nil && !t.info.Editable() {
		return fmt.Errorf("the app info (name, subtitle, privacy URL, categories) is %s and cannot be edited; it opens again with a new App Store version (--version X.Y)", t.info.State)
	}

	if len(p.shots) > 0 {
		return p.computeScreenshots(ctx, t.version != nil)
	}
	return nil
}

// diffLocale compares one locale's files with App Store Connect. A nil
// write means nothing differs.
func diffLocale(locale string, appInfo bool, values map[string]string, remote map[string]asc.Localization) (*locWrite, []Change, error) {
	existing, exists := remote[locale]
	w := &locWrite{appInfo: appInfo, locale: locale, id: existing.ID, fields: map[string]string{}}
	var changes []Change
	for _, f := range Fields {
		v, ok := values[f.File]
		if f.AppInfo != appInfo || !ok {
			continue
		}
		from := normalize(existing.Fields[f.Attr])
		if exists && v == from {
			continue
		}
		if !exists && v == "" {
			continue
		}
		w.fields[f.Attr] = v
		changes = append(changes, Change{Kind: ChangeUpdate, Locale: locale, Field: f.File, From: from, To: v})
	}
	if len(w.fields) == 0 {
		return nil, nil, nil
	}
	if !exists {
		if appInfo && w.fields[asc.AttrName] == "" {
			return nil, nil, fmt.Errorf("locale %s is new to the app info, which needs a name: add %s/name.txt", locale, locale)
		}
		kind := "version"
		if appInfo {
			kind = "app_info"
		}
		changes = append([]Change{{Kind: ChangeCreateLocale, Locale: locale, Field: kind}}, changes...)
	}
	return w, changes, nil
}

// computeScreenshots diffs each local set with App Store Connect by MD5. A
// version that does not exist yet has no sets, so everything is uploaded.
func (p *Plan) computeScreenshots(ctx context.Context, versionExists bool) error {
	for _, locale := range sortedKeys(p.shots) {
		sets := map[string]asc.AppScreenshotSet{}
		if id := p.versionLocIDs[locale]; id != "" && versionExists {
			remoteSets, err := p.client.ListAppScreenshotSets(ctx, id)
			if err != nil {
				return err
			}
			for _, s := range remoteSets {
				sets[s.DisplayType] = s
			}
		}
		for _, displayType := range sortedKeys(p.shots[locale]) {
			paths := p.shots[locale][displayType]
			var remote []asc.AppScreenshot
			set, hasSet := sets[displayType]
			if hasSet {
				var err error
				if remote, err = p.client.ListAppScreenshots(ctx, set.ID); err != nil {
					return err
				}
			}
			w, err := p.diffSet(locale, displayType, set.ID, paths, remote)
			if err != nil {
				return err
			}
			if w == nil {
				continue
			}
			p.shotWrites = append(p.shotWrites, *w)
			ch := Change{Kind: ChangeScreenshots, Locale: locale, DisplayType: displayType, Delete: len(w.deleteIDs)}
			for _, path := range w.upload {
				ch.Upload = append(ch.Upload, filepath.Base(path))
			}
			p.Changes = append(p.Changes, ch)
		}
	}
	return nil
}

func (p *Plan) diffSet(locale, displayType, setID string, paths []string, remote []asc.AppScreenshot) (*shotWrite, error) {
	sums := make([]string, len(paths))
	for i, path := range paths {
		sum, err := asc.FileMD5(path)
		if err != nil {
			return nil, err
		}
		sums[i] = sum
	}
	w := &shotWrite{locale: locale, displayType: displayType, setID: setID}
	if p.opts.ReplaceScreenshots {
		same := len(sums) == len(remote)
		for i := 0; same && i < len(sums); i++ {
			same = strings.EqualFold(sums[i], remote[i].Checksum)
		}
		if same {
			return nil, nil
		}
		for i := range remote {
			w.deleteIDs = append(w.deleteIDs, remote[i].ID)
		}
		w.upload = paths
		return w, nil
	}
	have := map[string]int{}
	for i := range remote {
		have[strings.ToLower(remote[i].Checksum)]++
	}
	for i, sum := range sums {
		if have[sum] > 0 {
			have[sum]--
			continue
		}
		w.upload = append(w.upload, paths[i])
	}
	if len(w.upload) == 0 {
		return nil, nil
	}
	if n := len(remote) + len(w.upload); n > MaxScreenshotsPerSet {
		return nil, fmt.Errorf("%s %s would have %d screenshots (%d in App Store Connect + %d new), the limit is %d; pass --replace-screenshots to replace the set", locale, displayType, n, len(remote), len(w.upload), MaxScreenshotsPerSet)
	}
	return w, nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Apply writes the plan: the version (when new, after which the plan is
// recomputed against it), app info locales, version locales, categories,
// then screenshots. It stops at the first error.
func (p *Plan) Apply(ctx context.Context) error {
	log := p.opts.Log
	if p.createVersion != "" {
		logf(log, "Creating App Store version %s...", p.createVersion)
		v, err := p.client.CreateAppStoreVersion(ctx, p.target.app.ID, asc.PlatformIOS, p.createVersion)
		if err != nil {
			return fmt.Errorf("create App Store version %s: %w", p.createVersion, err)
		}
		// The new version starts with copies of the previous one's
		// localizations, and App Store Connect opens an editable app info.
		t, err := resolveTarget(ctx, p.client, p.target.app, v.VersionString, true)
		if err != nil {
			return err
		}
		p.createVersion = ""
		if err := p.compute(ctx, t); err != nil {
			return err
		}
		p.Version.Created = true
		p.Changes = append([]Change{{Kind: ChangeCreateVersion, To: v.VersionString}}, p.Changes...)
	}
	for i := range p.writes {
		if err := p.applyLocale(ctx, &p.writes[i]); err != nil {
			return err
		}
	}
	if p.primary != nil || p.secondary != nil {
		logf(log, "Updating categories...")
		if err := p.client.UpdateAppInfoCategories(ctx, p.target.info.ID, p.primary, p.secondary); err != nil {
			return fmt.Errorf("update categories: %w", err)
		}
	}
	if err := p.applyScreenshots(ctx); err != nil {
		return err
	}
	p.Applied = true
	return nil
}

func (p *Plan) applyLocale(ctx context.Context, w *locWrite) error {
	what := "version"
	if w.appInfo {
		what = "app info"
	}
	logf(p.opts.Log, "Updating %s %s...", w.locale, what)
	var err error
	switch {
	case w.appInfo && w.id == "":
		_, err = p.client.CreateAppInfoLocalization(ctx, p.target.info.ID, w.locale, w.fields)
	case w.appInfo:
		err = p.client.UpdateAppInfoLocalization(ctx, w.id, w.fields)
	case w.id == "":
		var l *asc.Localization
		if l, err = p.client.CreateVersionLocalization(ctx, p.target.version.ID, w.locale, w.fields); err == nil {
			p.versionLocIDs[w.locale] = l.ID
		}
	default:
		err = p.client.UpdateVersionLocalization(ctx, w.id, w.fields)
	}
	if err != nil {
		return fmt.Errorf("update %s %s: %w", w.locale, what, err)
	}
	return nil
}

func (p *Plan) applyScreenshots(ctx context.Context) error {
	var uploaded []*asc.AppScreenshot
	for _, w := range p.shotWrites {
		locID := p.versionLocIDs[w.locale]
		if locID == "" {
			logf(p.opts.Log, "Adding locale %s to the version for its screenshots...", w.locale)
			l, err := p.client.CreateVersionLocalization(ctx, p.target.version.ID, w.locale, nil)
			if err != nil {
				return fmt.Errorf("add locale %s: %w", w.locale, err)
			}
			locID = l.ID
			p.versionLocIDs[w.locale] = locID
		}
		setID := w.setID
		if setID == "" {
			set, err := p.client.CreateAppScreenshotSet(ctx, locID, w.displayType)
			if err != nil {
				return fmt.Errorf("create %s %s screenshot set: %w", w.locale, w.displayType, err)
			}
			setID = set.ID
		}
		for _, id := range w.deleteIDs {
			if err := p.client.DeleteAppScreenshot(ctx, id); err != nil {
				return fmt.Errorf("delete %s %s screenshot %s: %w", w.locale, w.displayType, id, err)
			}
		}
		for _, path := range w.upload {
			logf(p.opts.Log, "Uploading %s (%s %s)...", filepath.Base(path), w.locale, w.displayType)
			shot, err := p.client.UploadScreenshot(ctx, setID, path)
			if err != nil {
				return err
			}
			uploaded = append(uploaded, shot)
		}
	}
	if len(uploaded) > 0 {
		logf(p.opts.Log, "Waiting for App Store Connect to process %d screenshots...", len(uploaded))
	}
	for _, shot := range uploaded {
		if _, err := p.client.WaitForScreenshot(ctx, shot.ID, p.opts.pollInterval()); err != nil {
			return err
		}
	}
	return nil
}
