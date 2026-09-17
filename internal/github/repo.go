package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

// GetRepository retrieves a repository by owner and name
func (c *Client) GetRepository(ctx context.Context, owner, repo string) (*Repository, error) {
	path := fmt.Sprintf("/repos/%s/%s", owner, repo)

	var repository Repository
	if err := c.do(ctx, path, &repository); err != nil {
		return nil, fmt.Errorf("failed to get repository: %w", err)
	}

	return &repository, nil
}

// GetPublicKey retrieves the repository's public key for encrypting secrets
func (c *Client) GetPublicKey(ctx context.Context, owner, repo string) (*PublicKey, error) {
	path := fmt.Sprintf("/repos/%s/%s/actions/secrets/public-key", owner, repo)

	var key PublicKey
	if err := c.do(ctx, path, &key); err != nil {
		return nil, fmt.Errorf("failed to get public key: %w", err)
	}

	return &key, nil
}

// ListSecretNames returns the names of the repository's Actions secrets
// (values are never readable), following every page. GitHub answers 404
// (token without the repo scope) or 403 (no admin access) rather than an
// empty list, so those name the cause instead of reading as "no secrets".
func (c *Client) ListSecretNames(ctx context.Context, owner, repo string) ([]string, error) {
	var names []string
	for page := 1; ; page++ {
		path := fmt.Sprintf("/repos/%s/%s/actions/secrets?per_page=100&page=%d", owner, repo, page)
		var list SecretsResponse
		if err := c.do(ctx, path, &list); err != nil {
			var apiErr *APIError
			if errors.As(err, &apiErr) && (apiErr.Status == "403" || apiErr.Status == "404") {
				return nil, fmt.Errorf("cannot list the secrets of %s/%s (%s): the GitHub token needs the repo scope and admin access to the repository; run builder auth github as an admin of it", owner, repo, apiErr.Message)
			}
			return nil, fmt.Errorf("failed to list the secrets of %s/%s: %w", owner, repo, err)
		}
		for _, s := range list.Secrets {
			names = append(names, s.Name)
		}
		if len(list.Secrets) == 0 || len(names) >= list.TotalCount {
			return names, nil
		}
	}
}

// CreateOrUpdateSecret creates or updates a repository secret
// The value should be encrypted using the repository's public key
func (c *Client) CreateOrUpdateSecret(ctx context.Context, owner, repo, name, encryptedValue, keyID string) error {
	path := fmt.Sprintf("/repos/%s/%s/actions/secrets/%s", owner, repo, name)

	req := CreateSecretRequest{
		EncryptedValue: encryptedValue,
		KeyID:          keyID,
	}

	resp, err := c.request(ctx, "PUT", path, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("failed to create secret: status %d", resp.StatusCode)
	}

	return nil
}
