package ci

import (
	"context"
	"fmt"
)

// ValidateToken checks read access without starting a build or changing an account.
func ValidateToken(ctx context.Context, name, token string) error {
	var api apiClient
	var endpoint string
	switch name {
	case "codemagic":
		api = newAPI(token, "x-auth-token")
		endpoint = "https://api.codemagic.io/apps"
	case "bitrise":
		api = newAPI(token, "Authorization")
		endpoint = "https://api.bitrise.io/v0.1/apps?limit=1"
	default:
		return fmt.Errorf("unknown provider %q", name)
	}
	return api.request(ctx, "GET", endpoint, nil, nil)
}
