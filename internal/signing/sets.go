package signing

// Signing sets: the CI secrets of one distribution (IOS_CERTIFICATE_<SET>, ...),
// provisioned through App Store Connect and uploaded to the GitHub repository.
// `builder signing setup` and the on-demand path of `ios build --profile` /
// `ios release` both come through here, and so does any program embedding
// Builder as a library: nothing in this file prompts or prints a summary.

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/MobAI-App/ios-builder/internal/asc"
	"github.com/MobAI-App/ios-builder/internal/config"
	"github.com/MobAI-App/ios-builder/internal/github"
	"github.com/MobAI-App/ios-builder/internal/ipa"
	"github.com/MobAI-App/ios-builder/internal/xcodeproj"
)

// SecretStore is the part of the GitHub client signing writes through and
// reads the secret names back from.
type SecretStore interface {
	GetPublicKey(ctx context.Context, owner, repo string) (*github.PublicKey, error)
	CreateOrUpdateSecret(ctx context.Context, owner, repo, name, encryptedValue, keyID string) error
	ListSecretNames(ctx context.Context, owner, repo string) ([]string, error)
}

// Commands names the CLI commands error messages point at, so a program that
// embeds this package can name its own verbs instead of Builder's.
type Commands struct {
	// AuthApple saves an App Store Connect API key: "builder auth apple".
	AuthApple string
	// Setup provisions a signing set: "builder signing setup".
	Setup string
}

// BuilderCommands are the builder CLI's own verbs, the default.
var BuilderCommands = Commands{AuthApple: "builder auth apple", Setup: "builder signing setup"}

func (c Commands) orBuilder() Commands {
	if c.AuthApple == "" {
		c.AuthApple = BuilderCommands.AuthApple
	}
	if c.Setup == "" {
		c.Setup = BuilderCommands.Setup
	}
	return c
}

// SetupOptions drives Setup, the automatic half of `signing setup`.
type SetupOptions struct {
	Type Type
	// ProfileName is the builder.json profile written with the distribution;
	// empty means the distribution's own name.
	ProfileName string
	BundleID    string
	Devices     []Device
	// KeyPEM reuses a private key; nil generates one.
	KeyPEM   []byte
	Password string
	Force    bool
	// OutDir receives the key, .p12 and profiles; OutDirAsGiven is the same
	// path as the user typed it (a ~ stays a ~) and is what builder.json
	// records. Empty OutDir means the working directory.
	OutDir        string
	OutDirAsGiven string
	// Log receives progress lines; nil discards them.
	Log io.Writer
}

// SetupResult is what Setup reports. A failed upload is not an error: the
// material exists and the values are printed either way, so it is recorded
// in UploadError for the caller to report and turn into an exit code.
type SetupResult struct {
	*AutoResult
	// SigningSet is the suffix of the secrets written (DEVELOPMENT, AD_HOC,
	// STORE), which builds select by their profile's distribution.
	SigningSet      string `json:"signing_set"`
	SecretsUploaded bool   `json:"secrets_uploaded"`
	// GitHubUpload is "ok" or why the upload failed.
	GitHubUpload string `json:"github_upload"`
	// BuildProfile is the builder.json profile written with the distribution.
	BuildProfile string `json:"build_profile"`
	// ReplacedDistribution is the distribution the profile had before, when
	// it was a different one.
	ReplacedDistribution string `json:"replaced_distribution,omitempty"`
	// UploadError is the failed upload, nil when the secrets reached GitHub.
	UploadError error `json:"-"`
}

// Setup provisions a signing set through App Store Connect, uploads it to
// the repository in cfg and records the profile in builder.json: everything
// `builder signing setup` does after its confirmation prompt. storeErr is a
// secret store that could not be built (no GitHub login); it is reported
// like a failed upload once the material exists. A partial result comes back
// with an error from provisioning.
func Setup(ctx context.Context, client *asc.Client, store SecretStore, storeErr error, cfg *config.Config, opts *SetupOptions) (*SetupResult, error) {
	if opts.Type == TypeEnterprise {
		return nil, errors.New("enterprise (in-house) profiles are not issued through the App Store Connect API; download the certificate and profile from the portal and pass --certificate and --profile")
	}
	profileName := opts.ProfileName
	if profileName == "" {
		profileName = string(opts.Type)
	}
	set, err := config.SigningSet(string(opts.Type))
	if err != nil {
		return nil, err
	}
	outDir := opts.OutDir
	if outDir == "" {
		outDir = "."
	}
	res := &SetupResult{SigningSet: set, BuildProfile: profileName}
	res.AutoResult, err = Auto(ctx, client, &AutoOptions{
		BundleID: opts.BundleID, Extensions: cfg.IOS.Extensions, Type: opts.Type, Devices: opts.Devices, KeyPEM: opts.KeyPEM, CommonName: cfg.Project,
		Password: opts.Password, Force: opts.Force, OutDir: outDir, Log: opts.Log,
	})
	if err != nil {
		return res, err
	}
	logf(opts.Log, "")
	res.UploadError = UploadSet(ctx, store, storeErr, cfg, opts.Log, set, res.P12, opts.Password, res.ProfileContent, res.ExtensionProfiles)
	res.SecretsUploaded = res.UploadError == nil
	res.GitHubUpload = "ok"
	if res.UploadError != nil {
		res.GitHubUpload = res.UploadError.Error()
	}

	// The profile is written whatever the upload did: the material exists and
	// the build that uses it is the same either way.
	res.ReplacedDistribution = WriteProfile(cfg, profileName, opts.Type)
	RecordDir(cfg, opts.OutDirAsGiven)
	if cfg.IOS.BundleID == "" {
		cfg.IOS.BundleID = opts.BundleID
	}
	if err := config.NewManager().Save(cfg); err != nil {
		return res, fmt.Errorf("failed to update config: %w", err)
	}
	return res, nil
}

// UploadSet writes the secrets of a set to the GitHub repository in
// builder.json. storeErr is a client that could not be built (no login),
// reported like a failed upload since the values are printed afterwards.
func UploadSet(ctx context.Context, store SecretStore, storeErr error, cfg *config.Config, log io.Writer, set string, p12 []byte, password string, profile []byte, extensions map[string][]byte) error {
	if storeErr != nil {
		return storeErr
	}
	logf(log, "Uploading secrets to %s/%s...", cfg.GitHub.Owner, cfg.GitHub.Repo)
	return UploadSecrets(ctx, store, cfg, log, set, p12, password, profile, extensions)
}

// UploadSecrets encrypts and stores the signing secrets of a set
// (IOS_CERTIFICATE_<SET>, ...). Other sets, and the unsuffixed secrets of
// repositories set up before signing sets, are left alone. The extension
// profiles are written even when empty, so a removed extension's profile
// does not linger in the repository.
func UploadSecrets(ctx context.Context, gh SecretStore, cfg *config.Config, log io.Writer, set string, p12 []byte, password string, profile []byte, extensions map[string][]byte) error {
	publicKey, err := gh.GetPublicKey(ctx, cfg.GitHub.Owner, cfg.GitHub.Repo)
	if err != nil {
		return fmt.Errorf("failed to get repository public key: %w", err)
	}
	names := config.SigningSecretNames(set)
	secrets := []struct{ name, value string }{
		{names.Certificate, base64.StdEncoding.EncodeToString(p12)},
		{names.Password, password},
		{names.Profile, base64.StdEncoding.EncodeToString(profile)},
		{names.Extensions, EncodeExtensionProfiles(extensions)},
	}
	for _, s := range secrets {
		encrypted, err := github.EncryptSecret(publicKey.Key, s.value)
		if err != nil {
			return fmt.Errorf("failed to encrypt %s: %w", s.name, err)
		}
		if err := gh.CreateOrUpdateSecret(ctx, cfg.GitHub.Owner, cfg.GitHub.Repo, s.name, encrypted, publicKey.KeyID); err != nil {
			return fmt.Errorf("failed to upload %s: %w", s.name, err)
		}
		logf(log, "  Uploaded: %s", s.name)
	}
	return nil
}

// MissingSecrets names the secrets of a set that the repository does not
// hold; the extension profiles only count when the app has extensions.
func MissingSecrets(ctx context.Context, gh SecretStore, cfg *config.Config, set string) ([]string, error) {
	have, err := gh.ListSecretNames(ctx, cfg.GitHub.Owner, cfg.GitHub.Repo)
	if err != nil {
		return nil, err // names the repository already
	}
	names := config.SigningSecretNames(set)
	var missing []string
	for _, name := range names.Names() {
		if name == names.Extensions && len(cfg.IOS.Extensions) == 0 {
			continue
		}
		if !slices.Contains(have, name) {
			missing = append(missing, name)
		}
	}
	return missing, nil
}

// EnsureOptions drives EnsureSecrets.
type EnsureOptions struct {
	// Profile is the builder.json profile about to be built; empty means
	// defaultProfile. Provider overrides the profile's CI provider.
	Profile  string
	Provider string
	// Log receives progress lines; nil discards them.
	Log io.Writer
	// Commands names the verbs error messages point at; zero means Builder's.
	Commands Commands
}

// EnsureSecrets provisions a missing or partial signing set through App
// Store Connect without prompts before a build is dispatched, so a
// distribution build never fails on the runner for want of secrets. It stops
// before anything is pushed when there are no Apple credentials, which
// ascClient reports by returning an error.
func EnsureSecrets(ctx context.Context, cfg *config.Config, store SecretStore, ascClient func() (*asc.Client, error), opts *EnsureOptions) error {
	cmds := opts.Commands.orBuilder()
	log := opts.Log
	s, err := cfg.ResolveProfile(opts.Profile)
	if err != nil {
		return err
	}
	provider := opts.Provider
	if provider == "" {
		provider = s.Provider
	}
	name, err := cfg.ProviderName(provider)
	if err != nil {
		return err
	}
	if s.Distribution == "" {
		return nil
	}
	if name != "github" {
		// Codemagic and Bitrise have no secrets API; their runner fails by name.
		logf(log, "Profile %q signs with set %s. Builder cannot check %s secrets; if the build fails on signing, run: %s --distribution %s", s.Profile, s.SigningSet(), name, cmds.Setup, s.Distribution)
		return nil
	}
	typ, set := Type(s.Distribution), s.SigningSet()
	// A secret's contents cannot be read back, so an extension target that
	// appeared since builder.json last listed it is provisioned like a
	// missing secret.
	newExtensions := SyncExtensions(cfg, log)
	missing, err := MissingSecrets(ctx, store, cfg, set)
	if err != nil {
		return err
	}
	if len(missing) == 0 && len(newExtensions) == 0 {
		return nil
	}
	if len(missing) > 0 {
		logf(log, "Profile %q signs with set %s, but %s/%s is missing %s.", s.Profile, set, cfg.GitHub.Owner, cfg.GitHub.Repo, strings.Join(missing, ", "))
	} else {
		logf(log, "Profile %q signs with set %s, but the Xcode project has extension targets the set has no profile for: %s.", s.Profile, set, strings.Join(newExtensions, ", "))
	}
	manual := fmt.Sprintf("%s --certificate <p12> --profile <mobileprovision> --name %s", cmds.Setup, s.Profile)
	if typ == TypeEnterprise {
		return fmt.Errorf("enterprise (in-house) profiles are not issued through the App Store Connect API; upload the files from the portal with %s", manual)
	}
	client, err := ascClient()
	if err != nil {
		return fmt.Errorf("%w\nRun %s and build again to provision the %s set automatically, or upload your own files with %s", err, cmds.AuthApple, set, manual)
	}
	bundleID := ConfiguredBundleID(cfg, log)
	if bundleID == "" {
		return fmt.Errorf("bundle ID unknown: set ios.bundleId in builder.json, or run %s --distribution %s --bundle-id <id>", cmds.Setup, typ)
	}
	dirs := KeyDirs(cfg)
	keyPEM, keyPath, err := ReadKey("", typ, dirs...)
	if err != nil {
		return err
	}
	password, err := RandomPassword()
	if err != nil {
		return err
	}
	logf(log, "Provisioning %s signing for %s through App Store Connect...", typ, bundleID)
	res, err := Auto(ctx, client, &AutoOptions{
		BundleID: bundleID, Extensions: cfg.IOS.Extensions, Type: typ, KeyPEM: keyPEM, CommonName: cfg.Project, Password: password, OutDir: dirs[0], Log: log,
	})
	if err != nil {
		if keyPath == "" && CertificateRefused(err) {
			// Apple has a certificate of this type already, and without its
			// key Builder asked for another: say where the key was looked for.
			return fmt.Errorf("%w\nNo private key of an existing %s certificate was found: looked for %s in %s. Pass the key of the certificate Apple already issued with %s --distribution %s --key <path>, or --out-dir <dir> with the directory that holds it", err, typ, KeyFileName(typ), strings.Join(dirs, ", "), cmds.Setup, typ)
		}
		return err
	}
	// A build cannot go on without the set in the repository, so here the
	// upload is fatal.
	logf(log, "")
	if err := UploadSet(ctx, store, nil, cfg, log, set, res.P12, password, res.ProfileContent, res.ExtensionProfiles); err != nil {
		return err
	}
	logf(log, "")
	PrintFiles(log, res, password)
	if cfg.IOS.BundleID == "" || len(newExtensions) > 0 {
		if cfg.IOS.BundleID == "" {
			cfg.IOS.BundleID = bundleID
		}
		if err := config.NewManager().Save(cfg); err != nil {
			return fmt.Errorf("failed to update config: %w", err)
		}
	}
	logf(log, "")
	return nil
}

// PrintFiles lists what Auto wrote and, when Builder made it up, the .p12
// password: it is printed exactly once.
func PrintFiles(w io.Writer, res *AutoResult, generatedPassword string) {
	if w == nil {
		return
	}
	if res.Files.Key != "" {
		fmt.Fprintf(w, "Private key: %s\n", res.Files.Key)
	}
	fmt.Fprintf(w, "Certificate: %s\n", res.Files.P12)
	fmt.Fprintf(w, "Profile:     %s\n", res.Files.Profile)
	for i := range res.Extensions {
		fmt.Fprintf(w, "Extension:   %s\n", res.Extensions[i].File)
	}
	if generatedPassword != "" {
		fmt.Fprintf(w, "Password:    %s (generated; shown only now)\n", generatedPassword)
	}
	fmt.Fprintln(w, "Keep these out of git (add them to .gitignore); gitignored files are also left out of build snapshots.")
}

// CertificateRefused reports App Store Connect's 409 on a certificate
// request: the team already holds one of that type (or is at its quota).
func CertificateRefused(err error) bool {
	var apiErr *asc.Error
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusConflict && apiErr.Path == "/v1/certificates"
}

// SyncExtensions appends the extension targets of the local Xcode project
// that ios.extensions does not list yet, keeping what was listed by hand (a
// managed Expo project has no project to read until the runner generates it)
// and returning the new ones.
func SyncExtensions(cfg *config.Config, log io.Writer) []string {
	found, err := xcodeproj.ExtensionBundleIDs(cfg.IOS.Path)
	if err != nil {
		logf(log, "Warning: could not read the extension targets of the Xcode project: %v. List their bundle IDs in ios.extensions in builder.json.", err)
		return nil
	}
	var added []string
	for _, id := range found {
		if !slices.Contains(cfg.IOS.Extensions, id) {
			cfg.IOS.Extensions = append(cfg.IOS.Extensions, id)
			added = append(added, id)
		}
	}
	return added
}

// WriteProfile creates or updates the builder.json profile that builds with
// this distribution. Other fields of an existing profile are kept, and so is
// its own spelling of the same distribution (internal stays internal); a
// different distribution is replaced and returned so the caller can say so.
func WriteProfile(cfg *config.Config, name string, typ Type) (replaced string) {
	if cfg.Profiles == nil {
		cfg.Profiles = map[string]config.Profile{}
	}
	p := cfg.Profiles[name]
	if d, err := config.ParseDistribution(p.Distribution); err == nil && d == string(typ) {
		return ""
	}
	replaced = p.Distribution
	p.Distribution = string(typ)
	cfg.Profiles[name] = p
	return replaced
}

// ConfiguredBundleID is ios.bundleId, else the bundle ID of the newest IPA
// in ./dist; empty when neither is there.
func ConfiguredBundleID(cfg *config.Config, log io.Writer) string {
	if cfg.IOS.BundleID != "" {
		return cfg.IOS.BundleID
	}
	if path, err := ipa.Newest("dist"); err == nil {
		if id := ipa.BundleID(path); id != "" {
			logf(log, "Bundle ID %s read from %s", id, path)
			return id
		}
	}
	return ""
}

// ReadKey returns the key at keyPath, else the first ios-signing-<type>.key
// or legacy ios-signing.key in dirs, else nil so a key is generated (path ""
// then).
func ReadKey(keyPath string, typ Type, dirs ...string) (keyPEM []byte, path string, err error) {
	if keyPath == "" {
		keyPath = FindKey(typ, dirs)
		if keyPath == "" {
			return nil, "", nil
		}
	}
	keyPath = ExpandPath(keyPath)
	keyPEM, err = os.ReadFile(keyPath)
	if err != nil {
		return nil, "", fmt.Errorf("failed to read private key %s: %w", keyPath, err)
	}
	return keyPEM, keyPath, nil
}

// FindKey is the first key file of the type in dirs, or "".
func FindKey(typ Type, dirs []string) string {
	for _, dir := range dirs {
		for _, name := range []string{KeyFileName(typ), LegacyKeyFileName} {
			if candidate := filepath.Join(dir, name); fileExists(candidate) {
				return candidate
			}
		}
	}
	return ""
}

// RecordDir keeps `signing setup`'s --out-dir in builder.json as given (a ~
// stays a ~, so the file works for every user of the repo), where on-demand
// provisioning looks for the key first; "." is not written.
func RecordDir(cfg *config.Config, outDir string) {
	outDir = strings.TrimSpace(outDir)
	if filepath.Clean(outDir) == "." {
		cfg.Signing = nil
		return
	}
	cfg.Signing = &config.SigningConfig{Dir: outDir}
}

// KeyDirs is where on-demand provisioning looks for the private key and
// writes the material: the directory `signing setup` recorded, then the
// working directory.
func KeyDirs(cfg *config.Config) []string {
	if cfg.Signing == nil {
		return []string{"."}
	}
	if dir := ExpandPath(cfg.Signing.Dir); dir != "" && filepath.Clean(dir) != "." {
		return []string{dir, "."}
	}
	return []string{"."}
}

// ExpandPath normalizes a path typed at a prompt. The shell never sees these,
// so a leading ~ is not expanded, and dragging a file into the terminal can
// wrap it in quotes and escape spaces.
func ExpandPath(path string) string {
	path = strings.TrimSpace(path)
	path = strings.Trim(path, `"'`)
	path = strings.ReplaceAll(path, `\ `, " ")

	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~"))
	}
	return path
}

// RandomPassword is 128 bits of randomness as URL-safe base64.
func RandomPassword() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate password: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
