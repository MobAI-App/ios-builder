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
	Signing       bool
	Provider      string // profile provider, else the top-level provider; may be empty (GitHub)
	Env           map[string]string
	Distribution  string
}

// Distributions are the accepted values of a profile's distribution field.
var Distributions = []string{"development", "ad-hoc", "app-store", "enterprise"}

// reservedEnv names the variables the runners read their parameters from. A
// profile that set one of these would silently change the build.
var reservedEnv = []string{
	"BUILD_ID", "SNAPSHOT_REF", "SNAPSHOT_SHA", "IOS_PATH", "SCHEME", "CONFIGURATION",
	"USE_SIGNING", "FLUTTER_VERSION", "JDK_VERSION", "BUILD_ENV", "DISTRIBUTION",
	"BUILDER_REPOSITORY", "BUILDER_WORKSPACE", "DURATION", "PROJECT_TYPE",
}

var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

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
func (c *Config) ResolveProfile(name string) (BuildSettings, error) {
	s := BuildSettings{
		Configuration: c.IOS.Configuration,
		Scheme:        c.IOS.Scheme,
		Signing:       c.IOS.Signing,
		Provider:      c.Provider,
	}
	if name == "" {
		name = c.DefaultProfile
	}
	if name == "" {
		return s, nil
	}
	p, ok := c.Profiles[name]
	if !ok {
		if len(c.Profiles) == 0 {
			return s, fmt.Errorf("profile %q is not defined; builder.json has no profiles", name)
		}
		return s, fmt.Errorf("profile %q is not defined; available profiles: %s", name, strings.Join(c.ProfileNames(), ", "))
	}
	if p.Distribution != "" && !slices.Contains(Distributions, p.Distribution) {
		return s, fmt.Errorf("profile %q: distribution %q must be one of %s", name, p.Distribution, strings.Join(Distributions, ", "))
	}
	for k := range p.Env {
		if !envNameRe.MatchString(k) {
			return s, fmt.Errorf("profile %q: env name %q is not a valid environment variable name", name, k)
		}
		if slices.Contains(reservedEnv, k) {
			return s, fmt.Errorf("profile %q: env name %q is reserved for the runner's own parameters", name, k)
		}
	}
	s.Profile = name
	if p.Configuration != "" {
		s.Configuration = p.Configuration
	}
	if p.Scheme != "" {
		s.Scheme = p.Scheme
	}
	if p.Signing != nil {
		s.Signing = *p.Signing
	}
	if p.Provider != "" {
		s.Provider = p.Provider
	}
	if len(p.Env) > 0 {
		s.Env = p.Env
	}
	s.Distribution = p.Distribution
	return s, nil
}

// EnvJSON encodes the profile's environment as a JSON object, which is how it
// travels to the runner: workflow inputs and CI variables are strings, and JSON
// survives values with spaces, quotes and newlines. Empty when there is none.
func (s BuildSettings) EnvJSON() string {
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
func (s BuildSettings) ProfileInput() string {
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
