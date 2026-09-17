package auth

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/zalando/go-keyring"
)

func clearAppleEnv(t *testing.T) {
	for _, name := range []string{"ASC_ISSUER_ID", "ASC_KEY_ID", "ASC_PRIVATE_KEY", "ASC_KEY_PATH"} {
		t.Setenv(name, "")
	}
}

func TestAppleCredentialsStoreGetLogout(t *testing.T) {
	keyring.MockInit()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("APPDATA", dir)
	clearAppleEnv(t)

	if _, _, err := GetAppleCredentials(); !errors.Is(err, ErrNotAuthenticated) {
		t.Fatalf("before login: %v", err)
	}
	want := AppleCredentials{IssuerID: " issuer ", KeyID: "KEY1", PrivateKey: "-----BEGIN PRIVATE KEY-----\\nabc\\n-----END PRIVATE KEY-----"}
	if err := StoreAppleCredentials(want); err != nil {
		t.Fatal(err)
	}
	// Other logins are untouched by the Apple one.
	if err := StoreProviderToken("codemagic", "cm-secret"); err != nil {
		t.Fatal(err)
	}
	got, source, err := GetAppleCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if source != AppleSourceStored || got.IssuerID != "issuer" || got.KeyID != "KEY1" || got.PrivateKey != "-----BEGIN PRIVATE KEY-----\nabc\n-----END PRIVATE KEY-----\n" {
		t.Errorf("got %+v from %s", got, source)
	}
	if err := LogoutProvider("apple"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := GetAppleCredentials(); !errors.Is(err, ErrNotAuthenticated) {
		t.Errorf("after logout: %v", err)
	}
	if token, err := GetProviderToken("codemagic"); err != nil || token != "cm-secret" {
		t.Errorf("codemagic login lost: %q %v", token, err)
	}
	if err := StoreAppleCredentials(AppleCredentials{IssuerID: "i"}); err == nil {
		t.Error("incomplete credentials accepted")
	}
}

func TestAppleCredentialsFromEnvironment(t *testing.T) {
	keyring.MockInit()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("APPDATA", dir)
	clearAppleEnv(t)
	if err := StoreAppleCredentials(AppleCredentials{IssuerID: "stored", KeyID: "S", PrivateKey: "pem"}); err != nil {
		t.Fatal(err)
	}

	t.Setenv("ASC_ISSUER_ID", "env-issuer")
	if _, _, err := GetAppleCredentials(); err == nil {
		t.Error("partial environment must be an error, not a silent fallback")
	}
	t.Setenv("ASC_KEY_ID", "ENVKEY")
	t.Setenv("ASC_PRIVATE_KEY", "line1\\nline2")
	got, source, err := GetAppleCredentials()
	if err != nil || source != AppleSourceEnv || got.IssuerID != "env-issuer" || got.KeyID != "ENVKEY" || got.PrivateKey != "line1\nline2\n" {
		t.Errorf("env credentials: %+v %s %v", got, source, err)
	}

	t.Setenv("ASC_PRIVATE_KEY", "")
	keyPath := filepath.Join(dir, "AuthKey.p8")
	if err := os.WriteFile(keyPath, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ASC_KEY_PATH", keyPath)
	got, _, err = GetAppleCredentials()
	if err != nil || got.PrivateKey != "from-file\n" {
		t.Errorf("key path: %+v %v", got, err)
	}
	t.Setenv("ASC_KEY_PATH", filepath.Join(dir, "missing.p8"))
	if _, _, err := GetAppleCredentials(); err == nil {
		t.Error("missing key file must be an error")
	}
}
