package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/MobAI-App/ios-builder/internal/asc"
	"github.com/MobAI-App/ios-builder/internal/config"
	"github.com/MobAI-App/ios-builder/internal/github"
	"github.com/MobAI-App/ios-builder/internal/ipa"
	"github.com/MobAI-App/ios-builder/internal/mobai"
	"github.com/MobAI-App/ios-builder/internal/signing"
	"github.com/MobAI-App/ios-builder/internal/xcodeproj"
	"github.com/manifoldco/promptui"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// providerSecretsDoc explains the dashboard steps for Codemagic and Bitrise.
const providerSecretsDoc = "https://github.com/MobAI-App/ios-builder/blob/main/docs/provider-secrets.md"

// signingAutoResult is the JSON output of the automatic `signing setup`.
type signingAutoResult struct {
	*signing.AutoResult
	// SigningSet is the suffix of the secrets written (DEVELOPMENT, AD_HOC,
	// STORE), which builds select by their profile's distribution.
	SigningSet      string `json:"signing_set"`
	SecretsUploaded bool   `json:"secrets_uploaded"`
	// GitHubUpload is "ok" or why the upload failed; the values are printed
	// either way, so a failure is reported, not fatal.
	GitHubUpload string `json:"github_upload"`
	// BuildProfile is the builder.json profile written with the distribution.
	BuildProfile string `json:"build_profile"`
	// GeneratedPassword is set when no password was given: it is printed
	// exactly once, here.
	GeneratedPassword string `json:"generated_password,omitempty"`
}

func stdinIsTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// runSigningAuto is `signing setup` without --certificate/--profile: it
// provisions everything through the App Store Connect API.
func runSigningAuto(cmd *cobra.Command) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	profileName, _ := cmd.Flags().GetString("name")
	distributionFlag, _ := cmd.Flags().GetString("distribution")
	typ, err := setupDistribution(cfg, profileName, distributionFlag)
	if err != nil {
		return err
	}
	if typ == signing.TypeEnterprise {
		return errors.New("enterprise (in-house) profiles are not issued through the App Store Connect API; download the certificate and profile from the portal and pass --certificate and --profile")
	}
	if profileName == "" {
		profileName = string(typ)
	}
	set, err := config.SigningSet(string(typ))
	if err != nil {
		return err
	}
	client, err := signingASCClient()
	if err != nil {
		return err
	}
	// A GitHub client that cannot be built is reported with the upload, after
	// the material exists: the values are printed either way.
	store, storeErr := signingSecretStore()
	out := newOutput(cmd)
	yes, _ := cmd.Flags().GetBool("yes")
	force, _ := cmd.Flags().GetBool("force")
	outDirFlag, _ := cmd.Flags().GetString("out-dir")
	outDir := expandPath(outDirFlag)
	ctx, cancel := commandContext(cmd, false)
	defer cancel()

	bundleID, err := resolveSigningBundleID(cmd, cfg, out)
	if err != nil {
		return err
	}
	syncExtensions(cfg, out.log)
	devices, err := signingDevices(ctx, cmd, cfg, typ)
	if err != nil {
		return err
	}
	keyFlag, _ := cmd.Flags().GetString("key")
	keyPEM, keyPath, err := signingKey(keyFlag, typ, outDir)
	if err != nil {
		return err
	}

	// The plan, then one confirmation before anything is created.
	fmt.Fprintf(out.log, "Bundle ID:    %s\n", bundleID)
	if len(cfg.IOS.Extensions) > 0 {
		fmt.Fprintf(out.log, "Extensions:   %s\n", strings.Join(cfg.IOS.Extensions, ", "))
	}
	fmt.Fprintf(out.log, "Distribution: %s (signing set %s)\n", typ, set)
	fmt.Fprintf(out.log, "Profile:      %s (builder.json)\n", profileName)
	if typ.NeedsDevices() {
		fmt.Fprintf(out.log, "Devices:      %s\n", describeDevices(devices))
	}
	if keyPath != "" {
		fmt.Fprintf(out.log, "Key:          %s (reusing its certificate if one is valid)\n", keyPath)
	} else {
		fmt.Fprintf(out.log, "Key:          new, written to %s\n", filepath.Join(outDir, signing.KeyFileName(typ)))
	}
	fmt.Fprintf(out.log, "Secrets:      %s/%s\n", cfg.GitHub.Owner, cfg.GitHub.Repo)
	if force {
		fmt.Fprintln(out.log, "Force:        a new certificate and profile will be issued")
	}
	fmt.Fprintln(out.log)
	if !yes {
		if !stdinIsTerminal() {
			return errors.New("this creates resources in your Apple Developer account; confirm with --yes when not running in a terminal")
		}
		if _, err := (&promptui.Prompt{Label: "Continue", IsConfirm: true}).Run(); err != nil {
			return errors.New("canceled")
		}
	}

	password, _ := cmd.Flags().GetString("password")
	var generated string
	switch {
	case password != "":
	case yes || !stdinIsTerminal():
		if generated, err = randomPassword(); err != nil {
			return err
		}
		password = generated
	default:
		if password, err = promptPassword("Password to protect the .p12"); err != nil {
			return err
		}
	}

	res := &signingAutoResult{SigningSet: set, BuildProfile: profileName, GeneratedPassword: generated}
	res.AutoResult, err = signing.Auto(ctx, client, &signing.AutoOptions{
		BundleID: bundleID, Extensions: cfg.IOS.Extensions, Type: typ, Devices: devices, KeyPEM: keyPEM, CommonName: cfg.Project,
		Password: password, Force: force, OutDir: outDir, Log: out.log,
	})
	if err != nil {
		return finish(out, cmd, res, err, nil)
	}
	fmt.Fprintln(out.log)
	uploadErr := uploadSigningSet(ctx, store, storeErr, cfg, out.log, set, res.P12, password, res.ProfileContent, res.ExtensionProfiles)
	res.SecretsUploaded = uploadErr == nil
	res.GitHubUpload = "ok"
	if uploadErr != nil {
		res.GitHubUpload = uploadErr.Error()
		fmt.Fprintf(cmd.ErrOrStderr(), "Error: %v\n", uploadErr)
	}

	// The profile is written whatever the upload did: the material exists and
	// the build that uses it is the same either way.
	replaced := writeSigningProfile(cfg, profileName, typ)
	recordSigningDir(cfg, outDirFlag)
	if cfg.IOS.BundleID == "" {
		cfg.IOS.BundleID = bundleID
	}
	if err := config.NewManager().Save(cfg); err != nil {
		return finish(out, cmd, res, fmt.Errorf("failed to update config: %w", err), nil)
	}
	fmt.Fprintln(out.log, profileWritten(profileName, typ, replaced))

	// Everything is printed before the exit code, so finish's success-only
	// hook is not used.
	if !out.json {
		printSigningSummary(out.log, cfg, res, uploadErr)
	}
	if uploadErr != nil {
		return finish(out, cmd, res, signingUploadFailed(cfg), nil)
	}
	return finish(out, cmd, res, nil, nil)
}

// setupDistribution is --distribution, else the distribution of the
// builder.json profile --name points at, else development.
func setupDistribution(cfg *config.Config, profileName, flag string) (signing.Type, error) {
	if flag != "" {
		return signing.ParseType(flag)
	}
	if p, ok := cfg.Profiles[profileName]; ok && p.Distribution != "" {
		return signing.ParseType(p.Distribution)
	}
	return signing.TypeDevelopment, nil
}

// uploadSigningSet writes the secrets of a set to the GitHub repository
// in builder.json. storeErr is a client that could not be built (no login),
// reported like a failed upload since the values are printed afterwards.
func uploadSigningSet(ctx context.Context, store secretStore, storeErr error, cfg *config.Config, log io.Writer, set string, p12 []byte, password string, profile []byte, extensions map[string][]byte) error {
	if storeErr != nil {
		return storeErr
	}
	fmt.Fprintf(log, "Uploading secrets to %s/%s...\n", cfg.GitHub.Owner, cfg.GitHub.Repo)
	return uploadSigningSecrets(ctx, store, cfg, log, set, p12, password, profile, extensions)
}

// signingUploadFailed is what `signing setup` ends with when the set did not
// reach the repository: everything is printed by then, so this only carries
// the exit code and says what is left to do.
func signingUploadFailed(cfg *config.Config) error {
	return fmt.Errorf("the signing set was not uploaded to %s/%s; add the secrets above by hand, or fix the access and run builder signing setup again", cfg.GitHub.Owner, cfg.GitHub.Repo)
}

// syncExtensions appends the extension targets of the local Xcode project
// that ios.extensions does not list yet, keeping what was listed by hand (a
// managed Expo project has no project to read until the runner generates it)
// and returning the new ones.
func syncExtensions(cfg *config.Config, log io.Writer) []string {
	found, err := xcodeproj.ExtensionBundleIDs(cfg.IOS.Path)
	if err != nil {
		fmt.Fprintf(log, "Warning: could not read the extension targets of the Xcode project: %v. List their bundle IDs in ios.extensions in builder.json.\n", err)
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

// writeSigningProfile creates or updates the builder.json profile that builds
// with this distribution. Other fields of an existing profile are kept, and so
// is its own spelling of the same distribution (internal stays internal); a
// different distribution is replaced and returned so the caller can say so.
func writeSigningProfile(cfg *config.Config, name string, typ signing.Type) (replaced string) {
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

// profileWritten is the "Updated: builder.json" line of both setup modes.
func profileWritten(name string, typ signing.Type, replaced string) string {
	line := fmt.Sprintf("  Updated: builder.json (profile %q, distribution %s", name, typ)
	if replaced != "" {
		line += ", was " + replaced
	}
	return line + ")"
}

// resolveSigningBundleID takes the flag, then builder.json, then the newest
// IPA in ./dist, then asks (only in a terminal).
func resolveSigningBundleID(cmd *cobra.Command, cfg *config.Config, out output) (string, error) {
	if id, _ := cmd.Flags().GetString("bundle-id"); id != "" {
		return strings.TrimSpace(id), nil
	}
	if id := configuredBundleID(cfg, out.log); id != "" {
		return id, nil
	}
	if !stdinIsTerminal() || out.json {
		return "", errors.New("bundle ID unknown: pass --bundle-id, set ios.bundleId in builder.json, or build once so ./dist has an IPA to read it from")
	}
	id, err := promptString("App bundle ID (e.g. com.example.app)", "")
	if err != nil {
		return "", err
	}
	if id = strings.TrimSpace(id); id == "" {
		return "", errors.New("a bundle ID is required")
	}
	return id, nil
}

// configuredBundleID is ios.bundleId, else the bundle ID of the newest IPA in
// ./dist; empty when neither is there.
func configuredBundleID(cfg *config.Config, log io.Writer) string {
	if cfg.IOS.BundleID != "" {
		return cfg.IOS.BundleID
	}
	if path, err := ipa.Newest("dist"); err == nil {
		if id := ipa.BundleID(path); id != "" {
			fmt.Fprintf(log, "Bundle ID %s read from %s\n", id, path)
			return id
		}
	}
	return ""
}

// signingDevices collects --device UDIDs and, with --devices-from-mobai, the
// physical iOS devices MobAI has connected.
func signingDevices(ctx context.Context, cmd *cobra.Command, cfg *config.Config, typ signing.Type) ([]signing.Device, error) {
	udids, _ := cmd.Flags().GetStringArray("device")
	fromMobAI, _ := cmd.Flags().GetBool("devices-from-mobai")
	if !typ.NeedsDevices() && (len(udids) > 0 || fromMobAI) {
		return nil, fmt.Errorf("%s profiles list no devices; drop --device/--devices-from-mobai", typ)
	}
	var devices []signing.Device
	for _, u := range udids {
		u = strings.TrimSpace(u)
		if !udidRe.MatchString(u) {
			return nil, fmt.Errorf("--device %q is not a UDID (40 hex digits, or 8-16 hex digits like 00008030-000A1B2C3D4E5F60)", u)
		}
		devices = append(devices, signing.Device{UDID: u})
	}
	if !fromMobAI {
		return devices, nil
	}
	url := cfg.MobAI.URL
	if url == "" {
		url = mobai.DefaultBaseURL
	}
	connected, err := mobai.NewClient(url).ListDevices(ctx)
	if err != nil {
		return nil, fmt.Errorf("list MobAI devices: %w (is MobAI running? try builder mobai ping)", err)
	}
	physical := mobaiSigningDevices(connected)
	if len(physical) == 0 {
		return nil, errors.New("MobAI has no physical iOS device connected; plug one in or pass --device <udid>")
	}
	return append(devices, physical...), nil
}

// udidRe matches an iOS device UDID: 40 hex digits on devices before the
// iPhone XS, 8-16 hex digits since.
var udidRe = regexp.MustCompile(`^(?i)([0-9a-f]{40}|[0-9a-f]{8}-[0-9a-f]{16})$`)

// mobaiSigningDevices keeps the devices whose MobAI ID is a UDID Apple can
// register: physical iOS devices attached to this or a peer machine.
// Simulators and cloud farm devices (their IDs are farm handles) are skipped.
func mobaiSigningDevices(connected []mobai.Device) []signing.Device {
	var devices []signing.Device
	for _, d := range connected {
		if d.Virtual || d.Cloud || (d.Platform != "" && !strings.EqualFold(d.Platform, "ios")) || !udidRe.MatchString(d.ID) {
			continue
		}
		devices = append(devices, signing.Device{Name: d.Name, UDID: d.ID})
	}
	return devices
}

// signingKey returns the key at keyPath (--key), else the first
// ios-signing-<type>.key or legacy ios-signing.key in dirs, else nil so a key
// is generated (path "" then).
func signingKey(keyPath string, typ signing.Type, dirs ...string) (keyPEM []byte, path string, err error) {
	if keyPath == "" {
		keyPath = findSigningKey(typ, dirs)
		if keyPath == "" {
			return nil, "", nil
		}
	}
	keyPath = expandPath(keyPath)
	keyPEM, err = os.ReadFile(keyPath)
	if err != nil {
		return nil, "", fmt.Errorf("failed to read private key %s: %w", keyPath, err)
	}
	return keyPEM, keyPath, nil
}

// findSigningKey is the first key file of the type in dirs, or "".
func findSigningKey(typ signing.Type, dirs []string) string {
	for _, dir := range dirs {
		for _, name := range []string{signing.KeyFileName(typ), signing.LegacyKeyFileName} {
			if candidate := filepath.Join(dir, name); fileExists(candidate) {
				return candidate
			}
		}
	}
	return ""
}

// recordSigningDir keeps `signing setup`'s --out-dir in builder.json as given
// (a ~ stays a ~, so the file works for every user of the repo), where
// on-demand provisioning looks for the key first; "." is not written.
func recordSigningDir(cfg *config.Config, outDir string) {
	outDir = strings.TrimSpace(outDir)
	if filepath.Clean(outDir) == "." {
		cfg.Signing = nil
		return
	}
	cfg.Signing = &config.SigningConfig{Dir: outDir}
}

// signingKeyDirs is where on-demand provisioning looks for the private key
// and writes the material: the directory `signing setup` recorded, then the
// working directory.
func signingKeyDirs(cfg *config.Config) []string {
	if cfg.Signing == nil {
		return []string{"."}
	}
	if dir := expandPath(cfg.Signing.Dir); dir != "" && filepath.Clean(dir) != "." {
		return []string{dir, "."}
	}
	return []string{"."}
}

func describeDevices(devices []signing.Device) string {
	if len(devices) == 0 {
		return "none given; the profile covers the devices already on the account"
	}
	parts := make([]string, 0, len(devices))
	for _, d := range devices {
		if d.Name != "" {
			parts = append(parts, fmt.Sprintf("%s (%s)", d.Name, d.UDID))
		} else {
			parts = append(parts, d.UDID)
		}
	}
	return strings.Join(parts, ", ")
}

// randomPassword is 128 bits of randomness as URL-safe base64.
func randomPassword() (string, error) {
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

// signingSecretStore is the GitHub secrets API `signing setup` uploads
// through, and signingASCClient the App Store Connect client it provisions
// with. Both are vars so tests can replace them.
var (
	signingSecretStore = func() (secretStore, error) {
		gh, err := getGitHubClient()
		if err != nil {
			return nil, err
		}
		return gh, nil
	}
	signingASCClient = getASCClient
)

// secretStore is the part of the GitHub client that signing writes through
// and reads the secret names back from.
type secretStore interface {
	GetPublicKey(ctx context.Context, owner, repo string) (*github.PublicKey, error)
	CreateOrUpdateSecret(ctx context.Context, owner, repo, name, encryptedValue, keyID string) error
	ListSecretNames(ctx context.Context, owner, repo string) ([]string, error)
}

// uploadSigningSecrets encrypts and stores the signing secrets of a set
// (IOS_CERTIFICATE_<SET>, ...). Other sets, and the unsuffixed secrets of
// repositories set up before signing sets, are left alone. The extension
// profiles are written even when empty, so a removed extension's profile
// does not linger in the repository.
func uploadSigningSecrets(ctx context.Context, gh secretStore, cfg *config.Config, log io.Writer, set string, p12 []byte, password string, profile []byte, extensions map[string][]byte) error {
	publicKey, err := gh.GetPublicKey(ctx, cfg.GitHub.Owner, cfg.GitHub.Repo)
	if err != nil {
		return fmt.Errorf("failed to get repository public key: %w", err)
	}
	names := config.SigningSecretNames(set)
	secrets := []struct{ name, value string }{
		{names.Certificate, base64.StdEncoding.EncodeToString(p12)},
		{names.Password, password},
		{names.Profile, base64.StdEncoding.EncodeToString(profile)},
		{names.Extensions, signing.EncodeExtensionProfiles(extensions)},
	}
	for _, s := range secrets {
		encrypted, err := github.EncryptSecret(publicKey.Key, s.value)
		if err != nil {
			return fmt.Errorf("failed to encrypt %s: %w", s.name, err)
		}
		if err := gh.CreateOrUpdateSecret(ctx, cfg.GitHub.Owner, cfg.GitHub.Repo, s.name, encrypted, publicKey.KeyID); err != nil {
			return fmt.Errorf("failed to upload %s: %w", s.name, err)
		}
		fmt.Fprintf(log, "  Uploaded: %s\n", s.name)
	}
	return nil
}

// missingSigningSecrets names the secrets of a set that the repository does
// not hold; the extension profiles only count when the app has extensions.
func missingSigningSecrets(ctx context.Context, gh secretStore, cfg *config.Config, set string) ([]string, error) {
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

// ensureSigningSecrets provisions a missing or partial signing set through
// App Store Connect without prompts before a build is dispatched, so a
// distribution build never fails on the runner for want of secrets. It stops
// before anything is pushed when there are no Apple credentials.
func ensureSigningSecrets(ctx context.Context, cfg *config.Config, store secretStore, ascClient func() (*asc.Client, error), profile, provider string, log io.Writer) error {
	s, err := cfg.ResolveProfile(profile)
	if err != nil {
		return err
	}
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
		fmt.Fprintf(log, "Profile %q signs with set %s. Builder cannot check %s secrets; if the build fails on signing, run: builder signing setup --distribution %s\n", s.Profile, s.SigningSet(), name, s.Distribution)
		return nil
	}
	typ, set := signing.Type(s.Distribution), s.SigningSet()
	// A secret's contents cannot be read back, so an extension target that
	// appeared since builder.json last listed it is provisioned like a
	// missing secret.
	newExtensions := syncExtensions(cfg, log)
	missing, err := missingSigningSecrets(ctx, store, cfg, set)
	if err != nil {
		return err
	}
	if len(missing) == 0 && len(newExtensions) == 0 {
		return nil
	}
	if len(missing) > 0 {
		fmt.Fprintf(log, "Profile %q signs with set %s, but %s/%s is missing %s.\n", s.Profile, set, cfg.GitHub.Owner, cfg.GitHub.Repo, strings.Join(missing, ", "))
	} else {
		fmt.Fprintf(log, "Profile %q signs with set %s, but the Xcode project has extension targets the set has no profile for: %s.\n", s.Profile, set, strings.Join(newExtensions, ", "))
	}
	manual := fmt.Sprintf("builder signing setup --certificate <p12> --profile <mobileprovision> --name %s", s.Profile)
	if typ == signing.TypeEnterprise {
		return fmt.Errorf("enterprise (in-house) profiles are not issued through the App Store Connect API; upload the files from the portal with %s", manual)
	}
	client, err := ascClient()
	if err != nil {
		return fmt.Errorf("%w\nRun builder auth apple and build again to provision the %s set automatically, or upload your own files with %s", err, set, manual)
	}
	bundleID := configuredBundleID(cfg, log)
	if bundleID == "" {
		return fmt.Errorf("bundle ID unknown: set ios.bundleId in builder.json, or run builder signing setup --distribution %s --bundle-id <id>", typ)
	}
	dirs := signingKeyDirs(cfg)
	keyPEM, keyPath, err := signingKey("", typ, dirs...)
	if err != nil {
		return err
	}
	password, err := randomPassword()
	if err != nil {
		return err
	}
	fmt.Fprintf(log, "Provisioning %s signing for %s through App Store Connect...\n", typ, bundleID)
	res, err := signing.Auto(ctx, client, &signing.AutoOptions{
		BundleID: bundleID, Extensions: cfg.IOS.Extensions, Type: typ, KeyPEM: keyPEM, CommonName: cfg.Project, Password: password, OutDir: dirs[0], Log: log,
	})
	if err != nil {
		if keyPath == "" && certificateRefused(err) {
			// Apple has a certificate of this type already, and without its
			// key Builder asked for another: say where the key was looked for.
			return fmt.Errorf("%w\nNo private key of an existing %s certificate was found: looked for %s in %s. Pass the key of the certificate Apple already issued with builder signing setup --distribution %s --key <path>, or --out-dir <dir> with the directory that holds it", err, typ, signing.KeyFileName(typ), strings.Join(dirs, ", "), typ)
		}
		return err
	}
	// A build cannot go on without the set in the repository, so here the
	// upload is fatal.
	fmt.Fprintln(log)
	if err := uploadSigningSet(ctx, store, nil, cfg, log, set, res.P12, password, res.ProfileContent, res.ExtensionProfiles); err != nil {
		return err
	}
	fmt.Fprintln(log)
	printSigningFiles(log, res, password)
	if cfg.IOS.BundleID == "" || len(newExtensions) > 0 {
		if cfg.IOS.BundleID == "" {
			cfg.IOS.BundleID = bundleID
		}
		if err := config.NewManager().Save(cfg); err != nil {
			return fmt.Errorf("failed to update config: %w", err)
		}
	}
	fmt.Fprintln(log)
	return nil
}

// certificateRefused reports App Store Connect's 409 on a certificate request:
// the team already holds one of that type (or is at its quota).
func certificateRefused(err error) bool {
	var apiErr *asc.Error
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusConflict && apiErr.Path == "/v1/certificates"
}

// printSigningFiles lists what was written and, when Builder made it up, the
// .p12 password: it is printed exactly once.
func printSigningFiles(w io.Writer, res *signing.AutoResult, generatedPassword string) {
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

func printSigningSummary(w io.Writer, cfg *config.Config, res *signingAutoResult, uploadErr error) {
	state := func(created bool, reason string) string {
		if !created {
			return "reused"
		}
		if reason != "" && reason != "missing" {
			return "new (" + reason + ")"
		}
		return "new"
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "Bundle ID:   %s (%s)\n", res.BundleID.Identifier, state(res.BundleID.Created, ""))
	fmt.Fprintf(w, "Certificate: %s (%s, expires %s)\n", res.Certificate.Name, state(res.Certificate.Created, ""), res.Certificate.ExpirationDate.Format("2006-01-02"))
	if res.Type.NeedsDevices() {
		fmt.Fprintf(w, "Devices:     %d in the profile, %d registered now\n", res.Devices.InProfile, len(res.Devices.Registered))
	}
	fmt.Fprintf(w, "Profile:     %s (%s, %s, expires %s)\n", res.Profile.Name, state(res.Profile.Created, res.Profile.Reason), strings.ToLower(res.Profile.State), res.Profile.ExpirationDate.Format("2006-01-02"))
	extensionFiles := map[string]string{}
	for i := range res.Extensions {
		ext := &res.Extensions[i]
		fmt.Fprintf(w, "Extension:   %s (App ID %s, profile %s, expires %s)\n", ext.BundleID.Identifier, state(ext.BundleID.Created, ""), state(ext.Profile.Created, ext.Profile.Reason), ext.Profile.ExpirationDate.Format("2006-01-02"))
		extensionFiles[ext.BundleID.Identifier] = ext.File
	}
	fmt.Fprintln(w)
	printSigningFiles(w, res.AutoResult, res.GeneratedPassword)
	fmt.Fprintln(w)
	names := config.SigningSecretNames(res.SigningSet)
	fmt.Fprintln(w, signingUploadLine(cfg, names, uploadErr))
	fmt.Fprintln(w)
	printSigningSecretValues(w, names, res.Files.P12, res.Files.Profile, extensionFiles)
	fmt.Fprintln(w)
	printSigningNext(w, res.BuildProfile, res.Type)
	fmt.Fprintln(w, "Run builder signing setup again any time: it reuses what is valid and renews only what expired or changed.")
}

// signingUploadLine says whether the set reached the GitHub repository.
func signingUploadLine(cfg *config.Config, names config.SigningSecrets, uploadErr error) string {
	if uploadErr != nil {
		return fmt.Sprintf("Secrets were NOT uploaded to %s/%s: %v", cfg.GitHub.Owner, cfg.GitHub.Repo, uploadErr)
	}
	return fmt.Sprintf("Secrets %s uploaded to %s/%s.", strings.Join(names.Names(), ", "), cfg.GitHub.Owner, cfg.GitHub.Repo)
}

// printSigningSecretValues names the secrets of the set and where their
// values come from, whether or not the upload worked: Codemagic, Bitrise and a
// repository this token cannot write to are set by hand.
func printSigningSecretValues(w io.Writer, names config.SigningSecrets, p12Path, profilePath string, extensionFiles map[string]string) {
	fmt.Fprintln(w, "Set them by hand wherever Builder cannot (Codemagic, Bitrise, a repository this login cannot write to):")
	width := len(names.Extensions)
	fmt.Fprintf(w, "  %-*s  base64 of %s\n", width, names.Certificate, p12Path)
	fmt.Fprintf(w, "  %-*s  the .p12 password\n", width, names.Password)
	fmt.Fprintf(w, "  %-*s  base64 of %s\n", width, names.Profile, profilePath)
	if len(extensionFiles) == 0 {
		fmt.Fprintf(w, "  %s  {} (no extension targets)\n", names.Extensions)
	} else {
		entries := make([]string, 0, len(extensionFiles))
		for _, id := range slices.Sorted(maps.Keys(extensionFiles)) {
			entries = append(entries, fmt.Sprintf("%q: base64 of %s", id, extensionFiles[id]))
		}
		fmt.Fprintf(w, "  %s  JSON object {%s}\n", names.Extensions, strings.Join(entries, ", "))
	}
	fmt.Fprintf(w, "Steps: %s\n", providerSecretsDoc)
}

// printSigningNext names the build that reads the set just written.
func printSigningNext(w io.Writer, buildProfile string, typ signing.Type) {
	fmt.Fprintf(w, "Next: builder ios build --profile %s\n", buildProfile)
	if typ == signing.TypeStore {
		fmt.Fprintln(w, "then builder ios upload --wait.")
	}
}
