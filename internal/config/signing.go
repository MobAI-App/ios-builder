package config

import (
	"fmt"
	"strings"
)

// signingSets maps a profile's distribution to the suffix of the IOS_* secrets
// the runner reads for it. No distribution means development, so a repository
// set up before signing sets keeps building with the secrets it has. The shell
// function signing_set in ios-build.yml and runner.sh is the same table.
var signingSets = map[string]string{
	"":            "DEVELOPMENT",
	"development": "DEVELOPMENT",
	"ad-hoc":      "AD_HOC",
	"app-store":   "APP_STORE",
	"enterprise":  "ENTERPRISE",
}

// SigningSet returns the suffix of the secrets a distribution is signed with:
// DEVELOPMENT, AD_HOC, APP_STORE or ENTERPRISE.
func SigningSet(distribution string) (string, error) {
	set, ok := signingSets[distribution]
	if !ok {
		return "", fmt.Errorf("distribution %q must be one of %s", distribution, strings.Join(Distributions, ", "))
	}
	return set, nil
}

// SigningSecrets names the three secrets of a signing set.
type SigningSecrets struct {
	Certificate string // base64 .p12
	Password    string // the .p12 password
	Profile     string // base64 .mobileprovision
}

// SigningSecretNames returns the secret names of a set: IOS_CERTIFICATE_<SET>,
// IOS_CERTIFICATE_PASSWORD_<SET> and IOS_PROVISIONING_PROFILE_<SET>. The empty
// set names the unsuffixed secrets, which every set falls back to.
func SigningSecretNames(set string) SigningSecrets {
	suffix := ""
	if set != "" {
		suffix = "_" + set
	}
	return SigningSecrets{
		Certificate: "IOS_CERTIFICATE" + suffix,
		Password:    "IOS_CERTIFICATE_PASSWORD" + suffix,
		Profile:     "IOS_PROVISIONING_PROFILE" + suffix,
	}
}

// SigningSet is the secret set the build signs with, from its distribution.
func (s *BuildSettings) SigningSet() string {
	set, _ := SigningSet(s.Distribution) // validated by ResolveProfile
	return set
}
