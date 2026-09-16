package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// appleSecretName is the keyring entry / fallback file holding the ASC API key.
const appleSecretName = "apple-asc-key"

// AppleCredentials is an App Store Connect API key (Users and Access → Integrations).
type AppleCredentials struct {
	IssuerID   string `json:"issuer_id"`
	KeyID      string `json:"key_id"`
	PrivateKey string `json:"private_key"` // .p8 contents, PEM
}

// AppleSource says where GetAppleCredentials found the key.
type AppleSource string

const (
	// AppleSourceEnv means the ASC_* environment variables were used.
	AppleSourceEnv AppleSource = "environment"
	// AppleSourceStored means the login saved by `builder auth apple` was used.
	AppleSourceStored AppleSource = "stored"
)

// GetAppleCredentials returns the App Store Connect API key. The environment
// (ASC_ISSUER_ID, ASC_KEY_ID and ASC_PRIVATE_KEY or ASC_KEY_PATH) takes
// precedence over the saved login so CI jobs and agents need no keychain.
func GetAppleCredentials() (*AppleCredentials, AppleSource, error) {
	creds, err := appleCredentialsFromEnv()
	if err != nil {
		return nil, "", err
	}
	if creds != nil {
		return creds, AppleSourceEnv, nil
	}
	raw, err := readSecret(appleSecretName)
	if err != nil {
		return nil, "", err
	}
	var stored AppleCredentials
	if err := json.Unmarshal([]byte(raw), &stored); err != nil || stored.IssuerID == "" || stored.KeyID == "" || stored.PrivateKey == "" {
		return nil, "", errors.New("saved Apple login is unreadable; run builder auth apple again")
	}
	return &stored, AppleSourceStored, nil
}

func appleCredentialsFromEnv() (*AppleCredentials, error) {
	issuer := strings.TrimSpace(os.Getenv("ASC_ISSUER_ID"))
	keyID := strings.TrimSpace(os.Getenv("ASC_KEY_ID"))
	key := os.Getenv("ASC_PRIVATE_KEY")
	path := strings.TrimSpace(os.Getenv("ASC_KEY_PATH"))
	if issuer == "" && keyID == "" && key == "" && path == "" {
		return nil, nil
	}
	if issuer == "" || keyID == "" || (key == "" && path == "") {
		return nil, errors.New("ASC_ISSUER_ID, ASC_KEY_ID and ASC_PRIVATE_KEY (or ASC_KEY_PATH) must all be set to use App Store Connect credentials from the environment")
	}
	if key == "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("ASC_KEY_PATH: %w", err)
		}
		key = string(data)
	}
	return &AppleCredentials{IssuerID: issuer, KeyID: keyID, PrivateKey: NormalizePEM(key)}, nil
}

// NormalizePEM accepts a key pasted with literal "\n" sequences (as CI secret
// stores often flatten it) and returns it with real newlines.
func NormalizePEM(key string) string {
	key = strings.ReplaceAll(key, `\n`, "\n")
	return strings.TrimSpace(key) + "\n"
}

// StoreAppleCredentials saves the API key as the Apple login.
func StoreAppleCredentials(c AppleCredentials) error {
	c.IssuerID, c.KeyID = strings.TrimSpace(c.IssuerID), strings.TrimSpace(c.KeyID)
	c.PrivateKey = NormalizePEM(c.PrivateKey)
	if c.IssuerID == "" || c.KeyID == "" || strings.TrimSpace(c.PrivateKey) == "" {
		return errors.New("issuer ID, key ID and private key are all required")
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return writeSecret(appleSecretName, string(data))
}
