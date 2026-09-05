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
