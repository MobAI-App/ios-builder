package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/MobAI-App/ios-builder/internal/config"
	"github.com/MobAI-App/ios-builder/internal/github"
	"github.com/MobAI-App/ios-builder/internal/signing"
	"github.com/spf13/cobra"
	"golang.org/x/crypto/nacl/box"
)

// fakeSecrets stands in for the GitHub secrets API: it hands out a real
// public key and decrypts what is stored, so the test sees the values.
type fakeSecrets struct {
	pub, priv *[32]byte
	stored    map[string]string
	names     []string
}

func newFakeSecrets(t *testing.T) *fakeSecrets {
	t.Helper()
	pub, priv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &fakeSecrets{pub: pub, priv: priv, stored: map[string]string{}}
}

func (f *fakeSecrets) GetPublicKey(context.Context, string, string) (*github.PublicKey, error) {
	return &github.PublicKey{KeyID: "key-1", Key: base64.StdEncoding.EncodeToString(f.pub[:])}, nil
}

func (f *fakeSecrets) CreateOrUpdateSecret(_ context.Context, _, _, name, encryptedValue, keyID string) error {
	if keyID != "key-1" {
		return os.ErrInvalid
	}
	sealed, err := base64.StdEncoding.DecodeString(encryptedValue)
	if err != nil {
		return err
	}
	value, ok := box.OpenAnonymous(nil, sealed, f.pub, f.priv)
	if !ok {
		return os.ErrInvalid
	}
	f.stored[name] = string(value)
	f.names = append(f.names, name)
	return nil
}

func TestUploadSigningSecretsWritesOneSet(t *testing.T) {
	store := newFakeSecrets(t)
	cfg := &config.Config{GitHub: config.GitHubConfig{Owner: "o", Repo: "r"}}
	var log strings.Builder
	if err := uploadSigningSecrets(context.Background(), store, cfg, &log, "APP_STORE", []byte("p12"), "pw", []byte("profile")); err != nil {
		t.Fatal(err)
	}
	want := []string{"IOS_CERTIFICATE_APP_STORE", "IOS_CERTIFICATE_PASSWORD_APP_STORE", "IOS_PROVISIONING_PROFILE_APP_STORE"}
	if !slices.Equal(store.names, want) {
		t.Fatalf("secrets written: %v, want %v", store.names, want)
	}
	if store.stored["IOS_CERTIFICATE_APP_STORE"] != base64.StdEncoding.EncodeToString([]byte("p12")) || store.stored["IOS_CERTIFICATE_PASSWORD_APP_STORE"] != "pw" || store.stored["IOS_PROVISIONING_PROFILE_APP_STORE"] != base64.StdEncoding.EncodeToString([]byte("profile")) {
		t.Fatalf("values: %v", store.stored)
	}
	for _, name := range want {
		if !strings.Contains(log.String(), "Uploaded: "+name) {
			t.Errorf("%s not reported:\n%s", name, log.String())
		}
	}

	// A second set adds to the first; the legacy names are never touched.
	if err := uploadSigningSecrets(context.Background(), store, cfg, io.Discard, "DEVELOPMENT", []byte("dev"), "pw2", []byte("dev-profile")); err != nil {
		t.Fatal(err)
	}
	if len(store.stored) != 6 || store.stored["IOS_CERTIFICATE_APP_STORE"] == "" || store.stored["IOS_CERTIFICATE_DEVELOPMENT"] == "" {
		t.Fatalf("second set replaced the first: %v", store.names)
	}
	for name := range store.stored {
		if name == "IOS_CERTIFICATE" || name == "IOS_CERTIFICATE_PASSWORD" || name == "IOS_PROVISIONING_PROFILE" {
			t.Errorf("legacy secret %s written", name)
		}
	}
}

// profileBytes is a .mobileprovision stand-in: a plist between arbitrary
// bytes, as the CMS wrapper leaves it.
func profileBytes(body string) []byte {
	return []byte("\x30\x82\x1a\x00 cms " + `<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict>` + body + `</dict></plist>` + "\x00\xff trailer")
}

func TestManualSigningTypeChoosesTheSet(t *testing.T) {
	devices := "<key>ProvisionedDevices</key><array><string>00008030-1</string></array>"
	dev := profileBytes(devices + "<key>Entitlements</key><dict><key>get-task-allow</key><true/></dict>")
	store := profileBytes("<key>Entitlements</key><dict><key>get-task-allow</key><false/></dict>")

	// Without --type the profile decides.
	typ, source, err := manualSigningType(store, "development", false)
	if err != nil || typ != signing.TypeAppStore || source != "read from the profile" {
		t.Fatalf("app-store profile: %q %q %v", typ, source, err)
	}
	if set, _ := config.SigningSet(string(typ)); set != "APP_STORE" {
		t.Fatalf("set = %s", set)
	}
	if typ, _, err = manualSigningType(dev, "development", false); err != nil || typ != signing.TypeDevelopment {
		t.Fatalf("development profile: %q %v", typ, err)
	}
	// --type overrides, even when it disagrees.
	if typ, source, err = manualSigningType(dev, "ad-hoc", true); err != nil || typ != signing.TypeAdHoc || source != "--type" {
		t.Fatalf("--type ad-hoc: %q %q %v", typ, source, err)
	}
	if _, _, err = manualSigningType(dev, "distribution", true); err == nil {
		t.Fatal("bad --type accepted")
	}
	// An unreadable profile needs --type.
	if _, _, err = manualSigningType([]byte("not a profile"), "development", false); err == nil || !strings.Contains(err.Error(), "--type") {
		t.Fatalf("unreadable profile: %v", err)
	}
	if typ, _, err = manualSigningType([]byte("not a profile"), "enterprise", true); err != nil || typ != signing.TypeEnterprise {
		t.Fatalf("unreadable profile with --type: %q %v", typ, err)
	}
}

func TestSigningKeyPrefersTheTypeThenLegacy(t *testing.T) {
	dir := t.TempDir()
	cmd := &cobra.Command{}
	cmd.Flags().String("key", "", "")

	// Nothing on disk: generate.
	if pem, path, err := signingKey(cmd, dir, signing.TypeAppStore); err != nil || pem != nil || path != "" {
		t.Fatalf("empty dir: %q %q %v", pem, path, err)
	}
	// A key from before signing sets is reused by every type.
	legacy := filepath.Join(dir, signing.LegacyKeyFileName)
	if err := os.WriteFile(legacy, []byte("legacy"), 0600); err != nil {
		t.Fatal(err)
	}
	if pem, path, err := signingKey(cmd, dir, signing.TypeAppStore); err != nil || string(pem) != "legacy" || path != legacy {
		t.Fatalf("legacy key: %q %q %v", pem, path, err)
	}
	// The type's own key wins over it.
	typed := filepath.Join(dir, signing.KeyFileName(signing.TypeAppStore))
	if err := os.WriteFile(typed, []byte("typed"), 0600); err != nil {
		t.Fatal(err)
	}
	if pem, path, err := signingKey(cmd, dir, signing.TypeAppStore); err != nil || string(pem) != "typed" || path != typed {
		t.Fatalf("typed key: %q %q %v", pem, path, err)
	}
	if pem, path, err := signingKey(cmd, dir, signing.TypeDevelopment); err != nil || string(pem) != "legacy" || path != legacy {
		t.Fatalf("other type falls back to legacy: %q %q %v", pem, path, err)
	}
	// --key beats both.
	explicit := filepath.Join(dir, "mine.key")
	if err := os.WriteFile(explicit, []byte("mine"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("key", explicit); err != nil {
		t.Fatal(err)
	}
	if pem, path, err := signingKey(cmd, dir, signing.TypeAppStore); err != nil || string(pem) != "mine" || path != explicit {
		t.Fatalf("--key: %q %q %v", pem, path, err)
	}
}
