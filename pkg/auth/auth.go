// Package auth exposes GitHub token storage to code outside this module.
//
// GetToken reads the token ios-builder stored; a caller holding a token from
// elsewhere can skip this package entirely and hand it to github.NewClient.
package auth

import (
	"context"

	"github.com/MobAI-App/ios-builder/internal/auth"
)

// ErrNotAuthenticated indicates no stored authentication token was found.
var ErrNotAuthenticated = auth.ErrNotAuthenticated

type Token = auth.Token

// Login performs GitHub OAuth Device Code flow authentication, printing the
// verification URL and code to stdout, and stores the resulting token.
func Login(ctx context.Context) (*Token, error) {
	return auth.Login(ctx)
}

// GetToken retrieves the stored GitHub token from the OS keychain or file storage.
func GetToken() (string, error) {
	return auth.GetToken()
}

// Logout removes the stored GitHub token.
func Logout() error {
	return auth.Logout()
}

func GetProviderToken(provider string) (string, error) { return auth.GetProviderToken(provider) }
func StoreProviderToken(provider, token string) error {
	return auth.StoreProviderToken(provider, token)
}
func LogoutProvider(provider string) error { return auth.LogoutProvider(provider) }

// App Store Connect API keys. A program with its own credential store keeps
// AppleCredentialsFromEnv and NormalizePEM and skips the stored login.
type (
	AppleCredentials = auth.AppleCredentials
	AppleSource      = auth.AppleSource
)

const (
	AppleSourceEnv    = auth.AppleSourceEnv
	AppleSourceStored = auth.AppleSourceStored
)

// GetAppleCredentials returns the key from the ASC_* environment, else the
// login saved by StoreAppleCredentials.
func GetAppleCredentials() (*AppleCredentials, AppleSource, error) {
	return auth.GetAppleCredentials()
}

// AppleCredentialsFromEnv reads ASC_ISSUER_ID, ASC_KEY_ID and ASC_PRIVATE_KEY
// or ASC_KEY_PATH: nil and no error when none is set, an error when only
// some are.
func AppleCredentialsFromEnv() (*AppleCredentials, error) {
	return auth.AppleCredentialsFromEnv()
}

// StoreAppleCredentials saves the API key as the Apple login.
func StoreAppleCredentials(c AppleCredentials) error { return auth.StoreAppleCredentials(c) }

// NormalizePEM accepts a key pasted with literal "\n" sequences and returns
// it with real newlines.
func NormalizePEM(key string) string { return auth.NormalizePEM(key) }
