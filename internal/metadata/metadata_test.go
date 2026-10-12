package metadata

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// pngBytes is a w×h PNG; seed varies one pixel so checksums differ.
func pngBytes(t *testing.T, w, h int, seed uint8) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, w, h))
	img.SetGray(0, 0, color.Gray{Y: seed})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func changeLines(p *Plan) []string {
	lines := make([]string, 0, len(p.Changes))
	for i := range p.Changes {
		lines = append(lines, p.Changes[i].String())
	}
	return lines
}

func TestValidateRejectsBeforeAnyRequest(t *testing.T) {
	f := newFakeASC(t)
	f.seed()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "en-US", "keywords.txt"), strings.Repeat("k", 101))
	writeFile(t, filepath.Join(dir, "en-US", "subtitle.txt"), "This subtitle is far too long for the store")
	writeFile(t, filepath.Join(dir, "en-US", "name.txt"), "Ünïcödé counts as characters") // 28 runes, more bytes
	writeFile(t, filepath.Join(dir, "en-US", "support_url.txt"), "example.com/support")
	writeFile(t, filepath.Join(dir, "en-US", "promotional_text.txt"), strings.Repeat("p", 171))
	writeFile(t, filepath.Join(dir, "primary_category.txt"), "GAMEZ")

	_, err := NewPlan(ctx(), f.client(), &Options{BundleID: "com.example.app", MetadataDir: dir})
	if err == nil {
		t.Fatal("expected a validation error")
	}
	for _, want := range []string{"en-US/keywords.txt is 101 characters, the limit is 100", "en-US/subtitle.txt is 43 characters, the limit is 30", "en-US/promotional_text.txt is 171", "en-US/support_url.txt is not an http(s) URL", "unknown category GAMEZ"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
	if strings.Contains(err.Error(), "name.txt") {
		t.Errorf("name is within the limit in characters: %v", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("requests before validation passed: %v", f.calls)
	}
}

func TestPlanDiffsAndApplyWritesOnlyChanges(t *testing.T) {
	f := newFakeASC(t)
	f.seed()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "en-US", "name.txt"), "My App\n")
	writeFile(t, filepath.Join(dir, "en-US", "subtitle.txt"), "New subtitle\n")
	writeFile(t, filepath.Join(dir, "en-US", "description.txt"), "New description\r\nline two\n")
	writeFile(t, filepath.Join(dir, "en-US", "keywords.txt"), "a,b")
	writeFile(t, filepath.Join(dir, "de-DE", "name.txt"), "Meine App")
	writeFile(t, filepath.Join(dir, "de-DE", "description.txt"), "Beschreibung")
	writeFile(t, filepath.Join(dir, "primary_category.txt"), "MZGenre.Productivity")
	writeFile(t, filepath.Join(dir, "secondary_category.txt"), "")
	writeFile(t, filepath.Join(dir, "review_information", "notes.txt"), "ignored")

	opts := &Options{BundleID: "com.example.app", MetadataDir: dir}
	plan, err := NewPlan(ctx(), f.client(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if w := f.writes(); len(w) != 0 {
		t.Fatalf("planning wrote: %v", w)
	}
	want := []string{
		"Will add locale de-DE to the app info (name, subtitle, privacy URL)",
		`Will update de-DE name: (empty) → "Meine App"`,
		`Will update en-US subtitle: "Old subtitle" → "New subtitle"`,
		"Will add locale de-DE to the version",
		"Will update de-DE description (0 → 12 characters)",
		"Will update en-US description (15 → 24 characters)",
		`Will set primary category: "GAMES" → "PRODUCTIVITY"`,
	}
	if got := changeLines(plan); !reflect.DeepEqual(got, want) {
		t.Errorf("plan:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if plan.Version.VersionString != "1.1" || plan.Version.ID != "v-1" {
		t.Errorf("version = %+v", plan.Version)
	}

	if err := plan.Apply(ctx()); err != nil {
		t.Fatal(err)
	}
	wantWrites := []string{
		"POST /v1/appInfoLocalizations",
		"PATCH /v1/appInfoLocalizations/il-en",
		"POST /v1/appStoreVersionLocalizations",
		"PATCH /v1/appStoreVersionLocalizations/vl-en",
		"PATCH /v1/appInfos/ai-1",
	}
	if got := f.writes(); !reflect.DeepEqual(got, wantWrites) {
		t.Errorf("writes = %v", got)
	}
	if a := obj(t, f.body("PATCH /v1/appInfoLocalizations/il-en"), "data", "attributes"); !reflect.DeepEqual(a, map[string]any{"subtitle": "New subtitle"}) {
		t.Errorf("app info patch = %v", a)
	}
	if a := obj(t, f.body("PATCH /v1/appStoreVersionLocalizations/vl-en"), "data", "attributes"); !reflect.DeepEqual(a, map[string]any{"description": "New description\nline two"}) {
		t.Errorf("version patch = %v", a)
	}
	created := f.body("POST /v1/appStoreVersionLocalizations")
	if a := obj(t, created, "data", "attributes"); a["locale"] != "de-DE" || a["description"] != "Beschreibung" {
		t.Errorf("new locale = %v", a)
	}
	if obj(t, created, "data", "relationships", "appStoreVersion", "data")["id"] != "v-1" {
		t.Errorf("new locale relationships = %v", created)
	}
	if obj(t, f.body("PATCH /v1/appInfos/ai-1"), "data", "relationships", "primaryCategory", "data")["id"] != "PRODUCTIVITY" {
		t.Errorf("category patch = %v", f.body("PATCH /v1/appInfos/ai-1"))
	}
	// secondary_category.txt is empty and App Store Connect has none: no change.
	if _, has := obj(t, f.body("PATCH /v1/appInfos/ai-1"), "data", "relationships")["secondaryCategory"]; has {
		t.Error("secondary category must not be sent")
	}

	again, err := NewPlan(ctx(), f.client(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Empty() {
		t.Errorf("second plan is not empty: %v", changeLines(again))
	}
}

func TestPlanNeedsNameForNewAppInfoLocale(t *testing.T) {
	f := newFakeASC(t)
	f.seed()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "fr-FR", "subtitle.txt"), "Sous-titre")
	_, err := NewPlan(ctx(), f.client(), &Options{BundleID: "com.example.app", MetadataDir: dir})
	if err == nil || !strings.Contains(err.Error(), "fr-FR/name.txt") {
		t.Errorf("err = %v", err)
	}
}

func TestPushCreatesVersionAndReplans(t *testing.T) {
	f := newFakeASC(t)
	f.seed()
	f.find("appStoreVersions", "v-1").attrs["appVersionState"] = "READY_FOR_DISTRIBUTION"
	f.find("appInfos", "ai-1").attrs["state"] = "READY_FOR_DISTRIBUTION"
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "en-US", "description.txt"), "Old description")
	writeFile(t, filepath.Join(dir, "en-US", "release_notes.txt"), "Bug fixes")
	writeFile(t, filepath.Join(dir, "en-US", "subtitle.txt"), "New subtitle")

	_, err := NewPlan(ctx(), f.client(), &Options{BundleID: "com.example.app", MetadataDir: dir})
	if err == nil || !strings.Contains(err.Error(), "--version") {
		t.Fatalf("without --version: %v", err)
	}
	plan, err := NewPlan(ctx(), f.client(), &Options{BundleID: "com.example.app", MetadataDir: dir, Version: "1.2"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"Will create App Store version 1.2",
		`Will update en-US subtitle: "Old subtitle" → "New subtitle"`,
		"Will update en-US release_notes (0 → 9 characters)",
	}
	if got := changeLines(plan); !reflect.DeepEqual(got, want) {
		t.Errorf("plan = %q", got)
	}
	if err := plan.Apply(ctx()); err != nil {
		t.Fatal(err)
	}
	if !plan.Version.Created || plan.Version.VersionString != "1.2" || plan.Version.ID == "v-1" {
		t.Errorf("version = %+v", plan.Version)
	}
	writes := f.writes()
	if len(writes) != 3 || writes[0] != "POST /v1/appStoreVersions" || !strings.HasPrefix(writes[1], "PATCH /v1/appInfoLocalizations/") || !strings.HasPrefix(writes[2], "PATCH /v1/appStoreVersionLocalizations/") {
		t.Fatalf("writes = %v", writes)
	}
	// The copied localization is patched, not the old version's.
	if writes[2] == "PATCH /v1/appStoreVersionLocalizations/vl-en" || writes[1] == "PATCH /v1/appInfoLocalizations/il-en" {
		t.Errorf("patched the live version: %v", writes)
	}
	if a := obj(t, f.body(writes[2]), "data", "attributes"); !reflect.DeepEqual(a, map[string]any{"whatsNew": "Bug fixes"}) {
		t.Errorf("version patch = %v", a)
	}
}

func TestPushRefusesLockedVersion(t *testing.T) {
	f := newFakeASC(t)
	f.seed()
	f.find("appStoreVersions", "v-1").attrs["appVersionState"] = "WAITING_FOR_REVIEW"
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "en-US", "keywords.txt"), "x")
	_, err := NewPlan(ctx(), f.client(), &Options{BundleID: "com.example.app", MetadataDir: dir, Version: "1.1"})
	if err == nil || !strings.Contains(err.Error(), "WAITING_FOR_REVIEW") {
		t.Errorf("err = %v", err)
	}
}

func TestPushScreenshots(t *testing.T) {
	f := newFakeASC(t)
	f.seed()
	metaDir, shotDir := t.TempDir(), t.TempDir()
	first, second := pngBytes(t, 1290, 2796, 1), pngBytes(t, 2796, 1290, 2) // landscape still APP_IPHONE_67
	writeFile(t, filepath.Join(shotDir, "en-US", "01_home.png"), string(first))
	writeFile(t, filepath.Join(shotDir, "en-US", "02_list.png"), string(second))
	writeFile(t, filepath.Join(shotDir, "en-US", "APP_IPAD_PRO_129", "ipad.png"), string(pngBytes(t, 2048, 2732, 3)))
	writeFile(t, filepath.Join(shotDir, "en-US", "iMessage", "x.png"), string(first))
	writeFile(t, filepath.Join(shotDir, "ja", "home.png"), string(first))
	writeFile(t, filepath.Join(metaDir, "ja", "name.txt"), "マイアプリ")

	opts := &Options{BundleID: "com.example.app", MetadataDir: metaDir, ScreenshotsDir: shotDir, Screenshots: true, PollInterval: 1}
	plan, err := NewPlan(ctx(), f.client(), opts)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"Will add locale ja to the app info (name, subtitle, privacy URL)",
		`Will update ja name: (empty) → "マイアプリ"`,
		"Will upload 1 screenshot in en-US APP_IPAD_PRO_129",
		"Will upload 2 screenshots in en-US APP_IPHONE_67",
		"Will upload 1 screenshot in ja APP_IPHONE_67",
	}
	if got := changeLines(plan); !reflect.DeepEqual(got, want) {
		t.Errorf("plan:\n%s", strings.Join(got, "\n"))
	}
	if len(plan.Warnings) != 1 || !strings.Contains(plan.Warnings[0], "iMessage") {
		t.Errorf("warnings = %v", plan.Warnings)
	}
	if err := plan.Apply(ctx()); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	var sets []string
	for _, r := range f.items {
		if r.typ == "appScreenshotSets" {
			sets = append(sets, r.parent+" "+fmt.Sprint(r.attrs["screenshotDisplayType"]))
		}
	}
	jaLoc := ""
	for _, r := range f.items {
		if r.typ == "appStoreVersionLocalizations" && r.attrs["locale"] == "ja" {
			jaLoc = r.id
		}
	}
	iphoneSet := ""
	for _, r := range f.items {
		if r.typ == "appScreenshotSets" && r.parent == "vl-en" && r.attrs["screenshotDisplayType"] == "APP_IPHONE_67" {
			iphoneSet = r.id
		}
	}
	shots := f.children("appScreenshots", iphoneSet)
	f.mu.Unlock()
	if jaLoc == "" {
		t.Fatal("ja was not added to the version for its screenshots")
	}
	if !reflect.DeepEqual(sets, []string{"vl-en APP_IPAD_PRO_129", "vl-en APP_IPHONE_67", jaLoc + " APP_IPHONE_67"}) {
		t.Errorf("sets = %v", sets)
	}
	if len(shots) != 2 || shots[0].attrs["fileName"] != "01_home.png" || shots[1].attrs["fileName"] != "02_list.png" {
		t.Fatalf("screenshots = %+v", shots)
	}
	if !bytes.Equal(f.blobs[shots[0].id], first) || fmt.Sprint(shots[0].attrs["assetDeliveryState"]) != "map[state:COMPLETE]" {
		t.Errorf("first screenshot not uploaded/committed: %+v", shots[0].attrs)
	}

	// Same files again: nothing to do.
	again, err := NewPlan(ctx(), f.client(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Empty() {
		t.Errorf("second plan = %v", changeLines(again))
	}

	// A changed file is appended without --replace-screenshots...
	writeFile(t, filepath.Join(shotDir, "en-US", "02_list.png"), string(pngBytes(t, 1290, 2796, 9)))
	appendPlan, err := NewPlan(ctx(), f.client(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if got := changeLines(appendPlan); !reflect.DeepEqual(got, []string{"Will upload 1 screenshot in en-US APP_IPHONE_67"}) {
		t.Errorf("append plan = %v", got)
	}
	// ...and replaces the set with it.
	replace := *opts
	replace.ReplaceScreenshots = true
	replacePlan, err := NewPlan(ctx(), f.client(), &replace)
	if err != nil {
		t.Fatal(err)
	}
	if got := changeLines(replacePlan); !reflect.DeepEqual(got, []string{"Will delete 2 and upload 2 screenshots in en-US APP_IPHONE_67"}) {
		t.Errorf("replace plan = %v", got)
	}
	if err := replacePlan.Apply(ctx()); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	n := len(f.children("appScreenshots", iphoneSet))
	f.mu.Unlock()
	if n != 2 {
		t.Errorf("set has %d screenshots after replace", n)
	}
}

func TestScreenshotSetLimit(t *testing.T) {
	f := newFakeASC(t)
	f.seed()
	set := f.add("appScreenshotSets", "set-1", "vl-en", map[string]any{"screenshotDisplayType": "APP_IPHONE_67"})
	for i := range 9 {
		f.addScreenshot(set.id, "old.png", []byte{byte(i)})
	}
	shotDir := t.TempDir()
	writeFile(t, filepath.Join(shotDir, "en-US", "a.png"), string(pngBytes(t, 1290, 2796, 1)))
	writeFile(t, filepath.Join(shotDir, "en-US", "b.png"), string(pngBytes(t, 1290, 2796, 2)))
	_, err := NewPlan(ctx(), f.client(), &Options{BundleID: "com.example.app", MetadataDir: t.TempDir(), ScreenshotsDir: shotDir, Screenshots: true})
	if err == nil || !strings.Contains(err.Error(), "would have 11 screenshots") {
		t.Errorf("err = %v", err)
	}
}

func TestLoadScreenshotsUnknownSize(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "en-US", "odd.png"), string(pngBytes(t, 100, 200, 0)))
	_, _, err := LoadScreenshots(dir)
	if err == nil || !strings.Contains(err.Error(), "100x200") || !strings.Contains(err.Error(), "<DISPLAY_TYPE>") {
		t.Errorf("err = %v", err)
	}
}

func TestDisplayTypeForSize(t *testing.T) {
	for _, tc := range []struct {
		w, h int
		want string
	}{
		{1320, 2868, "APP_IPHONE_67"}, {2796, 1290, "APP_IPHONE_67"}, {1242, 2688, "APP_IPHONE_65"},
		{1179, 2556, "APP_IPHONE_61"}, {1242, 2208, "APP_IPHONE_55"}, {2048, 2732, "APP_IPAD_PRO_3GEN_129"},
		{1668, 2388, "APP_IPAD_PRO_3GEN_11"}, {2880, 1800, "APP_DESKTOP"}, {100, 100, ""},
	} {
		if got := DisplayTypeForSize(tc.w, tc.h); got != tc.want {
			t.Errorf("%dx%d = %q, want %q", tc.w, tc.h, got, tc.want)
		}
	}
}

func TestNormalizeCategory(t *testing.T) {
	for in, want := range map[string]string{"MZGenre.SocialNetworking": "SOCIAL_NETWORKING", "productivity\n": "PRODUCTIVITY", "PHOTO_AND_VIDEO": "PHOTO_AND_VIDEO"} {
		if got := NormalizeCategory(in); got != want {
			t.Errorf("%q = %q, want %q", in, got, want)
		}
	}
}

func TestPullWritesFastlaneLayout(t *testing.T) {
	f := newFakeASC(t)
	f.seed()
	f.find("appInfos", "ai-1").rels["secondaryCategory"] = map[string]any{"data": map[string]any{"type": "appCategories", "id": "UTILITIES"}}
	set := f.add("appScreenshotSets", "set-1", "vl-en", map[string]any{"screenshotDisplayType": "APP_IPHONE_67"})
	shotA := pngBytes(t, 1290, 2796, 1)
	f.addScreenshot(set.id, "01_home.png", shotA)
	f.addScreenshot(set.id, "list.jpg", []byte("jpeg-bytes"))
	metaDir, shotDir := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(metaDir, "en-US", "release_notes.txt"), "stale notes")
	writeFile(t, filepath.Join(metaDir, "en-US", "keywords.txt"), "a,b\n")
	writeFile(t, filepath.Join(shotDir, "en-US", "stale.png"), "x")

	opts := &Options{BundleID: "com.example.app", MetadataDir: metaDir, ScreenshotsDir: shotDir}
	res, err := Pull(ctx(), f.client(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if w := f.writes(); len(w) != 0 {
		t.Errorf("pull wrote to App Store Connect: %v", w)
	}
	for file, want := range map[string]string{
		"en-US/name.txt": "My App\n", "en-US/subtitle.txt": "Old subtitle\n", "en-US/privacy_url.txt": "https://example.com/privacy\n",
		"en-US/description.txt": "Old description\n", "en-US/support_url.txt": "https://example.com/support\n",
		"primary_category.txt": "GAMES\n", "secondary_category.txt": "UTILITIES\n",
	} {
		if got := readFile(t, filepath.Join(metaDir, file)); got != want {
			t.Errorf("%s = %q, want %q", file, got, want)
		}
	}
	if res.Unchanged != 1 || len(res.Written) != 7 || res.Version.VersionString != "1.1" || !reflect.DeepEqual(res.Locales, []string{"en-US"}) {
		t.Errorf("result = %+v", res)
	}
	if readFile(t, filepath.Join(metaDir, "en-US", "release_notes.txt")) != "stale notes" {
		t.Error("pull without --clean removed a local file")
	}
	if _, err := os.Stat(filepath.Join(shotDir, "en-US", "APP_IPHONE_67")); err == nil {
		t.Error("screenshots downloaded without Screenshots")
	}

	opts.Screenshots, opts.Clean = true, true
	res, err = Pull(ctx(), f.client(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(shotDir, "en-US", "APP_IPHONE_67", "01_home.png")); got != string(shotA) {
		t.Error("first screenshot differs")
	}
	if got := readFile(t, filepath.Join(shotDir, "en-US", "APP_IPHONE_67", "02_list.jpg")); got != "jpeg-bytes" {
		t.Errorf("second screenshot = %q", got)
	}
	wantRemoved := []string{filepath.Join(metaDir, "en-US", "release_notes.txt"), filepath.Join(shotDir, "en-US", "stale.png")}
	if !reflect.DeepEqual(res.Removed, wantRemoved) || len(res.Screenshots) != 2 {
		t.Errorf("removed = %v, screenshots = %v", res.Removed, res.Screenshots)
	}

	// The pulled tree pushes back as no change at all.
	plan, err := NewPlan(ctx(), f.client(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Empty() {
		t.Errorf("round trip plan = %v", changeLines(plan))
	}
}

// obj walks decoded JSON down the given object keys.
func obj(t *testing.T, v any, keys ...string) map[string]any {
	t.Helper()
	for i := 0; ; i++ {
		m, ok := v.(map[string]any)
		if !ok {
			t.Errorf("JSON path %v: %T is not an object", keys[:i], v)
			return nil
		}
		if i == len(keys) {
			return m
		}
		v = m[keys[i]]
	}
}
