// Package github provides a client for the GitHub REST API.
// It supports repository operations, workflow dispatch, and artifact downloads.
package github

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"golang.org/x/crypto/nacl/box"
)

const (
	// BaseURL is the GitHub API base URL
	BaseURL = "https://api.github.com"

	// APIVersion is the GitHub API version header value
	APIVersion = "2022-11-28"
)

// Client is a GitHub API client
type Client struct {
	httpClient *http.Client
	token      string
	baseURL    string
}

// NewClient creates a new GitHub API client
func NewClient(token string) *Client {
	return &Client{
		httpClient: &http.Client{},
		token:      token,
		baseURL:    BaseURL,
	}
}

// request performs an HTTP request to the GitHub API
func (c *Client) request(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(jsonBody)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", APIVersion)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	return resp, nil
}

// do performs a GET request and decodes the response into result
func (c *Client) do(ctx context.Context, path string, result any) error {
	resp, err := c.request(ctx, "GET", path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		var apiErr APIError
		if err := json.Unmarshal(respBody, &apiErr); err != nil {
			return fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(respBody))
		}
		if apiErr.Status == "" { // GitHub sends it as a string; keep it so even when it does not
			apiErr.Status = strconv.Itoa(resp.StatusCode)
		}
		return &apiErr
	}

	if result != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, result); err != nil {
			return fmt.Errorf("failed to decode response: %w", err)
		}
	}

	return nil
}

// EncryptSecret encrypts a value using NaCl sealed box for GitHub secrets.
// The publicKey should be base64-encoded (as returned by GetPublicKey).
func EncryptSecret(publicKey, value string) (string, error) {
	// Decode the public key
	keyBytes, err := base64.StdEncoding.DecodeString(publicKey)
	if err != nil {
		return "", fmt.Errorf("failed to decode public key: %w", err)
	}

	if len(keyBytes) != 32 {
		return "", fmt.Errorf("invalid public key length: expected 32, got %d", len(keyBytes))
	}

	var recipientKey [32]byte
	copy(recipientKey[:], keyBytes)

	// Encrypt using NaCl sealed box (anonymous sender)
	encrypted, err := box.SealAnonymous(nil, []byte(value), &recipientKey, rand.Reader)
	if err != nil {
		return "", fmt.Errorf("failed to encrypt: %w", err)
	}

	return base64.StdEncoding.EncodeToString(encrypted), nil
}
