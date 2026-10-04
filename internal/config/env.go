package config

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// secretNameRe is stricter than envNameRe: GitHub stores secret names upper
// case whatever was sent, and a double underscore is the separator between a
// secret and the profile it belongs to (SecretStorageName).
var secretNameRe = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// ValidateEnvName checks a plain env name: a shell variable name the runners
// do not own.
func ValidateEnvName(name string) error {
	if !envNameRe.MatchString(name) {
		return fmt.Errorf("env name %q is not a valid environment variable name", name)
	}
	if reservedEnvName(name) {
		return fmt.Errorf("env name %q is reserved for the runner", name)
	}
	return nil
}

// ValidateSecretName checks a secret name: upper case letters, digits and
// single underscores, and none the runners own (so a secret cannot shadow
// IOS_CERTIFICATE_*, MOBAI_API_KEY, GITHUB_* and the like).
func ValidateSecretName(name string) error {
	if !secretNameRe.MatchString(name) {
		return fmt.Errorf("secret name %q must be upper case letters, digits and underscores, not starting with a digit", name)
	}
	if strings.Contains(name, "__") {
		return fmt.Errorf("secret name %q must not contain a double underscore, which separates a secret from its profile", name)
	}
	if reservedEnvName(name) {
		return fmt.Errorf("secret name %q is reserved for the runner", name)
	}
	return nil
}

// SecretSuffix is the provider-side suffix of a profile's secrets: the
// profile name upper-cased with every other character than A-Z and 0-9
// replaced by an underscore. The runners compute the same.
func SecretSuffix(profile string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(profile) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// SecretStorageName is the name a secret's value is stored under on the
// provider: the name itself for every build, NAME__<SUFFIX> for a value only
// the profile's builds use. On the runner a profile's builds take NAME__<SUFFIX>
// when it exists and NAME otherwise, and export it as NAME.
func SecretStorageName(name, profile string) string {
	if profile == "" {
		return name
	}
	return name + "__" + SecretSuffix(profile)
}

// resolveEnv merges the top-level env and secrets with the named profile's
// (none for an empty name) and validates every name involved.
func (c *Config) resolveEnv(profile string) (map[string]string, []string, error) {
	env := map[string]string{}
	var secrets []string
	add := func(scope string, e map[string]string, s []string) error {
		for k, v := range e {
			if err := ValidateEnvName(k); err != nil {
				return fmt.Errorf("%s: %w", scope, err)
			}
			env[k] = v
		}
		for _, n := range s {
			if err := ValidateSecretName(n); err != nil {
				return fmt.Errorf("%s: %w", scope, err)
			}
			secrets = append(secrets, n)
		}
		return nil
	}
	if err := add("builder.json", c.Env, c.Secrets); err != nil {
		return nil, nil, err
	}
	scope := "builder.json"
	if profile != "" {
		scope = fmt.Sprintf("profile %q", profile)
		p := c.Profiles[profile]
		if err := add(scope, p.Env, p.Secrets); err != nil {
			return nil, nil, err
		}
	}
	slices.Sort(secrets)
	secrets = slices.Compact(secrets)
	for _, n := range secrets {
		if _, ok := env[n]; ok {
			return nil, nil, fmt.Errorf("%s: %s is both a plain env value and a secret; remove one (builder env unset or builder secret unset)", scope, n)
		}
	}
	if len(env) == 0 {
		env = nil
	}
	return env, secrets, nil
}

// CheckEnv validates the env and secrets of the top level and of every
// profile, the way a build with each of them would.
func (c *Config) CheckEnv() error {
	if _, _, err := c.resolveEnv(""); err != nil {
		return err
	}
	for _, name := range c.ProfileNames() {
		if _, _, err := c.resolveEnv(name); err != nil {
			return err
		}
	}
	return nil
}

func (c *Config) checkProfile(profile string) error {
	if profile == "" {
		return nil
	}
	if _, ok := c.Profiles[profile]; !ok {
		if len(c.Profiles) == 0 {
			return fmt.Errorf("profile %q is not defined; builder.json has no profiles", profile)
		}
		return fmt.Errorf("profile %q is not defined; available profiles: %s", profile, strings.Join(c.ProfileNames(), ", "))
	}
	return nil
}

// EnvOf returns the env set at one level: the top level for an empty profile.
func (c *Config) EnvOf(profile string) map[string]string {
	if profile == "" {
		return c.Env
	}
	return c.Profiles[profile].Env
}

// SecretsOf returns the secret names listed at one level.
func (c *Config) SecretsOf(profile string) []string {
	if profile == "" {
		return c.Secrets
	}
	return c.Profiles[profile].Secrets
}

// SetEnv sets a plain env value at the top level or in a profile. The config
// is left unchanged when the result would not validate.
func (c *Config) SetEnv(profile, name, value string) error {
	if err := c.checkProfile(profile); err != nil {
		return err
	}
	if err := ValidateEnvName(name); err != nil {
		return err
	}
	next := maps.Clone(c.EnvOf(profile))
	if next == nil {
		next = map[string]string{}
	}
	next[name] = value
	return c.update(profile, func(p *Profile) { p.Env = next }, func() { c.Env = next })
}

// UnsetEnv removes a plain env value; false when it was not set there.
func (c *Config) UnsetEnv(profile, name string) (bool, error) {
	if err := c.checkProfile(profile); err != nil {
		return false, err
	}
	cur := c.EnvOf(profile)
	if _, ok := cur[name]; !ok {
		return false, nil
	}
	next := maps.Clone(cur)
	delete(next, name)
	if len(next) == 0 {
		next = nil
	}
	return true, c.update(profile, func(p *Profile) { p.Env = next }, func() { c.Env = next })
}

// AddSecret lists a secret name at the top level or in a profile, keeping
// the list sorted; adding a listed name changes nothing.
func (c *Config) AddSecret(profile, name string) error {
	if err := c.checkProfile(profile); err != nil {
		return err
	}
	if err := ValidateSecretName(name); err != nil {
		return err
	}
	cur := c.SecretsOf(profile)
	if slices.Contains(cur, name) {
		return c.CheckEnv()
	}
	next := append(slices.Clone(cur), name)
	slices.Sort(next)
	return c.update(profile, func(p *Profile) { p.Secrets = next }, func() { c.Secrets = next })
}

// RemoveSecret drops a secret name from one level; false when it was not listed.
func (c *Config) RemoveSecret(profile, name string) (bool, error) {
	if err := c.checkProfile(profile); err != nil {
		return false, err
	}
	cur := c.SecretsOf(profile)
	i := slices.Index(cur, name)
	if i < 0 {
		return false, nil
	}
	next := slices.Delete(slices.Clone(cur), i, i+1)
	if len(next) == 0 {
		next = nil
	}
	return true, c.update(profile, func(p *Profile) { p.Secrets = next }, func() { c.Secrets = next })
}

// update applies a change to a profile or the top level, then validates every
// level and rolls the change back when one fails.
func (c *Config) update(profile string, inProfile func(*Profile), topLevel func()) error {
	if profile == "" {
		env, secrets := c.Env, c.Secrets
		topLevel()
		if err := c.CheckEnv(); err != nil {
			c.Env, c.Secrets = env, secrets
			return err
		}
		return nil
	}
	old := c.Profiles[profile]
	p := old
	inProfile(&p)
	c.Profiles[profile] = p
	if err := c.CheckEnv(); err != nil {
		c.Profiles[profile] = old
		return err
	}
	return nil
}
