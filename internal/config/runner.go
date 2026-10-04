package config

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// DefaultRunner is the GitHub Actions runner a workflow runs on when neither
// builder.json nor the profile names one.
const DefaultRunner = "macos-latest"

// Default machines of the other providers, as the templates hardcoded them.
const (
	DefaultCodemagicInstance = "mac_mini_m2"
	DefaultBitriseMachine    = "g2.mac.medium"
)

// Runner is the GitHub Actions `runs-on` of a build: one label
// ("macos-latest", "macos-15", "self-hosted") or several that a runner must
// all carry (["self-hosted", "macOS", "ARM64"]). In builder.json it is a
// string or an array of strings.
type Runner []string

// runnerLabelRe keeps labels to what can be put into YAML and a workflow
// expression unquoted and unescaped.
var runnerLabelRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// UnmarshalJSON accepts "label" or ["label", ...]; an empty string is no runner.
func (r *Runner) UnmarshalJSON(data []byte) error {
	var one string
	if err := json.Unmarshal(data, &one); err == nil {
		if one == "" {
			*r = nil
		} else {
			*r = Runner{one}
		}
		return nil
	}
	var many []string
	if err := json.Unmarshal(data, &many); err != nil {
		return fmt.Errorf("runner must be a label or an array of labels")
	}
	*r = Runner(many)
	return nil
}

// MarshalJSON writes a single label as a string, several as an array.
func (r Runner) MarshalJSON() ([]byte, error) {
	if len(r) == 1 {
		return json.Marshal(r[0])
	}
	return json.Marshal([]string(r))
}

// ParseRunner reads the --runner flag: one label or a comma-separated list.
func ParseRunner(s string) (Runner, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var r Runner
	for _, label := range strings.Split(s, ",") {
		r = append(r, strings.TrimSpace(label))
	}
	return r, r.Validate()
}

// Validate checks that every label is non-empty and plain.
func (r Runner) Validate() error {
	for _, label := range r {
		if !runnerLabelRe.MatchString(label) {
			return fmt.Errorf("runner label %q must be letters, digits, '.', '_' or '-'", label)
		}
	}
	return nil
}

// SelfHosted reports whether the labels select a self-hosted runner.
func (r Runner) SelfHosted() bool {
	return slices.ContainsFunc(r, func(l string) bool { return strings.EqualFold(l, "self-hosted") })
}

// String is the comma-separated form --runner takes; empty is DefaultRunner.
func (r Runner) String() string {
	if len(r) == 0 {
		return DefaultRunner
	}
	return strings.Join(r, ",")
}

// Warning is a note for labels that are valid but unlikely to build iOS:
// a GitHub-hosted runner that is not macOS. Empty when there is nothing to say.
func (r Runner) Warning() string {
	if len(r) == 0 || r.SelfHosted() {
		return ""
	}
	if len(r) == 1 && strings.HasPrefix(strings.ToLower(r[0]), "macos") {
		return ""
	}
	return fmt.Sprintf("runner %q is not a GitHub-hosted macOS image (macos-*) and does not include self-hosted; iOS builds need macOS with Xcode", r.String())
}

// Known machines of the other providers. Others are passed through with a
// warning, since both services add types over time.
var (
	knownCodemagicInstances = []string{"mac_mini_m1", "mac_mini_m2", "mac_mini_m4", "mac_pro"}
	knownBitriseMachines    = []string{
		"g2.mac.medium", "g2.mac.large", "g2.mac.x-large", "g2.mac.4large",
		"g2-m1.4core", "g2-m1.8core", "g2-m1-max.5core", "g2-m1-max.10core",
	}
	machineRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// CheckMachine validates a Codemagic instance_type, Bitrise machine_type_id or
// Bitrise stack before it is written into a CI file. It returns a warning for a
// value Builder does not know, and an error for one that is not a plain name.
func CheckMachine(provider, field, value string) (warning string, err error) {
	if value == "" {
		return "", nil
	}
	if !machineRe.MatchString(value) {
		return "", fmt.Errorf("%s %s %q must be letters, digits, '.', '_' or '-'", provider, field, value)
	}
	var known []string
	switch {
	case provider == "codemagic" && field == "instance_type":
		known = knownCodemagicInstances
	case provider == "bitrise" && field == "machine_type_id":
		known = knownBitriseMachines
	default:
		return "", nil
	}
	if slices.Contains(known, value) {
		return "", nil
	}
	return fmt.Sprintf("%s %s %q is not one Builder knows (%s); using it as given", provider, field, value, strings.Join(known, ", ")), nil
}

// RunnerName is what a build on the provider runs on, for the build banner:
// the resolved GitHub runner, the Codemagic instance type, or the Bitrise
// machine type (with its stack when set).
func (c *Config) RunnerName(provider string, s *BuildSettings) string {
	switch provider {
	case "codemagic":
		if c.Codemagic.InstanceType != "" {
			return c.Codemagic.InstanceType
		}
		return DefaultCodemagicInstance
	case "bitrise":
		m := c.Bitrise.MachineTypeID
		if m == "" {
			m = DefaultBitriseMachine
		}
		if c.Bitrise.Stack != "" {
			m += " (" + c.Bitrise.Stack + ")"
		}
		return m
	default:
		if s != nil && len(s.Runner) > 0 {
			return s.Runner.String()
		}
		return DefaultRunner
	}
}
