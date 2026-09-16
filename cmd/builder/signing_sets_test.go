package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/MobAI-App/ios-builder/internal/asc"
	"github.com/MobAI-App/ios-builder/internal/config"
	"github.com/MobAI-App/ios-builder/internal/github"
	"github.com/MobAI-App/ios-builder/internal/signing"
	"github.com/MobAI-App/ios-builder/internal/signing/signingtest"
	"golang.org/x/crypto/nacl/box"
)

// fakeSecrets stands in for the GitHub secrets API: it hands out a real
// public key, decrypts what is stored so the test sees the values, and lists
// the names it holds.
type fakeSecrets struct {
	pub, priv *[32]byte
	stored    map[string]string
	names     []string
	listErr   error
	listed    int
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

func (f *fakeSecrets) ListSecretNames(context.Context, string, string) ([]string, error) {
	f.listed++
	if f.listErr != nil {
		return nil, f.listErr
	}
	names := make([]string, 0, len(f.stored))
	for name := range f.stored {
		names = append(names, name)
	}
	return names, nil
}

func TestUploadSigningSecretsWritesOneSet(t *testing.T) {
	store := newFakeSecrets(t)
	cfg := &config.Config{GitHub: config.GitHubConfig{Owner: "o", Repo: "r"}}
	var log strings.Builder
	if err := uploadSigningSecrets(context.Background(), store, cfg, &log, "STORE", []byte("p12"), "pw", []byte("profile")); err != nil {
		t.Fatal(err)
	}
	want := []string{"IOS_CERTIFICATE_STORE", "IOS_CERTIFICATE_PASSWORD_STORE", "IOS_PROVISIONING_PROFILE_STORE"}
	if !slices.Equal(store.names, want) {
		t.Fatalf("secrets written: %v, want %v", store.names, want)
	}
	if store.stored["IOS_CERTIFICATE_STORE"] != base64.StdEncoding.EncodeToString([]byte("p12")) || store.stored["IOS_CERTIFICATE_PASSWORD_STORE"] != "pw" || store.stored["IOS_PROVISIONING_PROFILE_STORE"] != base64.StdEncoding.EncodeToString([]byte("profile")) {
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
	if len(store.stored) != 6 || store.stored["IOS_CERTIFICATE_STORE"] == "" || store.stored["IOS_CERTIFICATE_DEVELOPMENT"] == "" {
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

func TestManualSigningTypeReadsTheProfile(t *testing.T) {
	devices := "<key>ProvisionedDevices</key><array><string>00008030-1</string></array>"
	dev := profileBytes(devices + "<key>Entitlements</key><dict><key>get-task-allow</key><true/></dict>")
	store := profileBytes("<key>Entitlements</key><dict><key>get-task-allow</key><false/></dict>")

	typ, err := manualSigningType(store, "")
	if err != nil || typ != signing.TypeStore {
		t.Fatalf("store profile: %q %v", typ, err)
	}
	if set, _ := config.SigningSet(string(typ)); set != "STORE" {
		t.Fatalf("set = %s", set)
	}
	if typ, err = manualSigningType(dev, ""); err != nil || typ != signing.TypeDevelopment {
		t.Fatalf("development profile: %q %v", typ, err)
	}
	// --distribution may confirm the type, aliases included, but not change it.
	if typ, err = manualSigningType(dev, "development"); err != nil || typ != signing.TypeDevelopment {
		t.Fatalf("--distribution development: %q %v", typ, err)
	}
	if _, err = manualSigningType(dev, "internal"); err == nil || !strings.Contains(err.Error(), "ad-hoc") {
		t.Fatalf("disagreeing --distribution accepted: %v", err)
	}
	if _, err = manualSigningType(dev, "app-store"); err == nil {
		t.Fatal("bad --distribution accepted")
	}
	// An unreadable profile is an error: there is no override.
	if _, err = manualSigningType([]byte("not a profile"), ""); err == nil {
		t.Fatal("unreadable profile accepted")
	}
}

func TestSetupDistribution(t *testing.T) {
	cfg := &config.Config{Profiles: map[string]config.Profile{"beta": {Distribution: "internal"}, "plain": {Scheme: "App"}}}
	for _, tc := range []struct {
		name, flag string
		want       signing.Type
	}{
		{"", "", signing.TypeDevelopment},
		{"beta", "", signing.TypeAdHoc},
		{"plain", "", signing.TypeDevelopment},
		{"beta", "store", signing.TypeStore},
		{"new", "internal", signing.TypeAdHoc},
	} {
		if got, err := setupDistribution(cfg, tc.name, tc.flag); err != nil || got != tc.want {
			t.Errorf("setupDistribution(%q, %q) = %q, %v; want %q", tc.name, tc.flag, got, err, tc.want)
		}
	}
	if _, err := setupDistribution(cfg, "", "app-store"); err == nil {
		t.Error("bad --distribution accepted")
	}
}

func TestSigningKeyPrefersTheTypeThenLegacy(t *testing.T) {
	dir := t.TempDir()

	// Nothing on disk: generate.
	if pem, path, err := signingKey("", dir, signing.TypeStore); err != nil || pem != nil || path != "" {
		t.Fatalf("empty dir: %q %q %v", pem, path, err)
	}
	// A key from before signing sets is reused by every type.
	legacy := filepath.Join(dir, signing.LegacyKeyFileName)
	if err := os.WriteFile(legacy, []byte("legacy"), 0600); err != nil {
		t.Fatal(err)
	}
	if pem, path, err := signingKey("", dir, signing.TypeStore); err != nil || string(pem) != "legacy" || path != legacy {
		t.Fatalf("legacy key: %q %q %v", pem, path, err)
	}
	// The type's own key wins over it.
	typed := filepath.Join(dir, signing.KeyFileName(signing.TypeStore))
	if err := os.WriteFile(typed, []byte("typed"), 0600); err != nil {
		t.Fatal(err)
	}
	if pem, path, err := signingKey("", dir, signing.TypeStore); err != nil || string(pem) != "typed" || path != typed {
		t.Fatalf("typed key: %q %q %v", pem, path, err)
	}
	if pem, path, err := signingKey("", dir, signing.TypeDevelopment); err != nil || string(pem) != "legacy" || path != legacy {
		t.Fatalf("other type falls back to legacy: %q %q %v", pem, path, err)
	}
	// --key beats both.
	explicit := filepath.Join(dir, "mine.key")
	if err := os.WriteFile(explicit, []byte("mine"), 0600); err != nil {
		t.Fatal(err)
	}
	if pem, path, err := signingKey(explicit, dir, signing.TypeStore); err != nil || string(pem) != "mine" || path != explicit {
		t.Fatalf("--key: %q %q %v", pem, path, err)
	}
}

// TestWriteSigningProfile checks what signing setup leaves in builder.json:
// a profile holding the distribution, other fields kept, ios.signing untouched.
func TestWriteSigningProfile(t *testing.T) {
	t.Chdir(t.TempDir())
	cfg := &config.Config{Project: "App", Platform: "ios", GitHub: config.GitHubConfig{Owner: "o", Repo: "r"},
		Profiles: map[string]config.Profile{"beta": {Scheme: "AppBeta", Env: map[string]string{"API_URL": "x"}}}}
	writeSigningProfile(cfg, "store", signing.TypeStore)
	writeSigningProfile(cfg, "beta", signing.TypeAdHoc)
	if err := config.NewManager().Save(cfg); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("builder.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		IOS      map[string]any            `json:"ios"`
		Profiles map[string]config.Profile `json:"profiles"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Profiles["store"].Distribution != "store" || doc.Profiles["beta"].Distribution != "ad-hoc" || doc.Profiles["beta"].Scheme != "AppBeta" || doc.Profiles["beta"].Env["API_URL"] != "x" {
		t.Fatalf("profiles written: %s", raw)
	}
	if _, ok := doc.IOS["signing"]; ok || strings.Contains(string(raw), `"signing"`) {
		t.Fatalf("ios.signing written for a profile setup: %s", raw)
	}
	// From nothing: the profiles map is created.
	empty := &config.Config{}
	writeSigningProfile(empty, "development", signing.TypeDevelopment)
	if empty.Profiles["development"].Distribution != "development" {
		t.Fatalf("profile not created: %+v", empty.Profiles)
	}
}

func signedConfig() *config.Config {
	return &config.Config{Project: "App", Platform: "ios", GitHub: config.GitHubConfig{Owner: "o", Repo: "r"},
		IOS: config.IOSConfig{BundleID: "com.example.app"},
		Profiles: map[string]config.Profile{
			"store":       {Distribution: "store"},
			"development": {Distribution: "development"},
			"unsigned":    {Configuration: "Release"},
			"inhouse":     {Distribution: "enterprise"},
		}}
}

func noASC() (*asc.Client, error) {
	return nil, errors.New("no App Store Connect API key configured. Run: builder auth apple")
}

func TestEnsureSigningSecretsChecksTheSet(t *testing.T) {
	ctx := context.Background()
	cfg := signedConfig()
	store := newFakeSecrets(t)

	// No distribution: nothing to check, the API is not even called.
	if err := ensureSigningSecrets(ctx, cfg, store, noASC, "unsigned", "", io.Discard); err != nil || store.listed != 0 {
		t.Fatalf("unsigned profile: %v, listed %d", err, store.listed)
	}
	if err := ensureSigningSecrets(ctx, cfg, store, noASC, "", "", io.Discard); err != nil || store.listed != 0 {
		t.Fatalf("no profile: %v, listed %d", err, store.listed)
	}

	// The set is complete: dispatch as today, no Apple credentials needed.
	for _, name := range config.SigningSecretNames("STORE").Names() {
		store.stored[name] = "x"
	}
	if err := ensureSigningSecrets(ctx, cfg, store, noASC, "store", "", io.Discard); err != nil {
		t.Fatalf("complete set: %v", err)
	}

	// A partial set without Apple credentials stops before the dispatch and
	// names both ways out.
	delete(store.stored, "IOS_PROVISIONING_PROFILE_STORE")
	var log strings.Builder
	err := ensureSigningSecrets(ctx, cfg, store, noASC, "store", "", &log)
	if err == nil {
		t.Fatal("missing profile secret accepted")
	}
	for _, want := range []string{"builder auth apple", "builder signing setup --certificate <p12> --profile <mobileprovision> --name store"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
	if !strings.Contains(log.String(), "missing IOS_PROVISIONING_PROFILE_STORE") {
		t.Errorf("missing secret not named:\n%s", log.String())
	}

	// Enterprise is never provisioned through the API.
	err = ensureSigningSecrets(ctx, cfg, store, noASC, "inhouse", "", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "--certificate") || strings.Contains(err.Error(), "auth apple") {
		t.Fatalf("enterprise: %v", err)
	}

	// A listing failure is reported, not treated as "missing".
	store.listErr = errors.New("403")
	if err := ensureSigningSecrets(ctx, cfg, store, noASC, "store", "", io.Discard); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("listing failure: %v", err)
	}

	// A profile that builds on Codemagic or Bitrise has no GitHub set to
	// check, whatever the top-level provider; --provider decides over it.
	store.listErr = nil
	store.listed = 0
	cfg.Profiles["cm"] = config.Profile{Distribution: "store", Provider: "codemagic"}
	if err := ensureSigningSecrets(ctx, cfg, store, noASC, "cm", "", io.Discard); err != nil || store.listed != 0 {
		t.Fatalf("codemagic profile: %v, listed %d", err, store.listed)
	}
	if err := ensureSigningSecrets(ctx, cfg, store, noASC, "store", "bitrise", io.Discard); err != nil || store.listed != 0 {
		t.Fatalf("--provider bitrise: %v, listed %d", err, store.listed)
	}
	if err := ensureSigningSecrets(ctx, cfg, store, noASC, "cm", "github", io.Discard); err == nil || store.listed != 1 {
		t.Fatalf("--provider github over a codemagic profile: %v, listed %d", err, store.listed)
	}
	if err := ensureSigningSecrets(ctx, cfg, store, noASC, "cm", "circle", io.Discard); err == nil || !strings.Contains(err.Error(), "unknown provider") {
		t.Fatalf("bad --provider: %v", err)
	}
}

func TestEnsureSigningSecretsProvisionsOnDemand(t *testing.T) {
	t.Chdir(t.TempDir())
	ctx := context.Background()
	cfg := signedConfig()
	store := newFakeSecrets(t)
	portal := signingtest.New(t)
	withPortal := func() (*asc.Client, error) { return portal.Client(t), nil }
	var log strings.Builder

	if err := ensureSigningSecrets(ctx, cfg, store, withPortal, "store", "", &log); err != nil {
		t.Fatalf("on-demand provisioning: %v\n%s", err, log.String())
	}
	// The whole store set is in the repository now, made from what the
	// portal issued, and the key, .p12 and profile are on disk.
	want := config.SigningSecretNames("STORE").Names()
	if !slices.Equal(store.names, want) {
		t.Fatalf("secrets written: %v, want %v", store.names, want)
	}
	if store.stored["IOS_PROVISIONING_PROFILE_STORE"] != base64.StdEncoding.EncodeToString([]byte("profile:prof-2")) {
		t.Fatalf("profile secret: %q", store.stored["IOS_PROVISIONING_PROFILE_STORE"])
	}
	for _, f := range []string{"ios-signing-store.key", "ios-signing-store.p12", "Builder-store-com.example.app.mobileprovision"} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("%s not written: %v", f, err)
		}
	}
	if !strings.Contains(log.String(), "Password:") || !strings.Contains(log.String(), store.stored["IOS_CERTIFICATE_PASSWORD_STORE"]) {
		t.Errorf("generated password not shown:\n%s", log.String())
	}
	if len(portal.Profiles) != 1 || portal.Profiles[0].Type != asc.ProfileTypeIOSAppStore || portal.Profiles[0].DeviceIDs != nil {
		t.Errorf("portal profiles: %+v", portal.Profiles)
	}

	// Second build: the set is there, nothing is provisioned again.
	portal.Reset()
	if err := ensureSigningSecrets(ctx, cfg, store, withPortal, "store", "", io.Discard); err != nil || len(portal.Calls()) != 0 || len(store.names) != 3 {
		t.Fatalf("second build: %v, calls %v, uploads %v", err, portal.Calls(), store.names)
	}

	// Development with no device anywhere cannot be provisioned without
	// prompting: the error sends the user to signing setup.
	err := ensureSigningSecrets(ctx, cfg, store, withPortal, "development", "", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "builder signing setup --distribution development --devices-from-mobai") {
		t.Fatalf("development without devices: %v", err)
	}
	if portal.Count("POST /v1/certificates") != 0 || len(store.names) != 3 {
		t.Fatalf("devices are checked before anything is issued: %v", portal.Calls())
	}

	// With a device on the account the development set follows.
	portal.Devices = []signingtest.Device{{ID: "dev-1", Name: "Jane's iPhone", UDID: "00008030-000000000000001E", Status: "ENABLED"}}
	if err := ensureSigningSecrets(ctx, cfg, store, withPortal, "development", "", io.Discard); err != nil {
		t.Fatalf("development with a registered device: %v", err)
	}
	if len(store.stored) != 6 || store.stored["IOS_CERTIFICATE_DEVELOPMENT"] == "" {
		t.Fatalf("development set not uploaded: %v", store.names)
	}

	// Without a bundle ID anywhere the build stops before touching Apple.
	cfg.IOS.BundleID = ""
	delete(store.stored, "IOS_CERTIFICATE_DEVELOPMENT")
	portal.Reset()
	err = ensureSigningSecrets(ctx, cfg, store, withPortal, "development", "", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "ios.bundleId") || len(portal.Calls()) != 0 {
		t.Fatalf("no bundle ID: %v, calls %v", err, portal.Calls())
	}
}
