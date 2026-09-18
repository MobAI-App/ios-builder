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
	// Hooks are the top-level hooks with the profile's applied field by
	// field, trimmed; they apply with no profile too.
	Hooks Hooks
}

// Empty reports whether no hook is set.
func (h Hooks) Empty() bool { return h.PreBuild == "" && h.PostBuild == "" }

// Names lists the hooks that are set, in the order they run.
func (h Hooks) Names() []string {
	var names []string
	if h.PreBuild != "" {
		names = append(names, "preBuild")
	}
	if h.PostBuild != "" {
		names = append(names, "postBuild")
	}
	return names
}

// resolveHooks lays the profile's hooks over the top-level ones: a non-blank
// command replaces the one below it, a blank one keeps it. Surrounding
// whitespace is dropped, so a hook that is only whitespace is absent.
func resolveHooks(top, profile *Hooks) Hooks {
	var h Hooks
	for _, src := range []*Hooks{top, profile} {
		if src == nil {
			continue
		}
		if v := strings.TrimSpace(src.PreBuild); v != "" {
			h.PreBuild = v
		}
		if v := strings.TrimSpace(src.PostBuild); v != "" {
			h.PostBuild = v
		}
	}
	return h
}

// reservedEnv names the variables the runners, the shell and the CI services
// own. A profile that set one would silently change the build or, on
// runner.sh, replace a provider secret, since the env is exported first.
var reservedEnv = []string{
	"BUILD_ID", "SNAPSHOT_REF", "SNAPSHOT_SHA", "IOS_PATH", "SCHEME", "CONFIGURATION",
	"USE_SIGNING", "FLUTTER_VERSION", "JDK_VERSION", "BUILD_ENV", "BUILD_HOOKS", "BUILD_PROFILE", "DISTRIBUTION",
	"SIGNING_SET", "SIGNING_SET_USED", "DURATION", "PROJECT_TYPE", "EXPORT_METHOD",
	"BUILD_NUMBER", "CODE_SIGN_IDENTITY", "DEVELOPMENT_TEAM", "PROVISIONING_PROFILE_NAME", "PROFILE_BUNDLE_ID", "EXTENSION_PROFILES",
	"MOBAI_API_KEY",
	"PATH", "HOME", "USER", "SHELL", "TMPDIR", "DEVELOPER_DIR", "NODE_OPTIONS",
}

// reservedEnvPrefixes cover the runners' own namespaces: Builder's, GitHub
// Actions' (GITHUB_*, RUNNER_*, ACTIONS_*), Codemagic's (CM_*, FCI_*),
// Bitrise's, and the signing secrets with every set suffix.
var reservedEnvPrefixes = []string{
	"BUILDER_", "GITHUB_", "RUNNER_", "ACTIONS_", "CM_", "FCI_", "BITRISE_",
	"IOS_CERTIFICATE", "IOS_PROVISIONING_PROFILE", "IOS_EXTENSION_PROFILES",
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
// empty, over the top-level ios.* and provider settings; with neither the
// top-level settings come back unchanged. A profile signs exactly when it has a
// distribution (ios.signing does not apply to it), and its configuration
// defaults to Debug for development and Release for every other distribution.
func (c *Config) ResolveProfile(name string) (BuildSettings, error) {
	s := BuildSettings{
		Configuration: c.IOS.Configuration,
		Scheme:        c.IOS.Scheme,
		Signing:       c.IOS.Signing,
		Provider:      c.Provider,
		Hooks:         resolveHooks(c.Hooks, nil),
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
	s.Hooks = resolveHooks(c.Hooks, p.Hooks)
	return s, nil
}

// EnvJSON encodes the profile's environment as a JSON object (empty when there
// is none), because workflow inputs and CI variables are strings and JSON
// survives values with spaces, quotes and newlines.
func (s *BuildSettings) EnvJSON() string {
	if len(s.Env) == 0 {
		return ""
	}
	data, _ := json.Marshal(s.Env) // a map[string]string cannot fail to marshal
	return string(data)
}

// ProfileInput encodes name, env, distribution and hooks as the single
// `profile` dispatch input, keeping the workflow under GitHub's limit of ten
// inputs. It is empty when no profile is selected and no hook is set, so older
// workflow files still work; top-level hooks without a profile travel with an
// empty name, which the workflow reads as "no profile".
func (s *BuildSettings) ProfileInput() string {
	if s.Profile == "" && s.Hooks.Empty() {
		return ""
	}
	env := s.Env
	if env == nil {
		env = map[string]string{}
	}
	var hooks *Hooks
	if !s.Hooks.Empty() {
		h := s.Hooks
		hooks = &h
	}
	data, _ := json.Marshal(struct {
		Name         string            `json:"name"`
		Env          map[string]string `json:"env"`
		Distribution string            `json:"distribution"`
		Hooks        *Hooks            `json:"hooks,omitempty"`
	}{s.Profile, env, s.Distribution, hooks})
	return string(data)
}

// HooksJSON encodes the resolved hooks as a JSON object of the commands that
// are set, for runner.sh's BUILD_HOOKS; empty when there is none.
func (s *BuildSettings) HooksJSON() string {
	if s.Hooks.Empty() {
		return ""
	}
	data, _ := json.Marshal(s.Hooks) // two strings cannot fail to marshal
	return string(data)
}
