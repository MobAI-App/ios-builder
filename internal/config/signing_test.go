package config

import (
	"slices"
	"testing"
)

func TestSigningSet(t *testing.T) {
	for distribution, want := range map[string]string{
		"": "", "development": "DEVELOPMENT", "ad-hoc": "AD_HOC", "internal": "AD_HOC", "store": "STORE", "enterprise": "ENTERPRISE",
	} {
		got, err := SigningSet(distribution)
		if err != nil || got != want {
			t.Errorf("SigningSet(%q) = %q, %v; want %q", distribution, got, err, want)
		}
	}
	for _, bad := range []string{"adhoc", "app-store", "AD_HOC", "Development"} {
		if _, err := SigningSet(bad); err == nil {
			t.Errorf("SigningSet(%q) accepted", bad)
		}
	}
	// Every canonical distribution has a set, and the settings expose it.
	for _, d := range Distributions {
		s := BuildSettings{Distribution: d}
		if s.SigningSet() == "" {
			t.Errorf("no set for %s", d)
		}
	}
	if (&BuildSettings{Signing: true}).SigningSet() != "" {
		t.Error("the legacy path has no set")
	}
}

func TestSigningSecretNames(t *testing.T) {
	got := SigningSecretNames("STORE")
	want := SigningSecrets{"IOS_CERTIFICATE_STORE", "IOS_CERTIFICATE_PASSWORD_STORE", "IOS_PROVISIONING_PROFILE_STORE"}
	if got != want {
		t.Errorf("suffixed = %+v, want %+v", got, want)
	}
	if !slices.Equal(got.Names(), []string{"IOS_CERTIFICATE_STORE", "IOS_CERTIFICATE_PASSWORD_STORE", "IOS_PROVISIONING_PROFILE_STORE"}) {
		t.Errorf("Names = %v", got.Names())
	}
	legacy := SigningSecretNames("")
	if legacy != (SigningSecrets{"IOS_CERTIFICATE", "IOS_CERTIFICATE_PASSWORD", "IOS_PROVISIONING_PROFILE"}) {
		t.Errorf("legacy = %+v", legacy)
	}
	// Every name is one a profile's env may not set.
	for _, name := range append(got.Names(), legacy.Names()...) {
		if !reservedEnvName(name) {
			t.Errorf("%s is not reserved", name)
		}
	}
}
