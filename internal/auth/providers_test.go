package auth

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestIndependentProviderLogins(t *testing.T) {
	keyring.MockInit()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("APPDATA", dir)
	t.Setenv("CODEMAGIC_API_TOKEN", "")
	t.Setenv("BITRISE_API_TOKEN", "")
	for _, name := range []string{"github", "codemagic", "bitrise"} {
		if err := StoreProviderToken(name, name+"-secret"); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"github", "codemagic", "bitrise"} {
		got, err := GetProviderToken(name)
		if err != nil || got != name+"-secret" {
			t.Fatalf("%s: %q %v", name, got, err)
		}
	}
	if err := LogoutProvider("codemagic"); err != nil {
		t.Fatal(err)
	}
	if _, err := GetProviderToken("codemagic"); !errors.Is(err, ErrNotAuthenticated) {
		t.Fatalf("logout: %v", err)
	}
	for _, name := range []string{"github", "bitrise"} {
		got, err := GetProviderToken(name)
		if err != nil || got != name+"-secret" {
			t.Fatalf("other login removed: %s", name)
		}
	}
	t.Setenv("BITRISE_API_TOKEN", "environment")
	got, err := GetProviderToken("bitrise")
	if err != nil || got != "environment" {
		t.Fatal("environment override lost")
	}
	if err := LogoutProvider("../../github"); err == nil {
		t.Fatal("accepted unknown provider")
	}
}

func TestCredentialFilePermissionsAndReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials", "codemagic-token")
	if err := writeProviderFile(path, "old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if err := writeProviderFile(path, "new"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("permissions: %v", info.Mode())
	}
	got, err := readProviderFile(path)
	if err != nil || got != "new" {
		t.Fatalf("%q %v", got, err)
	}
}
