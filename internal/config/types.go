package config

import (
	"fmt"
	"strings"
)

// Config represents the builder.json configuration file
type Config struct {
	Project     string            `json:"project"`
	Platform    string            `json:"platform"`
	GitHub      GitHubConfig      `json:"github"`
	Provider    string            `json:"provider,omitempty"`
	Codemagic   CIConfig          `json:"codemagic,omitempty"`
	Bitrise     CIConfig          `json:"bitrise,omitempty"`
	IOS         IOSConfig         `json:"ios,omitempty"`
	Flutter     FlutterConfig     `json:"flutter,omitempty"`
	ReactNative ReactNativeConfig `json:"reactNative,omitempty"`
	KMP         KMPConfig         `json:"kmp,omitempty"`
	MobAI       MobAIConfig       `json:"mobai,omitempty"`
	// Signing records where `signing setup` put the key, .p12 and profile;
	// nil when it was the working directory.
	Signing *SigningConfig `json:"signing,omitempty"`
	// DefaultProfile is used when a command is run without --profile. Tag-triggered
	// runs have no flags, so it is also the only way they can select a profile.
	DefaultProfile string             `json:"defaultProfile,omitempty"`
	Profiles       map[string]Profile `json:"profiles,omitempty"`
}

// SigningConfig is where the signing material lives on this machine.
type SigningConfig struct {
	// Dir is the --out-dir of the last automatic `signing setup`, as given
	// (a leading ~ is kept); empty means the working directory. On-demand
	// provisioning looks there first for the certificate's private key.
	Dir string `json:"dir,omitempty"`
}

// Profile is a named set of build settings, selected with --profile. Every
// field is optional and overrides the matching top-level setting; unset fields
// keep the top-level value. Runner and submit settings are planned here too.
type Profile struct {
	Configuration string            `json:"configuration,omitempty"` // overrides ios.configuration; derived from distribution when empty
	Scheme        string            `json:"scheme,omitempty"`        // overrides ios.scheme
	Provider      string            `json:"provider,omitempty"`      // overrides provider
	Env           map[string]string `json:"env,omitempty"`           // exported on the runner before dependencies and the build
	// Distribution is the only signing setting of a profile: development,
	// ad-hoc (or internal), store or enterprise. It selects the signing set the
	// runner reads (IOS_*_<SET> secrets, see SigningSet) and the type the
	// provisioning profile in it must have. Empty means an unsigned build.
	Distribution string `json:"distribution,omitempty"`
}

// CIConfig identifies an app already connected to the project's GitHub repository.
// Tokens are read from CODEMAGIC_API_TOKEN or BITRISE_API_TOKEN, never this file.
type CIConfig struct {
	AppID         string `json:"app_id,omitempty"`
	Branch        string `json:"branch,omitempty"`
	BuildWorkflow string `json:"build_workflow,omitempty"`
	ShareWorkflow string `json:"share_workflow,omitempty"`
}

// ProviderName resolves a command override, the project default, then GitHub.
func (c *Config) ProviderName(override string) (string, error) {
	if override == "" {
		override = c.Provider
	}
	if override == "" {
		override = "github"
	}
	switch override {
	case "github", "codemagic", "bitrise":
		return override, nil
	default:
		return "", fmt.Errorf("unknown provider %q (choose github, codemagic, or bitrise)", override)
	}
}

// ProviderConfig validates only the selected provider so other accounts may be incomplete.
func (c *Config) ProviderConfig(name string) (CIConfig, error) {
	var ci CIConfig
	switch name {
	case "codemagic":
		ci = c.Codemagic
	case "bitrise":
		ci = c.Bitrise
	case "github":
		return ci, nil
	default:
		return ci, fmt.Errorf("unknown provider %q", name)
	}
	if strings.TrimSpace(ci.AppID) == "" || strings.TrimSpace(ci.Branch) == "" {
		return ci, fmt.Errorf("%s requires app_id and branch; run builder init --provider %s --app-id <id> --branch <branch>", name, name)
	}
	for _, r := range ci.AppID {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return ci, fmt.Errorf("%s app_id must be an app identifier, not a URL", name)
	}
	if ci.BuildWorkflow == "" {
		ci.BuildWorkflow = "ios-build"
	}
	if ci.ShareWorkflow == "" {
		ci.ShareWorkflow = "ios-share"
	}
	return ci, nil
}

// FlutterConfig holds Flutter-specific settings
type FlutterConfig struct {
	Version string      `json:"version,omitempty"` // Pinned Flutter version (e.g., "3.24.0")
	Watch   WatchConfig `json:"watch,omitempty"`   // File watcher settings for hot reload
}

// WatchConfig holds file watcher settings for hot reload
type WatchConfig struct {
	Dirs     []string `json:"dirs,omitempty"`     // Directories to watch
	Patterns []string `json:"patterns,omitempty"` // File patterns to match (by suffix)
	Ignore   []string `json:"ignore,omitempty"`   // Patterns to ignore (by suffix)
	Debounce int      `json:"debounce,omitempty"` // Debounce ms
}

// ReactNativeConfig holds React Native-specific settings
type ReactNativeConfig struct {
	MetroPort int  `json:"metroPort,omitempty"` // Metro bundler port (default: 8081)
	Expo      bool `json:"expo,omitempty"`      // Whether this is an Expo project
}

// KMPConfig holds Kotlin Multiplatform-specific settings.
// The iOS app is built with xcodebuild, which invokes Gradle (via the Xcode
// "Run Script" build phase, or CocoaPods) to compile the shared Kotlin
// framework. The CI build therefore needs a JDK available for Gradle.
type KMPConfig struct {
	JDKVersion string `json:"jdkVersion,omitempty"` // JDK version for Gradle builds (default: 17)
}

// IOSConfig holds iOS build settings
type IOSConfig struct {
	// Path to iOS project relative to repo root (e.g., "ios" for React Native, "platforms/ios" for Cordova)
	// Empty means root directory contains the Xcode project
	Path   string `json:"path,omitempty"`
	Scheme string `json:"scheme,omitempty"` // Xcode scheme to build (auto-detected if empty)
	// BundleID is the app bundle identifier, for signing setup and to find the
	// App Store Connect app in `ios release` before any IPA exists (detected by
	// init when unambiguous; otherwise the newest IPA in the output directory).
	BundleID      string `json:"bundleId,omitempty"`
	Signing       bool   `json:"signing,omitempty"`       // Legacy: sign builds without a profile with the unsuffixed IOS_* secrets
	Configuration string `json:"configuration,omitempty"` // Build configuration: Debug (faster) or Release (production)
}

// MobAIConfig holds MobAI settings for local development
type MobAIConfig struct {
	URL      string `json:"url,omitempty"`       // MobAI API URL (default: http://localhost:8686)
	DeviceID string `json:"device_id,omitempty"` // Preferred device ID (default: first available)
}

// GitHubConfig holds GitHub repository settings
type GitHubConfig struct {
	Owner string `json:"owner"`
	Repo  string `json:"repo"`
}

// Validate checks if the configuration is valid
func (c *Config) Validate() error {
	if c.Project == "" {
		return &ValidationError{Field: "project", Message: "project name is required"}
	}
	if c.GitHub.Owner == "" {
		return &ValidationError{Field: "github.owner", Message: "GitHub owner is required"}
	}
	if c.GitHub.Repo == "" {
		return &ValidationError{Field: "github.repo", Message: "GitHub repo is required"}
	}
	return nil
}

// ValidationError represents a configuration validation error
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return "config validation error: " + e.Field + ": " + e.Message
}
