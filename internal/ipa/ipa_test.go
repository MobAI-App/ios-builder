package ipa

import (
	"archive/zip"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeIPA(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

const appPlist = `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>com.example.app</string>
<key>CFBundleShortVersionString</key><string>1.2.3</string>
<key>CFBundleVersion</key><string>42</string>
<key>ITSAppUsesNonExemptEncryption</key><false/>
</dict></plist>`

const frameworkPlist = `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict><key>CFBundleIdentifier</key><string>com.example.framework</string></dict></plist>`

func TestReadInfo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "App.ipa")
	writeIPA(t, path, map[string]string{
		// Listed first so a naive suffix match would pick the framework.
		"Payload/App.app/Frameworks/Lib.framework/Info.plist": frameworkPlist,
		"Payload/App.app/Info.plist":                          appPlist,
	})
	info, err := ReadInfo(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.BundleID != "com.example.app" || info.Version != "1.2.3" || info.BuildNumber != "42" {
		t.Errorf("unexpected info: %+v", info)
	}
	if info.UsesNonExemptEncryption == nil || *info.UsesNonExemptEncryption {
		t.Errorf("UsesNonExemptEncryption = %v, want false", info.UsesNonExemptEncryption)
	}
	if got := BundleID(path); got != "com.example.app" {
		t.Errorf("BundleID = %q", got)
	}
}

func TestReadProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Signed.ipa")
	writeIPA(t, path, map[string]string{
		"Payload/App.app/Info.plist":                                        appPlist,
		"Payload/App.app/Frameworks/Lib.framework/embedded.mobileprovision": "framework profile",
		"Payload/App.app/embedded.mobileprovision":                          "app profile",
	})
	data, err := ReadProfile(path)
	if err != nil || string(data) != "app profile" {
		t.Errorf("ReadProfile = %q, %v", data, err)
	}
	unsigned := filepath.Join(t.TempDir(), "Unsigned.ipa")
	writeIPA(t, unsigned, map[string]string{"Payload/App.app/Info.plist": appPlist})
	if _, err := ReadProfile(unsigned); !errors.Is(err, ErrUnsigned) {
		t.Errorf("unsigned: err = %v", err)
	}
	if _, err := ReadProfile(filepath.Join(t.TempDir(), "missing.ipa")); err == nil || errors.Is(err, ErrUnsigned) {
		t.Errorf("missing: err = %v", err)
	}
}

func TestInfoName(t *testing.T) {
	for _, tc := range []struct{ display, bundle, want string }{
		{"Tap Dash", "TapDash", "Tap Dash"}, {"", "TapDash", "TapDash"}, {"", "", "com.example.app"},
	} {
		info := &Info{BundleID: "com.example.app", DisplayName: tc.display, BundleName: tc.bundle}
		if got := info.Name(); got != tc.want {
			t.Errorf("Name(%q, %q) = %q, want %q", tc.display, tc.bundle, got, tc.want)
		}
	}
}

func TestReadInfoErrors(t *testing.T) {
	if _, err := ReadInfo(filepath.Join(t.TempDir(), "missing.ipa")); err == nil {
		t.Error("missing IPA: want error")
	}
	path := filepath.Join(t.TempDir(), "NoPlist.ipa")
	writeIPA(t, path, map[string]string{"Payload/App.app/app": "bin"})
	if _, err := ReadInfo(path); err == nil {
		t.Error("IPA without plist: want error")
	}
	if got := BundleID(path); got != "" {
		t.Errorf("BundleID = %q, want empty", got)
	}
}

func TestNewest(t *testing.T) {
	dir := t.TempDir()
	if _, err := Newest(dir); err == nil {
		t.Error("empty dir: want error")
	}
	old := filepath.Join(dir, "old.ipa")
	recent := filepath.Join(dir, "recent.ipa")
	for _, p := range []string{old, recent} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	got, err := Newest(dir)
	if err != nil || got != recent {
		t.Errorf("Newest = %q, %v; want %q", got, err, recent)
	}
}
