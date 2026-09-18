// Package otainstall installs a signed IPA on an iPhone over the air: the
// IPA and an itms-services manifest go somewhere the device can fetch them
// and the user scans the link. It is not OTA updates; every install is a
// whole build.
package otainstall

import (
	"errors"
	"fmt"

	"github.com/MobAI-App/ios-builder/internal/config"
	"github.com/MobAI-App/ios-builder/internal/ipa"
	"github.com/MobAI-App/ios-builder/internal/signing"
)

// App is what the manifest and the summary need from an IPA.
type App struct {
	Path     string  `json:"-"`
	BundleID string  `json:"bundle_id"`
	Version  string  `json:"version"`
	Build    string  `json:"build"`
	Title    string  `json:"title"`
	Profile  Profile `json:"profile"`
}

// Profile is the type of the embedded provisioning profile and, for
// development and ad-hoc, how many devices it lists.
type Profile struct {
	Type    string `json:"type"`
	Devices int    `json:"devices,omitempty"`
}

// String is "development, 2 devices" or "enterprise".
func (p Profile) String() string {
	if p.Type == config.DistributionDevelopment || p.Type == config.DistributionAdHoc {
		return fmt.Sprintf("%s, %d device(s)", p.Type, p.Devices)
	}
	return p.Type
}

// Inspect reads the IPA and refuses one an iPhone cannot install this way.
func Inspect(path string) (*App, error) {
	info, err := ipa.ReadInfo(path)
	if err != nil {
		return nil, err
	}
	profile, err := ipa.ReadProfile(path)
	if errors.Is(err, ipa.ErrUnsigned) {
		return nil, fmt.Errorf("%s is unsigned; build with a profile whose distribution is development, ad-hoc or enterprise (builder ios build --profile <name>)", path)
	}
	if err != nil {
		return nil, err
	}
	typ, err := signing.ProfileType(profile)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if typ == signing.TypeStore {
		return nil, fmt.Errorf("%s is signed for the App Store, which cannot be installed over the air; use TestFlight (builder ios release) or build with a development or ad-hoc profile", path)
	}
	devices, err := signing.ProfileDevices(profile)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &App{
		Path: path, BundleID: info.BundleID, Version: info.Version, Build: info.BuildNumber, Title: info.Name(),
		Profile: Profile{Type: string(typ), Devices: devices},
	}, nil
}

// CheckDistribution is the preflight of `ios build --distribute`: the profile
// must sign for devices, and that is known before anything is pushed.
func CheckDistribution(s *config.BuildSettings) error {
	switch s.Distribution {
	case config.DistributionDevelopment, config.DistributionAdHoc, config.DistributionEnterprise:
		return nil
	case config.DistributionStore:
		return fmt.Errorf("profile %q has distribution store; an App Store build cannot be installed over the air. Use TestFlight (builder ios release) or a development or ad-hoc profile (builder signing setup --devices-from-mobai writes one)", s.Profile)
	}
	if s.Profile == "" {
		return errors.New("--distribute needs a signed build: pass --profile with a development, ad-hoc or enterprise profile (builder signing setup --devices-from-mobai writes one)")
	}
	return fmt.Errorf("profile %q has no distribution, so the build is unsigned and cannot be installed; give it \"distribution\": \"development\" or \"ad-hoc\" (builder signing setup --devices-from-mobai)", s.Profile)
}
