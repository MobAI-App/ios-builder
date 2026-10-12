package metadata

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/MobAI-App/ios-builder/internal/asc"
)

// Field is one localized text field and its fastlane deliver file name.
type Field struct {
	// File is the name under metadata/<locale>/ without ".txt".
	File string
	// Attr is the App Store Connect attribute.
	Attr string
	// AppInfo: an appInfoLocalizations field rather than an
	// appStoreVersionLocalizations one.
	AppInfo bool
	// Limit is the maximum length in characters; 0 for none.
	Limit int
	URL   bool
}

// Fields lists every localized field, app info first (the order writes happen in).
var Fields = []Field{
	{File: "name", Attr: asc.AttrName, AppInfo: true, Limit: 30},
	{File: "subtitle", Attr: asc.AttrSubtitle, AppInfo: true, Limit: 30},
	{File: "privacy_url", Attr: asc.AttrPrivacyPolicyURL, AppInfo: true, URL: true},
	{File: "description", Attr: asc.AttrDescription, Limit: 4000},
	{File: "keywords", Attr: asc.AttrKeywords, Limit: 100},
	{File: "release_notes", Attr: asc.AttrWhatsNew, Limit: 4000},
	{File: "promotional_text", Attr: asc.AttrPromotionalText, Limit: 170},
	{File: "marketing_url", Attr: asc.AttrMarketingURL, URL: true},
	{File: "support_url", Attr: asc.AttrSupportURL, URL: true},
}

// Category files at the top of the metadata directory.
const (
	PrimaryCategoryFile   = "primary_category"
	SecondaryCategoryFile = "secondary_category"
)

// Categories are App Store Connect's appCategories IDs.
var Categories = []string{
	"BOOKS", "BUSINESS", "DEVELOPER_TOOLS", "EDUCATION", "ENTERTAINMENT", "FINANCE", "FOOD_AND_DRINK",
	"GAMES", "GRAPHICS_AND_DESIGN", "HEALTH_AND_FITNESS", "LIFESTYLE", "MAGAZINES_AND_NEWSPAPERS",
	"MEDICAL", "MUSIC", "NAVIGATION", "NEWS", "PHOTO_AND_VIDEO", "PRODUCTIVITY", "REFERENCE",
	"SHOPPING", "SOCIAL_NETWORKING", "SPORTS", "STICKERS", "TRAVEL", "UTILITIES", "WEATHER",
}

// Local is the metadata directory as read from disk. Absent files are
// absent from the maps: push leaves those fields alone.
type Local struct {
	// Locales maps locale → Field.File → value.
	Locales map[string]map[string]string
	// Primary and Secondary are the category files, nil when absent.
	Primary, Secondary *string
}

// skippedDirs are fastlane deliver directories that are not locales.
var skippedDirs = map[string]bool{"default": true, "review_information": true, "trade_representative_contact_information": true}

// normalize makes file and App Store Connect text comparable: LF line
// endings, no surrounding whitespace.
func normalize(s string) string {
	return strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
}

func readText(path string) (*string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s := normalize(string(data))
	return &s, nil
}

// LoadLocal reads the metadata directory. A missing directory is empty.
func LoadLocal(dir string) (*Local, error) {
	l := &Local{Locales: map[string]map[string]string{}}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return l, nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() || skippedDirs[e.Name()] || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		values := map[string]string{}
		for _, f := range Fields {
			v, err := readText(filepath.Join(dir, e.Name(), f.File+".txt"))
			if err != nil {
				return nil, err
			}
			if v != nil {
				values[f.File] = *v
			}
		}
		if len(values) > 0 {
			l.Locales[e.Name()] = values
		}
	}
	if l.Primary, err = readCategory(filepath.Join(dir, PrimaryCategoryFile+".txt")); err != nil {
		return nil, err
	}
	if l.Secondary, err = readCategory(filepath.Join(dir, SecondaryCategoryFile+".txt")); err != nil {
		return nil, err
	}
	return l, nil
}

func readCategory(path string) (*string, error) {
	v, err := readText(path)
	if v == nil || err != nil {
		return v, err
	}
	c := NormalizeCategory(*v)
	return &c, nil
}

// NormalizeCategory accepts App Store Connect IDs (PHOTO_AND_VIDEO) and the
// legacy names older fastlane setups wrote (MZGenre.SocialNetworking).
func NormalizeCategory(s string) string {
	s = strings.TrimSpace(s)
	if rest, ok := strings.CutPrefix(s, "MZGenre."); ok {
		var b strings.Builder
		for i, r := range rest {
			if i > 0 && unicode.IsUpper(r) {
				b.WriteByte('_')
			}
			b.WriteRune(r)
		}
		s = b.String()
	}
	return strings.ToUpper(s)
}

// LocaleNames returns the locales in a stable order.
func (l *Local) LocaleNames() []string {
	names := make([]string, 0, len(l.Locales))
	for name := range l.Locales {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Validate checks every value against App Store Connect's limits so a push
// fails before its first write. All problems are reported together.
func (l *Local) Validate() error {
	var problems []string
	for _, locale := range l.LocaleNames() {
		values := l.Locales[locale]
		for _, f := range Fields {
			v, ok := values[f.File]
			if !ok {
				continue
			}
			if n := utf8.RuneCountInString(v); f.Limit > 0 && n > f.Limit {
				problems = append(problems, fmt.Sprintf("%s/%s.txt is %d characters, the limit is %d", locale, f.File, n, f.Limit))
			}
			if f.URL && v != "" {
				if u, err := url.Parse(v); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
					problems = append(problems, fmt.Sprintf("%s/%s.txt is not an http(s) URL: %q", locale, f.File, v))
				}
			}
		}
	}
	for _, c := range []struct {
		file  string
		value *string
	}{{PrimaryCategoryFile, l.Primary}, {SecondaryCategoryFile, l.Secondary}} {
		if c.value == nil {
			continue
		}
		if *c.value == "" {
			if c.file == PrimaryCategoryFile {
				problems = append(problems, PrimaryCategoryFile+".txt is empty; the primary category cannot be removed")
			}
			continue
		}
		if !knownCategory(*c.value) {
			problems = append(problems, fmt.Sprintf("%s.txt: unknown category %s (one of %s)", c.file, *c.value, strings.Join(Categories, ", ")))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("metadata is not valid:\n  %s", strings.Join(problems, "\n  "))
	}
	return nil
}

func knownCategory(id string) bool {
	for _, c := range Categories {
		if c == id {
			return true
		}
	}
	return false
}
