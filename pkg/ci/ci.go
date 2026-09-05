// Package ci exposes Codemagic and Bitrise build providers for library callers.
package ci

import (
	"github.com/MobAI-App/ios-builder/internal/ci"
	"github.com/MobAI-App/ios-builder/pkg/config"
)

type (
	Provider  = ci.Provider
	Request   = ci.Request
	Run       = ci.Run
	Status    = ci.Status
	Artifact  = ci.Artifact
	Codemagic = ci.Codemagic
	Bitrise   = ci.Bitrise
)

func NewCodemagic(cfg config.CIConfig, token string) *Codemagic { return ci.NewCodemagic(cfg, token) }
func NewBitrise(cfg config.CIConfig, token string) *Bitrise     { return ci.NewBitrise(cfg, token) }
