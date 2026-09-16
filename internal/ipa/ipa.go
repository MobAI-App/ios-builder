// Package ipa reads the metadata of an .ipa archive without extracting it.
package ipa

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"howett.net/plist"
)

// Info is the subset of the app's Info.plist that Builder needs.
type Info struct {
	BundleID    string `plist:"CFBundleIdentifier"`
	Version     string `plist:"CFBundleShortVersionString"`
	BuildNumber string `plist:"CFBundleVersion"`
	// UsesNonExemptEncryption is nil when the plist does not declare
	// ITSAppUsesNonExemptEncryption, in which case App Store Connect asks for
	// the export compliance answer before a build can be distributed.
	UsesNonExemptEncryption *bool `plist:"ITSAppUsesNonExemptEncryption"`
}

// ReadInfo returns the Info.plist of the app bundle inside the IPA.
func ReadInfo(path string) (*Info, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("open IPA: %w", err)
	}
	defer func() { _ = r.Close() }()

	for _, f := range r.File {
		if !isAppInfoPlist(f.Name) {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", f.Name, err)
		}
		data, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", f.Name, err)
		}
		var info Info
		if _, err := plist.Unmarshal(data, &info); err != nil {
			return nil, fmt.Errorf("parse %s: %w", f.Name, err)
		}
		if info.BundleID == "" {
			return nil, fmt.Errorf("%s has no CFBundleIdentifier", f.Name)
		}
		return &info, nil
	}
	return nil, errors.New("no Payload/*.app/Info.plist in IPA")
}

// isAppInfoPlist matches the top-level app's plist only, not the ones of
// embedded frameworks, extensions or watch apps.
func isAppInfoPlist(name string) bool {
	return strings.HasPrefix(name, "Payload/") &&
		strings.HasSuffix(name, ".app/Info.plist") &&
		strings.Count(name, "/") == 2
}

// BundleID returns the bundle identifier of the IPA, or "" when it cannot be read.
func BundleID(path string) string {
	info, err := ReadInfo(path)
	if err != nil {
		return ""
	}
	return info.BundleID
}

// Newest returns the most recently modified .ipa in dir.
func Newest(dir string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.ipa"))
	if err != nil {
		return "", err
	}
	var newest string
	var newestTime int64
	for _, m := range matches {
		st, err := os.Stat(m)
		if err != nil || st.IsDir() {
			continue
		}
		if newest == "" || st.ModTime().UnixNano() > newestTime {
			newest, newestTime = m, st.ModTime().UnixNano()
		}
	}
	if newest == "" {
		return "", fmt.Errorf("no .ipa found in %s; run builder ios build or pass --ipa", dir)
	}
	return newest, nil
}
