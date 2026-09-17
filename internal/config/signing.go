package config

import (
	"fmt"
	"strings"
)

// Canonical distribution names: what a build profile's distribution field
// means once aliases are resolved, and the names the runner compares with the
// provisioning profile it is handed.
const (
	DistributionDevelopment = "development"
	DistributionAdHoc       = "ad-hoc"
	DistributionStore       = "store"
	DistributionEnterprise  = "enterprise"
)

// Distributions are the canonical values of a profile's distribution field.
var Distributions = []string{DistributionDevelopment, DistributionAdHoc, DistributionStore, DistributionEnterprise}

// distributionAliases are accepted spellings of a canonical distribution.
var distributionAliases = map[string]string{"internal": DistributionAdHoc}

// ParseDistribution canonicalizes a distribution value (internal is ad-hoc).
// Empty stays empty: it means an unsigned build.
func ParseDistribution(s string) (string, error) {
	s = strings.TrimSpace(s)
	if alias, ok := distributionAliases[s]; ok {
		return alias, nil
	}
	for _, d := range Distributions {
		if s == d {
			return d, nil
		}
	}
	if s == "" {
		return "", nil
	}
	if s == "app-store" {
		return "", fmt.Errorf("distribution %q is now %q", s, DistributionStore)
	}
	return "", fmt.Errorf("distribution %q must be one of %s (internal is ad-hoc)", s, strings.Join(Distributions, ", "))
}

// SigningSet returns the suffix of the secrets a distribution is signed with
// (DEVELOPMENT, AD_HOC, STORE, ENTERPRISE; "" for the legacy ios.signing path,
// which reads the unsuffixed secrets). The shell function signing_set in
// ios-build.yml and runner.sh is the same table and must agree.
func SigningSet(distribution string) (string, error) {
	d, err := ParseDistribution(distribution)
	if err != nil {
		return "", err
	}
	return strings.ToUpper(strings.ReplaceAll(d, "-", "_")), nil
}

// SigningSecrets names the three secrets of a signing set.
type SigningSecrets struct {
	Certificate string // base64 .p12
	Password    string // the .p12 password
	Profile     string // base64 .mobileprovision
}

// Names lists the three secret names in the order they are written.
func (s SigningSecrets) Names() []string { return []string{s.Certificate, s.Password, s.Profile} }

// SigningSecretNames returns the secret names of a set: IOS_CERTIFICATE_<SET>,
// IOS_CERTIFICATE_PASSWORD_<SET> and IOS_PROVISIONING_PROFILE_<SET>. The empty
// set names the unsuffixed legacy secrets.
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

// SigningSet is the secret set the build signs with, from its distribution;
// empty for the legacy path.
func (s *BuildSettings) SigningSet() string {
	set, _ := SigningSet(s.Distribution) // validated by ResolveProfile
	return set
}
