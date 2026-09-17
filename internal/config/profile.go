package config

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// BuildSettings is what a build runs with once a profile has been applied over
// the top-level settings. Command flags (--unsigned, --provider) are applied by
// the caller on top of this.
type BuildSettings struct {
	Profile       string // selected profile name, empty when none applies
	Configuration string
	Scheme        string
	// Signing is true when the profile has a distribution, or, with no
	// profile, when ios.signing is set (the legacy path).
	Signing  bool
	Provider string // profile provider, else the top-level provider; may be empty (GitHub)
	Env      map[string]string
	// Distribution is the profile's distribution, canonical (internal is
	// ad-hoc); empty for unsigned builds and the legacy path.
	Distribution string
}

// reservedEnv names the variables the runners read their parameters and
// secrets from, and the ones the shell and the CI services own. A profile that
// set one of these would silently change the build, or on runner.sh replace a
// provider secret, since the env is exported before the signing step reads it.
var reservedEnv = []string{
	"BUILD_ID", "SNAPSHOT_REF", "SNAPSHOT_SHA", "IOS_PATH", "SCHEME", "CONFIGURATION",
	"USE_SIGNING", "FLUTTER_VERSION", "JDK_VERSION", "BUILD_ENV", "DISTRIBUTION",
	"SIGNING_SET", "SIGNING_SET_USED", "DURATION", "PROJECT_TYPE", "EXPORT_METHOD",
	"BUILD_NUMBER", "CODE_SIGN_IDENTITY", "MOBAI_API_KEY",
	"PATH", "HOME", "USER", "SHELL", "TMPDIR", "DEVELOPER_DIR", "NODE_OPTIONS",
}

// reservedEnvPrefixes cover the runners' own namespaces: Builder's, GitHub
// Actions' (GITHUB_*, RUNNER_*, ACTIONS_*), Codemagic's (CM_*, FCI_*),
// Bitrise's, and the signing secrets with every set suffix.
var reservedEnvPrefixes = []string{
	"BUILDER_", "GITHUB_", "RUNNER_", "ACTIONS_", "CM_", "FCI_", "BITRISE_",
	"IOS_CERTIFICATE", "IOS_PROVISIONING_PROFILE",
}

var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func reservedEnvName(name string) bool {
	if slices.Contains(reservedEnv, name) {
		return true
	}
	for _, prefix := range reservedEnvPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// ProfileNames lists the configured profiles, sorted.
func (c *Config) ProfileNames() []string {
	names := make([]string, 0, len(c.Profiles))
	for n := range c.Profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// ResolveProfile applies the named profile, or defaultProfile when name is
// empty, over the top-level ios.* and provider settings. With neither, the
// result is the top-level settings unchanged, so projects without profiles
// build exactly as before.
//
// A profile signs exactly when it has a distribution; ios.signing does not
// apply to it. Its configuration is the one it sets, else Debug for
// development and Release for every other distribution, else ios.configuration.
func (c *Config) ResolveProfile(name string) (BuildSettings, error) {
	s := BuildSettings{
		Configuration: c.IOS.Configuration,
		Scheme:        c.IOS.Scheme,
		Signing:       c.IOS.Signing,
		Provider:      c.Provider,
	}
	source := "profile"
	if name == "" {
		name, source = c.DefaultProfile, "defaultProfile"
	}
	if name == "" {
		return s, nil
	}
	p, ok := c.Profiles[name]
	if !ok {
		if len(c.Profiles) == 0 {
			return s, fmt.Errorf("%s %q is not defined; builder.json has no profiles", source, name)
		}
		return s, fmt.Errorf("%s %q is not defined; available profiles: %s", source, name, strings.Join(c.ProfileNames(), ", "))
	}
	distribution, err := ParseDistribution(p.Distribution)
	if err != nil {
		return s, fmt.Errorf("profile %q: %w", name, err)
	}
	for k := range p.Env {
		if !envNameRe.MatchString(k) {
			return s, fmt.Errorf("profile %q: env name %q is not a valid environment variable name", name, k)
		}
		if reservedEnvName(k) {
			return s, fmt.Errorf("profile %q: env name %q is reserved for the runner", name, k)
		}
	}
	s.Profile = name
	s.Distribution = distribution
	s.Signing = distribution != ""
	switch {
	case p.Configuration != "":
		s.Configuration = p.Configuration
	case distribution == DistributionDevelopment:
		s.Configuration = "Debug"
	case distribution != "":
		s.Configuration = "Release"
	}
	if p.Scheme != "" {
		s.Scheme = p.Scheme
	}
	if p.Provider != "" {
		s.Provider = p.Provider
	}
	if len(p.Env) > 0 {
		s.Env = p.Env
	}
	return s, nil
}

// EnvJSON encodes the profile's environment as a JSON object, which is how it
// travels to the runner: workflow inputs and CI variables are strings, and JSON
// survives values with spaces, quotes and newlines. Empty when there is none.
func (s *BuildSettings) EnvJSON() string {
	if len(s.Env) == 0 {
		return ""
	}
	data, _ := json.Marshal(s.Env) // a map[string]string cannot fail to marshal
	return string(data)
}

// ProfileInput encodes the parts of the profile that are not workflow inputs of
// their own (name, env, distribution) as the single `profile` dispatch input,
// keeping the workflow under GitHub's limit of ten inputs. Empty when no
// profile is selected, so older workflow files keep receiving the inputs they
// declare.
func (s *BuildSettings) ProfileInput() string {
	if s.Profile == "" {
		return ""
	}
	env := s.Env
	if env == nil {
		env = map[string]string{}
	}
	data, _ := json.Marshal(struct {
		Name         string            `json:"name"`
		Env          map[string]string `json:"env"`
		Distribution string            `json:"distribution"`
	}{s.Profile, env, s.Distribution})
	return string(data)
}
