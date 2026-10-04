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

// Inspect reads the IPA and refuses one the backend cannot deliver: the
// over-the-air backends need a development, ad-hoc or enterprise signature;
// testflight needs the opposite, an App Store one.
func Inspect(path, backend string) (*App, error) {
	info, err := ipa.ReadInfo(path)
	if err != nil {
		return nil, err
	}
	testflight := backend == BackendTestFlight
	profile, err := ipa.ReadProfile(path)
	if errors.Is(err, ipa.ErrUnsigned) {
		if testflight {
			return nil, fmt.Errorf("%s is unsigned; TestFlight needs an App Store signed IPA (builder ios build --profile <a profile with distribution store>)", path)
		}
		return nil, fmt.Errorf("%s is unsigned; build with a profile whose distribution is development, ad-hoc or enterprise (builder ios build --profile <name>)", path)
	}
	if err != nil {
		return nil, err
	}
	typ, err := signing.ProfileType(profile)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	app := &App{Path: path, BundleID: info.BundleID, Version: info.Version, Build: info.BuildNumber, Title: info.Name(), Profile: Profile{Type: string(typ)}}
	switch {
	case testflight && typ != signing.TypeStore:
		return nil, fmt.Errorf("%s is signed for %s, but TestFlight takes only App Store signed builds; build with a store profile (builder signing setup --distribution store writes one) or drop --backend testflight to install it over the air", path, typ)
	case testflight:
		return app, nil
	case typ == signing.TypeStore:
		return nil, fmt.Errorf("%s is signed for the App Store, which cannot be installed over the air; use --backend testflight --group <internal group>, or build with a development or ad-hoc profile", path)
	}
	if app.Profile.Devices, err = signing.ProfileDevices(profile); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return app, nil
}

// CheckDistribution is the preflight of `ios build --distribute`: the profile
// must sign the way the backend needs, and that is known before anything is
// pushed. For testflight an empty profile passes, since the release preflight
// then picks the only store profile.
func CheckDistribution(s *config.BuildSettings, backend string) error {
	if backend == BackendTestFlight {
		switch s.Distribution {
		case config.DistributionStore:
			return nil
		case "":
			if s.Profile == "" {
				return nil
			}
			return fmt.Errorf("profile %q has no distribution, so the build is unsigned; TestFlight needs \"distribution\": \"store\" (builder signing setup --distribution store)", s.Profile)
		}
		return fmt.Errorf("profile %q has distribution %s; TestFlight takes only App Store builds, so pass a store profile (builder signing setup --distribution store writes one) or another --backend", s.Profile, s.Distribution)
	}
	switch s.Distribution {
	case config.DistributionDevelopment, config.DistributionAdHoc, config.DistributionEnterprise:
		return nil
	case config.DistributionStore:
		return fmt.Errorf("profile %q has distribution store; an App Store build cannot be installed over the air. Use --backend testflight --group <internal group>, or a development or ad-hoc profile (builder signing setup --devices-from-mobai writes one)", s.Profile)
	}
	if s.Profile == "" {
		return errors.New("--distribute needs a signed build: pass --profile with a development, ad-hoc or enterprise profile (builder signing setup --devices-from-mobai writes one)")
	}
	return fmt.Errorf("profile %q has no distribution, so the build is unsigned and cannot be installed; give it \"distribution\": \"development\" or \"ad-hoc\" (builder signing setup --devices-from-mobai)", s.Profile)
}
