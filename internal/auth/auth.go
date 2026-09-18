// Package auth provides GitHub OAuth authentication via Device Code flow.
// Tokens are stored in the OS keychain (macOS/Windows) or file-based storage (Linux/WSL).
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/zalando/go-keyring"
)

const (
	clientID = "Ov23livITjYL1eNTuE7a"

	keyringService = "builder-cli"
	keyringUser    = "github-token"

	configDirName = "ios-builder"
	tokenFileName = "token"
)

// Token represents a GitHub OAuth access token.
type Token struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`
}

// DeviceCode represents the response from GitHub's device code request.
type DeviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// Login performs GitHub OAuth Device Code flow authentication.
// It displays a URL and code for the user to authorize, then stores the token in the keychain.
func Login(ctx context.Context) (*Token, error) {
	code, err := requestDeviceCode(ctx)
	if err != nil {
		return nil, err
	}

	fmt.Println()
	fmt.Printf("  Open: %s\n", code.VerificationURI)
	fmt.Printf("  Enter code: %s\n", code.UserCode)
	fmt.Println()
	fmt.Println("Waiting for authorization...")

	token, err := pollForToken(ctx, code)
	if err != nil {
		return nil, err
	}
	fmt.Println("Authorized. Saving token...")

	if err := storeToken(token.AccessToken); err != nil {
		return nil, fmt.Errorf("failed to store token: %w", err)
	}

	return token, nil
}

// ErrNotAuthenticated indicates no stored authentication token was found.
var ErrNotAuthenticated = errors.New("not authenticated")

// GetToken retrieves the stored GitHub token from the OS keychain or file storage.
func GetToken() (string, error) {
	// Skip the keyring on Linux/WSL - it needs a desktop Secret Service that
	// is usually absent there, and the call can hang.
	if runtime.GOOS != "linux" {
		if token, err := keyring.Get(keyringService, keyringUser); err == nil {
			return token, nil
		}
	}
	token, err := getTokenFromFile()
	if err == nil {
		return token, nil
	}
	if errors.Is(err, ErrNotAuthenticated) {
		return "", ErrNotAuthenticated
	}
	return "", fmt.Errorf("failed to get token: %w", err)
}

// Logout removes the stored GitHub token from keychain and file storage.
func Logout() error {
	if runtime.GOOS != "linux" {
		if err := keyring.Delete(keyringService, keyringUser); err != nil && err != keyring.ErrNotFound {
			fmt.Fprintf(os.Stderr, "Warning: failed to delete from keyring: %v\n", err)
		}
	}
	if err := deleteTokenFile(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to delete token file: %v\n", err)
	}
	return nil
}

// storeToken stores the token in the OS keychain, or a file on Linux/WSL.
//
// The keyring needs a desktop Secret Service, which WSL and headless Linux
// lack; there the call can hang rather than error, so it is skipped entirely.
func storeToken(token string) error {
	if runtime.GOOS == "linux" {
		return storeTokenToFile(token)
	}
	if err := keyring.Set(keyringService, keyringUser, token); err == nil {
		return nil
	}
	return storeTokenToFile(token)
}

// getConfigDir returns the path to ~/.config/ios-builder/
func getConfigDir() (string, error) {
	var configBase string
	if runtime.GOOS == "windows" {
		configBase = os.Getenv("APPDATA")
		if configBase == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			configBase = filepath.Join(home, "AppData", "Roaming")
		}
	} else {
		configBase = os.Getenv("XDG_CONFIG_HOME")
		if configBase == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			configBase = filepath.Join(home, ".config")
		}
	}
	return filepath.Join(configBase, configDirName), nil
}

// storeTokenToFile saves the token to ~/.config/ios-builder/token with restricted permissions.
func storeTokenToFile(token string) error {
	configDir, err := getConfigDir()
	if err != nil {
		return fmt.Errorf("failed to get config directory: %w", err)
	}
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}
	tokenPath := filepath.Join(configDir, tokenFileName)
	if err := os.WriteFile(tokenPath, []byte(token), 0600); err != nil {
		return fmt.Errorf("failed to write token file: %w", err)
	}
	return nil
}

// getTokenFromFile reads the token from ~/.config/ios-builder/token.
func getTokenFromFile() (string, error) {
	configDir, err := getConfigDir()
	if err != nil {
		return "", fmt.Errorf("failed to get config directory: %w", err)
	}
	tokenPath := filepath.Join(configDir, tokenFileName)
	data, err := os.ReadFile(tokenPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrNotAuthenticated
		}
		return "", fmt.Errorf("failed to read token file: %w", err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", ErrNotAuthenticated
	}
	return token, nil
}

// deleteTokenFile removes the token file.
func deleteTokenFile() error {
	configDir, err := getConfigDir()
	if err != nil {
		return err
	}
	tokenPath := filepath.Join(configDir, tokenFileName)
	err = os.Remove(tokenPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func requestDeviceCode(ctx context.Context) (*DeviceCode, error) {
	data := url.Values{
		"client_id": {clientID},
		"scope":     {"repo workflow gist"},
	}

	req, err := http.NewRequestWithContext(ctx, "POST", "https://github.com/login/device/code", strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to request device code: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("device code request failed: status %d", resp.StatusCode)
	}

	var code DeviceCode
	if err := json.NewDecoder(resp.Body).Decode(&code); err != nil {
		return nil, fmt.Errorf("failed to parse device code response: %w", err)
	}

	if code.DeviceCode == "" {
		return nil, fmt.Errorf("empty device code in response")
	}

	return &code, nil
}

func pollForToken(ctx context.Context, code *DeviceCode) (*Token, error) {
	interval := time.Duration(code.Interval) * time.Second
	if interval == 0 {
		interval = 5 * time.Second
	}

	deadline := time.Now().Add(time.Duration(code.ExpiresIn) * time.Second)

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}

		if time.Now().After(deadline) {
			return nil, fmt.Errorf("authorization timed out")
		}

		token, err := requestToken(ctx, code.DeviceCode)
		if err == nil {
			return token, nil
		}

		if strings.Contains(err.Error(), "authorization_pending") {
			continue
		}
		if strings.Contains(err.Error(), "slow_down") {
			interval += 5 * time.Second
			continue
		}

		return nil, err
	}
}

func requestToken(ctx context.Context, deviceCode string) (*Token, error) {
	data := url.Values{
		"client_id":   {clientID},
		"device_code": {deviceCode},
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
	}

	req, err := http.NewRequestWithContext(ctx, "POST", "https://github.com/login/oauth/access_token", strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to request token: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Token
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to parse token response: %w", err)
	}

	if result.Error != "" {
		return nil, fmt.Errorf("%s: %s", result.Error, result.ErrorDescription)
	}

	if result.AccessToken == "" {
		return nil, fmt.Errorf("empty access token in response")
	}

	return &result.Token, nil
}
