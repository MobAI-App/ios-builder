package config

import "testing"

func TestSigningSet(t *testing.T) {
	for distribution, want := range map[string]string{
		"": "DEVELOPMENT", "development": "DEVELOPMENT", "ad-hoc": "AD_HOC", "app-store": "APP_STORE", "enterprise": "ENTERPRISE",
	} {
		got, err := SigningSet(distribution)
		if err != nil || got != want {
			t.Errorf("SigningSet(%q) = %q, %v; want %q", distribution, got, err, want)
		}
	}
	for _, bad := range []string{"adhoc", "AD_HOC", "Development"} {
		if _, err := SigningSet(bad); err == nil {
			t.Errorf("SigningSet(%q) accepted", bad)
		}
	}
	// Every accepted distribution has a set, and the settings expose it.
	for _, d := range Distributions {
		s := BuildSettings{Distribution: d}
		if s.SigningSet() == "" {
			t.Errorf("no set for %s", d)
		}
	}
}

func TestSigningSecretNames(t *testing.T) {
	got := SigningSecretNames("APP_STORE")
	want := SigningSecrets{"IOS_CERTIFICATE_APP_STORE", "IOS_CERTIFICATE_PASSWORD_APP_STORE", "IOS_PROVISIONING_PROFILE_APP_STORE"}
	if got != want {
		t.Errorf("suffixed = %+v, want %+v", got, want)
	}
	legacy := SigningSecretNames("")
	if legacy != (SigningSecrets{"IOS_CERTIFICATE", "IOS_CERTIFICATE_PASSWORD", "IOS_PROVISIONING_PROFILE"}) {
		t.Errorf("legacy = %+v", legacy)
	}
	// Every name is one a profile's env may not set.
	for _, name := range []string{got.Certificate, got.Password, got.Profile, legacy.Certificate, legacy.Password, legacy.Profile} {
		if !reservedEnvName(name) {
			t.Errorf("%s is not reserved", name)
		}
	}
}
