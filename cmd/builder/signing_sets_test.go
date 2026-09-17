package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
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
	"github.com/spf13/cobra"
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
	// writeErr is a repository the login cannot write to.
	writeErr error
	listed   int
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
	if f.writeErr != nil {
		return f.writeErr
	}
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
	if err := uploadSigningSecrets(context.Background(), store, cfg, &log, "STORE", []byte("p12"), "pw", []byte("profile"), map[string][]byte{"com.example.app.widget": []byte("widget")}); err != nil {
		t.Fatal(err)
	}
	want := []string{"IOS_CERTIFICATE_STORE", "IOS_CERTIFICATE_PASSWORD_STORE", "IOS_PROVISIONING_PROFILE_STORE", "IOS_EXTENSION_PROFILES_STORE"}
	if !slices.Equal(store.names, want) {
		t.Fatalf("secrets written: %v, want %v", store.names, want)
	}
	if store.stored["IOS_CERTIFICATE_STORE"] != base64.StdEncoding.EncodeToString([]byte("p12")) || store.stored["IOS_CERTIFICATE_PASSWORD_STORE"] != "pw" || store.stored["IOS_PROVISIONING_PROFILE_STORE"] != base64.StdEncoding.EncodeToString([]byte("profile")) {
		t.Fatalf("values: %v", store.stored)
	}
	if got, err := signing.DecodeExtensionProfiles(store.stored["IOS_EXTENSION_PROFILES_STORE"]); err != nil || string(got["com.example.app.widget"]) != "widget" {
		t.Fatalf("extension profiles: %q, %v", store.stored["IOS_EXTENSION_PROFILES_STORE"], err)
	}
	for _, name := range want {
		if !strings.Contains(log.String(), "Uploaded: "+name) {
			t.Errorf("%s not reported:\n%s", name, log.String())
		}
	}

	// A second set adds to the first; the legacy names are never touched, and
	// an app without extensions writes {} so nothing stale is left behind.
	if err := uploadSigningSecrets(context.Background(), store, cfg, io.Discard, "DEVELOPMENT", []byte("dev"), "pw2", []byte("dev-profile"), nil); err != nil {
		t.Fatal(err)
	}
	if len(store.stored) != 8 || store.stored["IOS_CERTIFICATE_STORE"] == "" || store.stored["IOS_CERTIFICATE_DEVELOPMENT"] == "" || store.stored["IOS_EXTENSION_PROFILES_DEVELOPMENT"] != "{}" {
		t.Fatalf("second set replaced the first: %v", store.stored)
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

// storeProfile is an App Store profile for the app id, team ABCDE12345.
func storeProfile(appID string) []byte {
	return profileBytes("<key>TeamIdentifier</key><array><string>ABCDE12345</string></array><key>Entitlements</key><dict><key>get-task-allow</key><false/><key>application-identifier</key><string>ABCDE12345." + appID + "</string></dict>")
}

// appWithWidgetPbxproj is an app target and a widget extension target, in
// the OpenStep form Xcode writes.
const appWithWidgetPbxproj = `// !$*UTF8*$!
{
	objects = {
		A1 = { isa = PBXNativeTarget; buildConfigurationList = LA; name = App; productType = "com.apple.product-type.application"; };
		W1 = { isa = PBXNativeTarget; buildConfigurationList = LW; name = Widget; productType = "com.apple.product-type.app-extension"; };
		AR = { isa = XCBuildConfiguration; buildSettings = { PRODUCT_BUNDLE_IDENTIFIER = com.example.app; }; name = Release; };
		WR = { isa = XCBuildConfiguration; buildSettings = { PRODUCT_BUNDLE_IDENTIFIER = com.example.app.widget; }; name = Release; };
		LA = { isa = XCConfigurationList; buildConfigurations = ( AR, ); };
		LW = { isa = XCConfigurationList; buildConfigurations = ( WR, ); };
	};
	rootObject = P0;
}
`

// writeProject writes a project.pbxproj under ./<name> in the working directory.
func writeProject(t *testing.T, name, pbxproj string) {
	t.Helper()
	if err := os.MkdirAll(name, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(name, "project.pbxproj"), []byte(pbxproj), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestMatchExtensionProfiles(t *testing.T) {
	widget, share, wildcard := storeProfile("com.example.app.widget"), storeProfile("com.example.app.share"), storeProfile("com.example.*")
	extensions := []string{"com.example.app.share", "com.example.app.widget"}

	got, err := matchExtensionProfiles(extensions, map[string][]byte{"w.mobileprovision": widget, "s.mobileprovision": share}, signing.TypeStore)
	if err != nil || got["com.example.app.widget"] != "w.mobileprovision" || got["com.example.app.share"] != "s.mobileprovision" {
		t.Fatalf("exact profiles: %v, %v", got, err)
	}
	// One wildcard profile covers every extension under it; an exact one
	// wins over it for its own extension.
	if got, err = matchExtensionProfiles(extensions, map[string][]byte{"any.mobileprovision": wildcard}, signing.TypeStore); err != nil || got["com.example.app.widget"] != "any.mobileprovision" || got["com.example.app.share"] != "any.mobileprovision" {
		t.Fatalf("wildcard profile: %v, %v", got, err)
	}
	if got, err = matchExtensionProfiles(extensions, map[string][]byte{"any.mobileprovision": wildcard, "w.mobileprovision": widget}, signing.TypeStore); err != nil || got["com.example.app.widget"] != "w.mobileprovision" || got["com.example.app.share"] != "any.mobileprovision" {
		t.Fatalf("exact over wildcard: %v, %v", got, err)
	}
	// Nothing to match is fine both ways round only when both are empty.
	if got, err = matchExtensionProfiles(nil, nil, signing.TypeStore); err != nil || len(got) != 0 {
		t.Fatalf("no extensions: %v, %v", got, err)
	}
	// A missing profile names the extension and the flag; a profile for an
	// unlisted extension names the file and the config field.
	_, err = matchExtensionProfiles(extensions, map[string][]byte{"w.mobileprovision": widget}, signing.TypeStore)
	if err == nil || !strings.Contains(err.Error(), "extension com.example.app.share has no profile") || !strings.Contains(err.Error(), "--extension-profile") {
		t.Fatalf("missing profile: %v", err)
	}
	_, err = matchExtensionProfiles(nil, map[string][]byte{"w.mobileprovision": widget}, signing.TypeStore)
	if err == nil || !strings.Contains(err.Error(), "w.mobileprovision covers com.example.app.widget, which is not in ios.extensions") {
		t.Fatalf("unlisted extension: %v", err)
	}
	// Every extension profile is of the app profile's type.
	_, err = matchExtensionProfiles(extensions[1:], map[string][]byte{"w.mobileprovision": widget}, signing.TypeDevelopment)
	if err == nil || !strings.Contains(err.Error(), "w.mobileprovision is a store profile, but the app profile is development") {
		t.Fatalf("type mismatch: %v", err)
	}
	if _, err = matchExtensionProfiles(extensions, map[string][]byte{"bad.mobileprovision": []byte("nope")}, signing.TypeStore); err == nil {
		t.Fatal("unreadable profile accepted")
	}
}

// TestSigningSetupManualUploadsExtensionProfiles: the widget found in the
// project goes into ios.extensions, its --extension-profile into the fourth
// secret, and a run without that flag says which extension lacks a profile.
func TestSigningSetupManualUploadsExtensionProfiles(t *testing.T) {
	t.Chdir(t.TempDir())
	cfg := &config.Config{Project: "App", Platform: "ios", GitHub: config.GitHubConfig{Owner: "o", Repo: "r"}}
	if err := config.NewManager().Save(cfg); err != nil {
		t.Fatal(err)
	}
	writeProject(t, "App.xcodeproj", appWithWidgetPbxproj)
	for name, data := range map[string][]byte{"ios-signing.p12": []byte("p12 bytes"), "App.mobileprovision": storeProfile("com.example.app"), "Widget.mobileprovision": storeProfile("com.example.app.widget")} {
		if err := os.WriteFile(name, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	store := newFakeSecrets(t)

	cmd, _, _ := signingSetupCommand(t, store, "--certificate", "ios-signing.p12", "--profile", "App.mobileprovision", "--password", "pw")
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "extension com.example.app.widget has no profile") {
		t.Fatalf("widget without a profile: %v", err)
	}
	if len(store.names) != 0 {
		t.Fatalf("uploaded despite the missing profile: %v", store.names)
	}

	cmd, stdout, stderr := signingSetupCommand(t, store, "--certificate", "ios-signing.p12", "--profile", "App.mobileprovision", "--password", "pw", "--extension-profile", "Widget.mobileprovision")
	if err := cmd.Execute(); err != nil {
		t.Fatalf("%v\n%s", err, stderr.String())
	}
	if got, err := signing.DecodeExtensionProfiles(store.stored["IOS_EXTENSION_PROFILES_STORE"]); err != nil || !bytes.Equal(got["com.example.app.widget"], storeProfile("com.example.app.widget")) || len(got) != 1 {
		t.Errorf("extension profiles secret: %q, %v", store.stored["IOS_EXTENSION_PROFILES_STORE"], err)
	}
	if !strings.Contains(stdout.String(), `IOS_EXTENSION_PROFILES_STORE  JSON object {"com.example.app.widget": base64 of Widget.mobileprovision}`) {
		t.Errorf("secret value not explained:\n%s", stdout.String())
	}
	saved, err := config.NewManager().Load()
	if err != nil || !slices.Equal(saved.IOS.Extensions, []string{"com.example.app.widget"}) {
		t.Errorf("ios.extensions = %v, %v", saved.IOS.Extensions, err)
	}
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
	if pem, path, err := signingKey("", signing.TypeStore, dir); err != nil || pem != nil || path != "" {
		t.Fatalf("empty dir: %q %q %v", pem, path, err)
	}
	// A key from before signing sets is reused by every type.
	legacy := filepath.Join(dir, signing.LegacyKeyFileName)
	if err := os.WriteFile(legacy, []byte("legacy"), 0600); err != nil {
		t.Fatal(err)
	}
	if pem, path, err := signingKey("", signing.TypeStore, dir); err != nil || string(pem) != "legacy" || path != legacy {
		t.Fatalf("legacy key: %q %q %v", pem, path, err)
	}
	// The type's own key wins over it.
	typed := filepath.Join(dir, signing.KeyFileName(signing.TypeStore))
	if err := os.WriteFile(typed, []byte("typed"), 0600); err != nil {
		t.Fatal(err)
	}
	if pem, path, err := signingKey("", signing.TypeStore, dir); err != nil || string(pem) != "typed" || path != typed {
		t.Fatalf("typed key: %q %q %v", pem, path, err)
	}
	if pem, path, err := signingKey("", signing.TypeDevelopment, dir); err != nil || string(pem) != "legacy" || path != legacy {
		t.Fatalf("other type falls back to legacy: %q %q %v", pem, path, err)
	}
	// --key beats both.
	explicit := filepath.Join(dir, "mine.key")
	if err := os.WriteFile(explicit, []byte("mine"), 0600); err != nil {
		t.Fatal(err)
	}
	if pem, path, err := signingKey(explicit, signing.TypeStore, dir); err != nil || string(pem) != "mine" || path != explicit {
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
	if replaced := writeSigningProfile(empty, "development", signing.TypeDevelopment); replaced != "" || empty.Profiles["development"].Distribution != "development" {
		t.Fatalf("profile not created: %q %+v", replaced, empty.Profiles)
	}
	// The same distribution keeps the user's spelling; a different one is
	// replaced, the other fields stay, and the old value is reported.
	cfg.Profiles["beta"] = config.Profile{Distribution: "internal", Scheme: "AppBeta"}
	if replaced := writeSigningProfile(cfg, "beta", signing.TypeAdHoc); replaced != "" || cfg.Profiles["beta"].Distribution != "internal" {
		t.Fatalf("same distribution rewritten: %q %+v", replaced, cfg.Profiles["beta"])
	}
	if replaced := writeSigningProfile(cfg, "beta", signing.TypeStore); replaced != "internal" || cfg.Profiles["beta"].Distribution != "store" || cfg.Profiles["beta"].Scheme != "AppBeta" {
		t.Fatalf("different distribution: %q %+v", replaced, cfg.Profiles["beta"])
	}
	if line := profileWritten("beta", signing.TypeStore, "internal"); line != `  Updated: builder.json (profile "beta", distribution store, was internal)` {
		t.Fatalf("line: %s", line)
	}
	if line := profileWritten("beta", signing.TypeStore, ""); strings.Contains(line, "was") {
		t.Fatalf("line: %s", line)
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

	// The set is complete: dispatch as today, no Apple credentials needed. The
	// extension profiles are required only once the app has extensions.
	for _, name := range config.SigningSecretNames("STORE").Names()[:3] {
		store.stored[name] = "x"
	}
	if err := ensureSigningSecrets(ctx, cfg, store, noASC, "store", "", io.Discard); err != nil {
		t.Fatalf("complete set: %v", err)
	}
	cfg.IOS.Extensions = []string{"com.example.app.widget"}
	if err := ensureSigningSecrets(ctx, cfg, store, noASC, "store", "", io.Discard); err == nil || !strings.Contains(err.Error(), "auth apple") {
		t.Fatalf("missing extension profiles accepted: %v", err)
	}
	store.stored["IOS_EXTENSION_PROFILES_STORE"] = "{}"
	if err := ensureSigningSecrets(ctx, cfg, store, noASC, "store", "", io.Discard); err != nil {
		t.Fatalf("complete set with extensions: %v", err)
	}
	cfg.IOS.Extensions = nil

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
	var warn strings.Builder
	if err := ensureSigningSecrets(ctx, cfg, store, noASC, "cm", "", &warn); err != nil || store.listed != 0 {
		t.Fatalf("codemagic profile: %v, listed %d", err, store.listed)
	}
	if !strings.Contains(warn.String(), "builder signing setup --distribution store") {
		t.Fatalf("no hint for the unchecked provider: %q", warn.String())
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
	if err := ensureSigningSecrets(ctx, cfg, store, withPortal, "store", "", io.Discard); err != nil || len(portal.Calls()) != 0 || len(store.names) != 4 {
		t.Fatalf("second build: %v, calls %v, uploads %v", err, portal.Calls(), store.names)
	}

	// An extension target found in the project is written to builder.json,
	// gets its own profile, and the whole set is uploaded again with it.
	writeProject(t, "App.xcodeproj", appWithWidgetPbxproj)
	if err := config.NewManager().Save(cfg); err != nil {
		t.Fatal(err)
	}
	portal.Reset()
	if err := ensureSigningSecrets(ctx, cfg, store, withPortal, "store", "", io.Discard); err != nil {
		t.Fatalf("build with a new extension: %v", err)
	}
	if !slices.Equal(cfg.IOS.Extensions, []string{"com.example.app.widget"}) || portal.Count("POST /v1/profiles") != 1 || len(portal.Profiles) != 2 {
		t.Errorf("extensions %v, calls %v", cfg.IOS.Extensions, portal.Calls())
	}
	if saved, err := config.NewManager().Load(); err != nil || !slices.Equal(saved.IOS.Extensions, cfg.IOS.Extensions) {
		t.Errorf("builder.json extensions = %v, %v", saved.IOS.Extensions, err)
	}
	if got, err := signing.DecodeExtensionProfiles(store.stored["IOS_EXTENSION_PROFILES_STORE"]); err != nil || string(got["com.example.app.widget"]) != "profile:prof-3" {
		t.Errorf("extension profiles secret: %q, %v", store.stored["IOS_EXTENSION_PROFILES_STORE"], err)
	}
	if _, err := os.Stat("Builder-store-com.example.app.widget.mobileprovision"); err != nil {
		t.Errorf("extension profile not written: %v", err)
	}

	// Development with no device anywhere cannot be provisioned without
	// prompting: the error sends the user to signing setup.
	err := ensureSigningSecrets(ctx, cfg, store, withPortal, "development", "", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "builder signing setup --distribution development --devices-from-mobai") {
		t.Fatalf("development without devices: %v", err)
	}
	if portal.Count("POST /v1/certificates") != 0 || len(store.names) != 8 {
		t.Fatalf("devices are checked before anything is issued: %v", portal.Calls())
	}

	// With a device on the account the development set follows.
	portal.Devices = []signingtest.Device{{ID: "dev-1", Name: "Jane's iPhone", UDID: "00008030-000000000000001E", Status: "ENABLED"}}
	if err := ensureSigningSecrets(ctx, cfg, store, withPortal, "development", "", io.Discard); err != nil {
		t.Fatalf("development with a registered device: %v", err)
	}
	if len(store.stored) != 8 || store.stored["IOS_CERTIFICATE_DEVELOPMENT"] == "" {
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

// writeSigningKey generates a private key, writes it to path and issues a
// certificate of certType for it on the portal, as a `signing setup` run
// with --out-dir filepath.Dir(path) would have.
func writeSigningKey(t *testing.T, portal *signingtest.Portal, path, certType string) {
	t.Helper()
	keyPEM, _, err := signing.GenerateKeyAndCSR("Jane", "jane@example.com")
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(keyPEM)
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		t.Fatalf("generated key is %T, want RSA", key)
	}
	portal.Issue(certType, &rsaKey.PublicKey, signingtest.Now.AddDate(0, 6, 0))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, keyPEM, 0600); err != nil {
		t.Fatal(err)
	}
}

// TestEnsureSigningSecretsReusesTheKeyInTheRecordedDir: the key of a
// `signing setup --out-dir ~/signing/app` run is found through signing.dir in
// builder.json, so the certificate is reused instead of requested again (and
// refused by Apple, which allows one per type).
func TestEnsureSigningSecretsReusesTheKeyInTheRecordedDir(t *testing.T) {
	t.Chdir(t.TempDir())
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	ctx := context.Background()
	cfg := signedConfig()
	cfg.Signing = &config.SigningConfig{Dir: "~/signing/app"}
	store := newFakeSecrets(t)
	portal := signingtest.New(t)
	withPortal := func() (*asc.Client, error) { return portal.Client(t), nil }
	keyDir := filepath.Join(home, "signing", "app")
	writeSigningKey(t, portal, filepath.Join(keyDir, "ios-signing-store.key"), asc.CertificateTypeDistribution)

	var log strings.Builder
	if err := ensureSigningSecrets(ctx, cfg, store, withPortal, "store", "", &log); err != nil {
		t.Fatalf("on-demand provisioning: %v\n%s", err, log.String())
	}
	if portal.Count("POST /v1/certificates") != 0 || len(store.names) != 4 {
		t.Errorf("the certificate must be reused, not requested: %v, uploads %v", portal.Calls(), store.names)
	}
	// The material lands next to the key, not in the working directory.
	if _, err := os.Stat(filepath.Join(keyDir, "ios-signing-store.p12")); err != nil {
		t.Errorf(".p12 not written to the recorded dir: %v", err)
	}
	if _, err := os.Stat("ios-signing-store.p12"); err == nil {
		t.Error(".p12 written to the working directory")
	}

	// A recorded dir without the key, and Apple refusing another
	// certificate: the error says where the key was looked for and how to
	// pass it.
	cfg.Signing.Dir = "~/signing/other"
	store = newFakeSecrets(t)
	portal.RefuseCertificates = true
	err := ensureSigningSecrets(ctx, cfg, store, withPortal, "store", "", io.Discard)
	if err == nil {
		t.Fatal("a refused certificate must fail the build")
	}
	for _, want := range []string{"ios-signing-store.key", filepath.Join(home, "signing", "other") + ", .", "builder signing setup --distribution store --key <path>", "--out-dir"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q missing from:\n%v", want, err)
		}
	}
}

// TestSigningSetupRecordsTheOutDir: --out-dir goes into builder.json as
// given, tilde included, so the next build finds the key; the default
// working directory is not written.
func TestSigningSetupRecordsTheOutDir(t *testing.T) {
	t.Chdir(t.TempDir())
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cfg := &config.Config{Project: "App", Platform: "ios", GitHub: config.GitHubConfig{Owner: "o", Repo: "r"},
		IOS: config.IOSConfig{BundleID: "com.example.app"}}
	if err := config.NewManager().Save(cfg); err != nil {
		t.Fatal(err)
	}
	portal := signingtest.New(t)
	prev := signingASCClient
	signingASCClient = func() (*asc.Client, error) { return portal.Client(t), nil }
	t.Cleanup(func() { signingASCClient = prev })

	cmd, _, stderr := signingSetupCommand(t, newFakeSecrets(t), "--distribution", "store", "--yes", "--out-dir", "~/signing/app")
	if err := cmd.Execute(); err != nil {
		t.Fatalf("%v\n%s", err, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(home, "signing", "app", "ios-signing-store.key")); err != nil {
		t.Errorf("key not written under --out-dir: %v", err)
	}
	saved, err := config.NewManager().Load()
	if err != nil {
		t.Fatal(err)
	}
	if saved.Signing == nil || saved.Signing.Dir != "~/signing/app" {
		t.Errorf("signing = %+v, want the flag as given", saved.Signing)
	}

	cmd, _, stderr = signingSetupCommand(t, newFakeSecrets(t), "--distribution", "store", "--yes")
	if err := cmd.Execute(); err != nil {
		t.Fatalf("%v\n%s", err, stderr.String())
	}
	if saved, err = config.NewManager().Load(); err != nil || saved.Signing != nil {
		t.Errorf("signing = %+v after the default --out-dir, %v", saved.Signing, err)
	}
}

// signingSetupCommand is `signing setup` with its own flags and buffers, and
// the fake secrets API in place of the GitHub client.
func signingSetupCommand(t *testing.T, store secretStore, args ...string) (cmd *cobra.Command, stdout, stderr *bytes.Buffer) {
	t.Helper()
	prev := signingSecretStore
	signingSecretStore = func() (secretStore, error) { return store, nil }
	t.Cleanup(func() { signingSecretStore = prev })

	cmd = &cobra.Command{Use: "setup", RunE: runSigningSetup, SilenceErrors: true, SilenceUsage: true}
	addSigningSetupFlags(cmd)
	stdout, stderr = &bytes.Buffer{}, &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs(args)
	return cmd, stdout, stderr
}

// TestSigningSetupManualReportsAFailedUpload: a repository Builder cannot
// write to is a message, not a dead end. The values are printed, the build
// profile is written, and only the exit code says it failed.
func TestSigningSetupManualReportsAFailedUpload(t *testing.T) {
	t.Chdir(t.TempDir())
	cfg := &config.Config{Project: "App", Platform: "ios", GitHub: config.GitHubConfig{Owner: "o", Repo: "r"}}
	if err := config.NewManager().Save(cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("ios-signing.p12", []byte("p12 bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	profile := profileBytes("<key>Entitlements</key><dict><key>get-task-allow</key><false/></dict>")
	if err := os.WriteFile("App.mobileprovision", profile, 0600); err != nil {
		t.Fatal(err)
	}
	store := newFakeSecrets(t)
	store.writeErr = errors.New("403 Resource not accessible by integration")

	cmd, stdout, stderr := signingSetupCommand(t, store,
		"--certificate", "ios-signing.p12", "--profile", "App.mobileprovision", "--password", "pw")
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "o/r") {
		t.Fatalf("a failed upload must set the exit code: %v", err)
	}
	if !strings.Contains(stderr.String(), "Error: failed to upload IOS_CERTIFICATE_STORE") || !strings.Contains(stderr.String(), "403") {
		t.Errorf("the failure is not reported on stderr:\n%s", stderr.String())
	}
	for _, want := range []string{"NOT uploaded to o/r", "IOS_CERTIFICATE_STORE", "IOS_CERTIFICATE_PASSWORD_STORE",
		"IOS_PROVISIONING_PROFILE_STORE", "base64 of ios-signing.p12", "base64 of App.mobileprovision", "the .p12 password"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("%q not printed:\n%s", want, stdout.String())
		}
	}
	saved, err := config.NewManager().Load()
	if err != nil {
		t.Fatal(err)
	}
	if saved.Profiles["store"].Distribution != "store" {
		t.Errorf("build profile not written: %+v", saved.Profiles)
	}
}

// TestSigningSetupAutoReportsAFailedUpload is the same for the App Store
// Connect mode: everything Apple issued is kept and printed.
func TestSigningSetupAutoReportsAFailedUpload(t *testing.T) {
	t.Chdir(t.TempDir())
	cfg := &config.Config{Project: "App", Platform: "ios", GitHub: config.GitHubConfig{Owner: "o", Repo: "r"},
		IOS: config.IOSConfig{BundleID: "com.example.app"}}
	if err := config.NewManager().Save(cfg); err != nil {
		t.Fatal(err)
	}
	portal := signingtest.New(t)
	prev := signingASCClient
	signingASCClient = func() (*asc.Client, error) { return portal.Client(t), nil }
	t.Cleanup(func() { signingASCClient = prev })
	store := newFakeSecrets(t)
	store.writeErr = errors.New("403 Resource not accessible by integration")

	cmd, stdout, stderr := signingSetupCommand(t, store, "--distribution", "store", "--yes")
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "o/r") {
		t.Fatalf("a failed upload must set the exit code: %v", err)
	}
	if !strings.Contains(stderr.String(), "Error: failed to upload IOS_CERTIFICATE_STORE") {
		t.Errorf("the failure is not reported on stderr:\n%s", stderr.String())
	}
	for _, want := range []string{"NOT uploaded to o/r", "IOS_PROVISIONING_PROFILE_STORE",
		"base64 of ios-signing-store.p12", "Next: builder ios build --profile store"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("%q not printed:\n%s", want, stdout.String())
		}
	}
	for _, f := range []string{"ios-signing-store.key", "ios-signing-store.p12", "Builder-store-com.example.app.mobileprovision"} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("%s not written: %v", f, err)
		}
	}
	saved, err := config.NewManager().Load()
	if err != nil {
		t.Fatal(err)
	}
	if saved.Profiles["store"].Distribution != "store" {
		t.Errorf("build profile not written: %+v", saved.Profiles)
	}

	// --json says the same in github_upload, and still exits non-zero.
	cmd, jsonOut, _ := signingSetupCommand(t, store, "--distribution", "store", "--yes", "--json")
	if err := cmd.Execute(); err == nil {
		t.Fatal("--json run: a failed upload must set the exit code")
	}
	var res struct {
		SigningSet      string `json:"signing_set"`
		SecretsUploaded bool   `json:"secrets_uploaded"`
		GitHubUpload    string `json:"github_upload"`
	}
	if err := json.Unmarshal(jsonOut.Bytes(), &res); err != nil {
		t.Fatalf("%v:\n%s", err, jsonOut.String())
	}
	if res.SigningSet != "STORE" || res.SecretsUploaded || !strings.Contains(res.GitHubUpload, "403") {
		t.Errorf("result: %+v", res)
	}
}
