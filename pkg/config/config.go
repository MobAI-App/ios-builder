// Package config exposes builder.json handling to code outside this module.
//
// The implementation lives in internal/config, which Go forbids other modules
// from importing. This package re-exports it so ios-builder can be consumed as
// a library. Types are aliases, not wrappers, so values pass between the two
// packages without conversion.
package config

import "github.com/MobAI-App/ios-builder/internal/config"

// ConfigFileName is the default configuration file name.
const ConfigFileName = config.ConfigFileName

// ErrConfigNotFound indicates builder.json was not found.
var ErrConfigNotFound = config.ErrConfigNotFound

type (
	Config            = config.Config
	GitHubConfig      = config.GitHubConfig
	CIConfig          = config.CIConfig
	IOSConfig         = config.IOSConfig
	FlutterConfig     = config.FlutterConfig
	WatchConfig       = config.WatchConfig
	ReactNativeConfig = config.ReactNativeConfig
	MobAIConfig       = config.MobAIConfig
	ValidationError   = config.ValidationError
	Manager           = config.Manager
)

// NewManager creates a configuration manager rooted at builder.json.
func NewManager() *Manager {
	return config.NewManager()
}
