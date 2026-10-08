package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/MobAI-App/ios-builder/internal/exitcode"
)

// RequiredScopes are the OAuth scopes Builder asks for in the device flow:
// repo (snapshot refs, secrets, releases), workflow (the workflow files) and
// gist (ios distribute manifests).
var RequiredScopes = []string{"repo", "workflow", "gist"}

// githubAPI is the REST API root; tests point it at a fake server.
var githubAPI = "https://api.github.com"

// GitHubIdentity is what GitHub says about a token.
type GitHubIdentity struct {
	Login string
	// Scopes are the token's OAuth scopes. ScopesKnown is false when GitHub
	// does not report them, as for fine-grained tokens, whose permissions
	// cannot be read back.
	Scopes      []string
	ScopesKnown bool
}

// CheckGitHubToken asks GitHub who the token belongs to and which scopes it
// carries. A token GitHub rejects is an exitcode.Auth error.
func CheckGitHubToken(ctx context.Context, token string) (*GitHubIdentity, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubAPI+"/user", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("check the GitHub token: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, exitcode.With(exitcode.Auth, fmt.Errorf("GitHub rejected the token (HTTP 401): it is wrong, expired or revoked"))
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("check the GitHub token: HTTP %d", resp.StatusCode)
	}
	var user struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, fmt.Errorf("check the GitHub token: %w", err)
	}
	id := &GitHubIdentity{Login: user.Login}
	if values, ok := resp.Header[http.CanonicalHeaderKey("X-OAuth-Scopes")]; ok {
		id.ScopesKnown = true
		id.Scopes = ParseScopes(strings.Join(values, ","))
	}
	return id, nil
}

// ParseScopes splits a scope list as GitHub writes it, comma or space
// separated.
func ParseScopes(s string) []string {
	return append([]string{}, strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' })...)
}

// MissingScopes returns the RequiredScopes absent from scopes.
func MissingScopes(scopes []string) []string {
	missing := []string{}
	for _, want := range RequiredScopes {
		if !slices.Contains(scopes, want) {
			missing = append(missing, want)
		}
	}
	return missing
}

// SaveToken stores a GitHub token where GetToken finds it.
func SaveToken(token string) error {
	return storeToken(token)
}
