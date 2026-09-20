// Package signing exposes portal-free provisioning and signing sets to code
// outside this module: Auto creates bundle IDs, certificates, devices and
// profiles through App Store Connect; Setup and EnsureSecrets turn that
// material into the CI secrets of one distribution.
package signing

import (
	"context"
	"io"

	"github.com/MobAI-App/ios-builder/internal/signing"
	"github.com/MobAI-App/ios-builder/pkg/asc"
	"github.com/MobAI-App/ios-builder/pkg/config"
)

type (
	Type              = signing.Type
	Device            = signing.Device
	AutoOptions       = signing.AutoOptions
	AutoResult        = signing.AutoResult
	BundleIDResult    = signing.BundleIDResult
	CertificateResult = signing.CertificateResult
	DevicesResult     = signing.DevicesResult
	ProfileResult     = signing.ProfileResult
	ExtensionResult   = signing.ExtensionResult
	Files             = signing.Files

	SecretStore   = signing.SecretStore
	Commands      = signing.Commands
	SetupOptions  = signing.SetupOptions
	SetupResult   = signing.SetupResult
	EnsureOptions = signing.EnsureOptions
)

const (
	TypeDevelopment = signing.TypeDevelopment
	TypeAdHoc       = signing.TypeAdHoc
	TypeStore       = signing.TypeStore
	TypeEnterprise  = signing.TypeEnterprise

	LegacyKeyFileName = signing.LegacyKeyFileName
)

// BuilderCommands are the builder CLI's own verbs, what error messages name
// unless EnsureOptions.Commands says otherwise.
var BuilderCommands = signing.BuilderCommands

// ParseType accepts a distribution name (development, ad-hoc or internal,
// store, enterprise).
func ParseType(s string) (Type, error) { return signing.ParseType(s) }

// Auto provisions bundle IDs, a certificate, devices and profiles for one
// distribution; idempotent, and it never revokes anything.
func Auto(ctx context.Context, client *asc.Client, opts *AutoOptions) (*AutoResult, error) {
	return signing.Auto(ctx, client, opts)
}

// Setup provisions a signing set, uploads it to the repository in cfg and
// records the profile in builder.json: `builder signing setup` without its
// prompts and summary.
func Setup(ctx context.Context, client *asc.Client, store SecretStore, storeErr error, cfg *config.Config, opts *SetupOptions) (*SetupResult, error) {
	return signing.Setup(ctx, client, store, storeErr, cfg, opts)
}

// EnsureSecrets provisions a missing or partial signing set before a build
// is dispatched, so a distribution build never fails on the runner for want
// of secrets. A nil error with nothing logged means the set was complete.
func EnsureSecrets(ctx context.Context, cfg *config.Config, store SecretStore, ascClient func() (*asc.Client, error), opts *EnsureOptions) error {
	return signing.EnsureSecrets(ctx, cfg, store, ascClient, opts)
}

// MissingSecrets names the secrets of a set that the repository does not hold.
func MissingSecrets(ctx context.Context, store SecretStore, cfg *config.Config, set string) ([]string, error) {
	return signing.MissingSecrets(ctx, store, cfg, set)
}

// SyncExtensions appends the extension targets of the local Xcode project
// that ios.extensions does not list yet and returns the new ones.
func SyncExtensions(cfg *config.Config, log io.Writer) []string {
	return signing.SyncExtensions(cfg, log)
}

// ConfiguredBundleID is ios.bundleId, else the bundle ID of the newest IPA
// in ./dist; empty when neither is there.
func ConfiguredBundleID(cfg *config.Config, log io.Writer) string {
	return signing.ConfiguredBundleID(cfg, log)
}

// ReadKey returns the key at keyPath, else the first key file of the type
// in dirs, else nil so a key is generated.
func ReadKey(keyPath string, typ Type, dirs ...string) (keyPEM []byte, path string, err error) {
	return signing.ReadKey(keyPath, typ, dirs...)
}

// KeyDirs is where provisioning looks for the private key and writes the
// material.
func KeyDirs(cfg *config.Config) []string { return signing.KeyDirs(cfg) }

// KeyFileName is the private key file of a distribution, ios-signing-<type>.key.
func KeyFileName(t Type) string { return signing.KeyFileName(t) }

// P12FileName is the certificate file of a distribution, ios-signing-<type>.p12.
func P12FileName(t Type) string { return signing.P12FileName(t) }

// ExpandPath normalizes a path typed at a prompt (~, quotes, escaped spaces).
func ExpandPath(path string) string { return signing.ExpandPath(path) }

// RandomPassword is 128 bits of randomness as URL-safe base64.
func RandomPassword() (string, error) { return signing.RandomPassword() }

// CertificateRefused reports App Store Connect's 409 on a certificate
// request: the team already holds one of that type.
func CertificateRefused(err error) bool { return signing.CertificateRefused(err) }

// PrintFiles lists what Auto wrote and, when Builder made it up, the .p12
// password.
func PrintFiles(w io.Writer, res *AutoResult, generatedPassword string) {
	signing.PrintFiles(w, res, generatedPassword)
}

// ProfileType reads a .mobileprovision and reports its distribution.
func ProfileType(data []byte) (Type, error) { return signing.ProfileType(data) }
