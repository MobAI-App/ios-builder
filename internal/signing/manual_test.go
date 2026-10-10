package signing

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"golang.org/x/crypto/nacl/box"

	"github.com/MobAI-App/ios-builder/internal/config"
	"github.com/MobAI-App/ios-builder/internal/github"
)

// fakeSecrets is a SecretStore that opens what it is given, so a test can
// read the values back.
type fakeSecrets struct {
	pub, priv *[32]byte
	stored    map[string]string
	names     []string
	writeErr  error
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

func (f *fakeSecrets) CreateOrUpdateSecret(_ context.Context, _, _, name, encryptedValue, _ string) error {
	if f.writeErr != nil {
		return f.writeErr
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

func (f *fakeSecrets) ListSecretNames(context.Context, string, string) ([]string, error) {
	return f.names, nil
}

func storeProfile(appID string) []byte {
	return mobileprovision("<key>TeamIdentifier</key><array><string>ABCDE12345</string></array><key>Entitlements</key><dict><key>get-task-allow</key><false/><key>application-identifier</key><string>ABCDE12345." + appID + "</string></dict>")
}

// SetupManual uploads the four secrets of the profile's set, writes the
// build profile and hands builder.json to SaveConfig instead of the disk.
func TestSetupManualUploadsAndSavesThroughCallback(t *testing.T) {
	store := newFakeSecrets(t)
	cfg := &config.Config{Project: "App", GitHub: config.GitHubConfig{Owner: "o", Repo: "r"}, IOS: config.IOSConfig{Extensions: []string{"com.example.app.widget"}}}
	var saved *config.Config
	widget := storeProfile("com.example.app.widget")
	res, err := SetupManual(context.Background(), store, nil, cfg, &ManualOptions{
		Type: TypeStore, P12: []byte("p12 bytes"), Password: "pw", Profile: storeProfile("com.example.app"),
		ExtensionProfiles: map[string][]byte{"com.example.app.widget": widget},
		SaveConfig:        func(c *config.Config) error { saved = c; return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.SecretsUploaded || res.GitHubUpload != "ok" || res.SigningSet != "STORE" || res.BuildProfile != "store" || !res.ProfileWritten || res.ReplacedDistribution != "" {
		t.Errorf("result = %+v", res)
	}
	want := []string{"IOS_CERTIFICATE_STORE", "IOS_CERTIFICATE_PASSWORD_STORE", "IOS_PROVISIONING_PROFILE_STORE", "IOS_EXTENSION_PROFILES_STORE"}
	if !slices.Equal(store.names, want) {
		t.Errorf("uploaded %v, want %v", store.names, want)
	}
	if store.stored["IOS_CERTIFICATE_STORE"] != base64.StdEncoding.EncodeToString([]byte("p12 bytes")) || store.stored["IOS_CERTIFICATE_PASSWORD_STORE"] != "pw" {
		t.Errorf("certificate secrets: %q", store.stored)
	}
	if got, err := DecodeExtensionProfiles(store.stored["IOS_EXTENSION_PROFILES_STORE"]); err != nil || !bytes.Equal(got["com.example.app.widget"], widget) {
		t.Errorf("extension profiles: %q, %v", store.stored["IOS_EXTENSION_PROFILES_STORE"], err)
	}
	if saved != cfg || cfg.Profiles["store"].Distribution != "store" {
		t.Errorf("saved %v, profiles %v", saved, cfg.Profiles)
	}
	if _, err := os.Stat(config.ConfigFileName); err == nil {
		t.Error("builder.json written to the working directory despite SaveConfig")
	}
}

// A failed upload is reported in the result, not as an error, and the profile
// stays out of builder.json: it must not claim a set the repository lacks.
// The config is still saved, as the CLI does.
func TestSetupManualReportsUploadFailure(t *testing.T) {
	store := newFakeSecrets(t)
	store.writeErr = errors.New("no admin access")
	cfg := &config.Config{GitHub: config.GitHubConfig{Owner: "o", Repo: "r"}, Profiles: map[string]config.Profile{"beta": {Distribution: "development", Scheme: "Beta"}}}
	saved := false
	res, err := SetupManual(context.Background(), store, nil, cfg, &ManualOptions{
		Type: TypeAdHoc, ProfileName: "beta", P12: []byte("p12"), Password: "pw", Profile: storeProfile("com.example.app"),
		SaveConfig: func(*config.Config) error { saved = true; return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.SecretsUploaded || res.ProfileWritten || res.UploadError == nil || !strings.Contains(res.GitHubUpload, "no admin access") {
		t.Errorf("result = %+v", res)
	}
	if !saved || res.ReplacedDistribution != "" || cfg.Profiles["beta"].Distribution != "development" {
		t.Errorf("profile: saved %v, replaced %q, %+v", saved, res.ReplacedDistribution, cfg.Profiles["beta"])
	}
	// With the upload working, the same call replaces the profile's distribution.
	store.writeErr = nil
	res, err = SetupManual(context.Background(), store, nil, cfg, &ManualOptions{
		Type: TypeAdHoc, ProfileName: "beta", P12: []byte("p12"), Password: "pw", Profile: storeProfile("com.example.app"),
		SaveConfig: func(*config.Config) error { return nil },
	})
	if err != nil || !res.ProfileWritten || res.ReplacedDistribution != "development" || cfg.Profiles["beta"].Distribution != "ad-hoc" || cfg.Profiles["beta"].Scheme != "Beta" {
		t.Errorf("replaced profile: %+v, %v, %+v", res, err, cfg.Profiles["beta"])
	}
	// A store that could not be built counts as a failed upload too.
	res, err = SetupManual(context.Background(), nil, errors.New("not logged in"), cfg, &ManualOptions{
		Type: TypeAdHoc, ProfileName: "beta", P12: []byte("p12"), Password: "pw", Profile: storeProfile("com.example.app"),
		SaveConfig: func(*config.Config) error { return nil },
	})
	if err != nil || res.SecretsUploaded || res.GitHubUpload != "not logged in" {
		t.Errorf("no store: %+v, %v", res, err)
	}
}

// A SaveConfig that fails is the error, with the upload result kept.
func TestSetupManualSaveFailure(t *testing.T) {
	cfg := &config.Config{GitHub: config.GitHubConfig{Owner: "o", Repo: "r"}}
	res, err := SetupManual(context.Background(), newFakeSecrets(t), nil, cfg, &ManualOptions{
		Type: TypeDevelopment, P12: []byte("p12"), Password: "pw", Profile: storeProfile("com.example.app"),
		SaveConfig: func(*config.Config) error { return errors.New("disk full") },
	})
	if err == nil || !strings.Contains(err.Error(), "failed to update config: disk full") || res == nil || !res.SecretsUploaded {
		t.Fatalf("save failure: %+v, %v", res, err)
	}
}

// Without SaveConfig, builder.json lands in the working directory: the CLI's
// behavior, unchanged.
func TestSaveConfigDefaultsToWorkingDirectory(t *testing.T) {
	t.Chdir(t.TempDir())
	cfg := &config.Config{Project: "App"}
	if err := saveConfig(nil, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.NewManager().Load()
	if err != nil || loaded.Project != "App" {
		t.Fatalf("builder.json: %+v, %v", loaded, err)
	}
}

func TestManualType(t *testing.T) {
	dev := mobileprovision("<key>ProvisionedDevices</key><array><string>00008030-1</string></array><key>Entitlements</key><dict><key>get-task-allow</key><true/></dict>")
	if typ, err := ManualType(storeProfile("com.example.app"), ""); err != nil || typ != TypeStore {
		t.Fatalf("store profile: %q %v", typ, err)
	}
	// The distribution may confirm the type, aliases included, but not change it.
	if typ, err := ManualType(dev, "development"); err != nil || typ != TypeDevelopment {
		t.Fatalf("development: %q %v", typ, err)
	}
	if _, err := ManualType(dev, "internal"); err == nil || !strings.Contains(err.Error(), "ad-hoc") {
		t.Fatalf("disagreeing distribution accepted: %v", err)
	}
	if _, err := ManualType([]byte("not a profile"), ""); err == nil {
		t.Fatal("unreadable profile accepted")
	}
}

func TestMatchExtensionProfiles(t *testing.T) {
	widget, share, wildcard := storeProfile("com.example.app.widget"), storeProfile("com.example.app.share"), storeProfile("com.example.*")
	extensions := []string{"com.example.app.share", "com.example.app.widget"}
	got, err := MatchExtensionProfiles(extensions, map[string][]byte{"any": wildcard, "w": widget}, TypeStore)
	if err != nil || got["com.example.app.widget"] != "w" || got["com.example.app.share"] != "any" {
		t.Fatalf("exact over wildcard: %v, %v", got, err)
	}
	if _, err = MatchExtensionProfiles(extensions, map[string][]byte{"s": share}, TypeStore); err == nil || !strings.Contains(err.Error(), "extension com.example.app.widget has no profile") {
		t.Fatalf("missing profile: %v", err)
	}
	if _, err = MatchExtensionProfiles(nil, map[string][]byte{"w": widget}, TypeDevelopment); err == nil || !strings.Contains(err.Error(), "w is a store profile, but the app profile is development") {
		t.Fatalf("type mismatch: %v", err)
	}
}
