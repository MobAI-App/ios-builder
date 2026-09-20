// Package ipa exposes the metadata reading of .ipa archives to code outside
// this module: the Info.plist of the app bundle and its embedded
// provisioning profile.
package ipa

import "github.com/MobAI-App/ios-builder/internal/ipa"

// ErrUnsigned is ReadProfile's error for an IPA with no embedded profile.
var ErrUnsigned = ipa.ErrUnsigned

// Info is the subset of the app's Info.plist that Builder needs.
type Info = ipa.Info

// ReadInfo returns the Info.plist of the app bundle inside the IPA.
func ReadInfo(path string) (*Info, error) { return ipa.ReadInfo(path) }

// BundleID returns the bundle identifier of the IPA, or "" when it cannot be read.
func BundleID(path string) string { return ipa.BundleID(path) }

// ReadProfile returns the embedded.mobileprovision of the app bundle inside
// the IPA, ErrUnsigned when it has none.
func ReadProfile(path string) ([]byte, error) { return ipa.ReadProfile(path) }

// Newest returns the most recently modified .ipa in dir.
func Newest(dir string) (string, error) { return ipa.Newest(dir) }
