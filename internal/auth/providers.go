package auth

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/zalando/go-keyring"
)

func validateProvider(provider string) error {
	switch provider {
	case "github", "codemagic", "bitrise":
		return nil
	}
	return fmt.Errorf("unknown authentication provider %q", provider)
}

// GetProviderToken reads a provider's independent login. Environment overrides
// for the new providers are useful on headless CI; GitHub retains its old flow.
func GetProviderToken(provider string) (string, error) {
	if err := validateProvider(provider); err != nil {
		return "", err
	}
	if provider == "github" {
		return GetToken()
	}
	if token := strings.TrimSpace(os.Getenv(strings.ToUpper(provider) + "_API_TOKEN")); token != "" {
		return token, nil
	}
	return readSecret(provider + "-token")
}

// readSecret returns a saved secret by name, checking the fallback file before
// the keyring: the file may contain a newer login than an inaccessible old
// keyring entry, and a successful keyring save removes the file.
func readSecret(name string) (string, error) {
	dir, err := getConfigDir()
	if err != nil {
		return "", err
	}
	if value, err := readProviderFile(filepath.Join(dir, name)); err == nil {
		return value, nil
	} else if !errors.Is(err, ErrNotAuthenticated) {
		return "", err
	}
	if runtime.GOOS != "linux" {
		if value, err := keyring.Get(keyringService, name); err == nil && value != "" {
			return value, nil
		}
	}
	return "", ErrNotAuthenticated
}

func readProviderFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", ErrNotAuthenticated
	}
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", ErrNotAuthenticated
	}
	return token, nil
}

// StoreProviderToken keeps credentials in separate keyring entries/files. The
// Linux/file fallback matches the existing GitHub login storage behavior.
func StoreProviderToken(provider, token string) error {
	if err := validateProvider(provider); err != nil {
		return err
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return fmt.Errorf("API token is empty")
	}
	if provider == "github" {
		return storeToken(token)
	}
	return writeSecret(provider+"-token", token)
}

// writeSecret saves a secret in the keyring, falling back to a 0600 file in
// the config directory on Linux/WSL or when the keyring is unavailable.
func writeSecret(name, value string) error {
	dir, err := getConfigDir()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, name)
	if runtime.GOOS != "linux" {
		if err := keyring.Set(keyringService, name, value); err == nil {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
			return nil
		}
	}
	return writeProviderFile(path, value)
}

func writeProviderFile(path, token string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Chmod(0600); err != nil {
		return err
	}
	_, err = f.WriteString(token)
	return err
}

// LogoutProvider removes only this provider's saved login, leaving others intact.
// It does not unset environment variables in the parent shell.
func LogoutProvider(provider string) error {
	switch provider {
	case "github":
		return Logout()
	case "apple":
		return deleteSecret(appleSecretName)
	}
	if err := validateProvider(provider); err != nil {
		return err
	}
	return deleteSecret(provider + "-token")
}

// deleteSecret removes a secret from both the keyring and the fallback file.
func deleteSecret(name string) error {
	var keyringErr error
	if runtime.GOOS != "linux" {
		if err := keyring.Delete(keyringService, name); err != nil && err != keyring.ErrNotFound {
			keyringErr = err
		}
	}
	dir, err := getConfigDir()
	if err != nil {
		return err
	}
	err = os.Remove(filepath.Join(dir, name))
	if os.IsNotExist(err) {
		err = nil
	}
	return errors.Join(keyringErr, err)
}
