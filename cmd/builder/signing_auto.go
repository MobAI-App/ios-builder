package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
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
	"github.com/manifoldco/promptui"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// providerSecretsDoc explains the dashboard steps for Codemagic and Bitrise.
const providerSecretsDoc = "https://github.com/MobAI-App/ios-builder/blob/main/docs/provider-secrets.md"

// signingAutoResult is the JSON output of the automatic `signing setup`.
type signingAutoResult struct {
	*signing.AutoResult
	Provider string `json:"provider"`
	// SigningSet is the suffix of the secrets written (DEVELOPMENT, AD_HOC,
	// STORE), which builds select by their profile's distribution.
	SigningSet      string `json:"signing_set"`
	SecretsUploaded bool   `json:"secrets_uploaded"`
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
	client, err := getASCClient()
	if err != nil {
		return err
	}
	provider, err := cfg.ProviderName("")
	if err != nil {
		return err
	}
	var store secretStore
	if provider == "github" {
		ghClient, err := getGitHubClient()
		if err != nil {
			return err
		}
		store = ghClient
	}
	out := newOutput(cmd)
	yes, _ := cmd.Flags().GetBool("yes")
	force, _ := cmd.Flags().GetBool("force")
	outDir, _ := cmd.Flags().GetString("out-dir")
	outDir = expandPath(outDir)
	ctx, cancel := commandContext(cmd, false)
	defer cancel()

	bundleID, err := resolveSigningBundleID(cmd, cfg, out)
	if err != nil {
		return err
	}
	devices, err := signingDevices(ctx, cmd, cfg, typ)
	if err != nil {
		return err
	}
	keyFlag, _ := cmd.Flags().GetString("key")
	keyPEM, keyPath, err := signingKey(keyFlag, outDir, typ)
	if err != nil {
		return err
	}

	// The plan, then one confirmation before anything is created.
	fmt.Fprintf(out.log, "Bundle ID:    %s\n", bundleID)
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
	fmt.Fprintf(out.log, "Provider:     %s\n", provider)
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

	res := &signingAutoResult{Provider: provider, SigningSet: set, BuildProfile: profileName, GeneratedPassword: generated}
	res.AutoResult, err = provisionSigning(ctx, client, store, cfg, out.log, &signing.AutoOptions{
		BundleID: bundleID, Type: typ, Devices: devices, KeyPEM: keyPEM, CommonName: cfg.Project,
		Password: password, Force: force, OutDir: outDir, Log: out.log,
	})
	if err != nil {
		return finish(out, cmd, res, err, nil)
	}
	res.SecretsUploaded = store != nil
	writeSigningProfile(cfg, profileName, typ)
	if cfg.IOS.BundleID == "" {
		cfg.IOS.BundleID = bundleID
	}
	if err := config.NewManager().Save(cfg); err != nil {
		return finish(out, cmd, res, fmt.Errorf("failed to update config: %w", err), nil)
	}
	fmt.Fprintf(out.log, "  Updated: builder.json (profile %q, distribution %s)\n", profileName, typ)

	return finish(out, cmd, res, nil, func() { printSigningSummary(cfg, res) })
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

// provisionSigning issues (or reuses) the certificate and profile of a
// distribution through App Store Connect and, when store is a GitHub
// repository, uploads them as the distribution's signing set. `signing setup`
// runs it, and so does `ios build` when a profile's set is missing.
func provisionSigning(ctx context.Context, client *asc.Client, store secretStore, cfg *config.Config, log io.Writer, opts *signing.AutoOptions) (*signing.AutoResult, error) {
	res, err := signing.Auto(ctx, client, opts)
	if err != nil {
		return res, err
	}
	if store == nil {
		return res, nil
	}
	set, err := config.SigningSet(string(opts.Type))
	if err != nil {
		return res, err
	}
	fmt.Fprintf(log, "\nUploading secrets to %s/%s...\n", cfg.GitHub.Owner, cfg.GitHub.Repo)
	if err := uploadSigningSecrets(ctx, store, cfg, log, set, res.P12, opts.Password, res.ProfileContent); err != nil {
		return res, err
	}
	return res, nil
}

// writeSigningProfile creates or updates the builder.json profile that builds
// with this distribution; other fields of an existing profile are kept.
func writeSigningProfile(cfg *config.Config, name string, typ signing.Type) {
	if cfg.Profiles == nil {
		cfg.Profiles = map[string]config.Profile{}
	}
	p := cfg.Profiles[name]
	p.Distribution = string(typ)
	cfg.Profiles[name] = p
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

// signingKey returns the key at keyPath (--key), else the key a previous run
// of this type left in outDir (ios-signing-<type>.key, or the ios-signing.key
// of runs before signing sets), else nil so a key is generated. The returned
// path is "" when generating.
func signingKey(keyPath, outDir string, typ signing.Type) (keyPEM []byte, path string, err error) {
	if keyPath == "" {
		for _, name := range []string{signing.KeyFileName(typ), signing.LegacyKeyFileName} {
			if candidate := filepath.Join(outDir, name); fileExists(candidate) {
				keyPath = candidate
				break
			}
		}
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

// secretStore is the part of the GitHub client that signing writes through
// and reads the secret names back from.
type secretStore interface {
	GetPublicKey(ctx context.Context, owner, repo string) (*github.PublicKey, error)
	CreateOrUpdateSecret(ctx context.Context, owner, repo, name, encryptedValue, keyID string) error
	ListSecretNames(ctx context.Context, owner, repo string) ([]string, error)
}

// uploadSigningSecrets encrypts and stores the three signing secrets of a set
// (IOS_CERTIFICATE_<SET>, ...). Other sets, and the unsuffixed secrets of
// repositories set up before signing sets, are left alone.
func uploadSigningSecrets(ctx context.Context, gh secretStore, cfg *config.Config, log io.Writer, set string, p12 []byte, password string, profile []byte) error {
	publicKey, err := gh.GetPublicKey(ctx, cfg.GitHub.Owner, cfg.GitHub.Repo)
	if err != nil {
		return fmt.Errorf("failed to get repository public key: %w", err)
	}
	names := config.SigningSecretNames(set)
	secrets := []struct{ name, value string }{
		{names.Certificate, base64.StdEncoding.EncodeToString(p12)},
		{names.Password, password},
		{names.Profile, base64.StdEncoding.EncodeToString(profile)},
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
// not hold.
func missingSigningSecrets(ctx context.Context, gh secretStore, cfg *config.Config, set string) ([]string, error) {
	have, err := gh.ListSecretNames(ctx, cfg.GitHub.Owner, cfg.GitHub.Repo)
	if err != nil {
		return nil, err // names the repository already
	}
	var missing []string
	for _, name := range config.SigningSecretNames(set).Names() {
		if !slices.Contains(have, name) {
			missing = append(missing, name)
		}
	}
	return missing, nil
}

// ensureSigningSecrets runs before a build is dispatched to GitHub: when the
// selected profile has a distribution, its signing set must be in the
// repository. A missing or partial set is provisioned through App Store
// Connect the way `signing setup` does, without prompts; without Apple
// credentials the build stops here, before anything is pushed. The provider
// that will run the job is --provider, else the profile's, else the top-level
// one (as the coordinator resolves it); Codemagic and Bitrise have no secrets
// API, so their builds are left to the runner, which reports a missing set.
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
	if name != "github" || s.Distribution == "" {
		return nil
	}
	typ, set := signing.Type(s.Distribution), s.SigningSet()
	missing, err := missingSigningSecrets(ctx, store, cfg, set)
	if err != nil {
		return err
	}
	if len(missing) == 0 {
		return nil
	}
	fmt.Fprintf(log, "Profile %q signs with set %s, but %s/%s is missing %s.\n", s.Profile, set, cfg.GitHub.Owner, cfg.GitHub.Repo, strings.Join(missing, ", "))
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
	keyPEM, _, err := signingKey("", ".", typ)
	if err != nil {
		return err
	}
	password, err := randomPassword()
	if err != nil {
		return err
	}
	fmt.Fprintf(log, "Provisioning %s signing for %s through App Store Connect...\n", typ, bundleID)
	res, err := provisionSigning(ctx, client, store, cfg, log, &signing.AutoOptions{
		BundleID: bundleID, Type: typ, KeyPEM: keyPEM, CommonName: cfg.Project, Password: password, OutDir: ".", Log: log,
	})
	if err != nil {
		return err
	}
	fmt.Fprintln(log)
	printSigningFiles(log, res, password)
	if cfg.IOS.BundleID == "" {
		cfg.IOS.BundleID = bundleID
		if err := config.NewManager().Save(cfg); err != nil {
			return fmt.Errorf("failed to update config: %w", err)
		}
	}
	fmt.Fprintln(log)
	return nil
}

// printSigningFiles lists what was written and, when Builder made it up, the
// .p12 password: it is printed exactly once.
func printSigningFiles(w io.Writer, res *signing.AutoResult, generatedPassword string) {
	if res.Files.Key != "" {
		fmt.Fprintf(w, "Private key: %s\n", res.Files.Key)
	}
	fmt.Fprintf(w, "Certificate: %s\n", res.Files.P12)
	fmt.Fprintf(w, "Profile:     %s\n", res.Files.Profile)
	if generatedPassword != "" {
		fmt.Fprintf(w, "Password:    %s (generated; shown only now)\n", generatedPassword)
	}
	fmt.Fprintln(w, "Keep these out of git (add them to .gitignore); gitignored files are also left out of build snapshots.")
}

func printSigningSummary(cfg *config.Config, res *signingAutoResult) {
	state := func(created bool, reason string) string {
		if !created {
			return "reused"
		}
		if reason != "" && reason != "missing" {
			return "new (" + reason + ")"
		}
		return "new"
	}
	fmt.Println()
	fmt.Printf("Bundle ID:   %s (%s)\n", res.BundleID.Identifier, state(res.BundleID.Created, ""))
	fmt.Printf("Certificate: %s (%s, expires %s)\n", res.Certificate.Name, state(res.Certificate.Created, ""), res.Certificate.ExpirationDate.Format("2006-01-02"))
	if res.Type.NeedsDevices() {
		fmt.Printf("Devices:     %d in the profile, %d registered now\n", res.Devices.InProfile, len(res.Devices.Registered))
	}
	fmt.Printf("Profile:     %s (%s, %s, expires %s)\n", res.Profile.Name, state(res.Profile.Created, res.Profile.Reason), strings.ToLower(res.Profile.State), res.Profile.ExpirationDate.Format("2006-01-02"))
	fmt.Println()
	printSigningFiles(os.Stdout, res.AutoResult, res.GeneratedPassword)
	fmt.Println()
	names := config.SigningSecretNames(res.SigningSet)
	if res.SecretsUploaded {
		fmt.Printf("Secrets %s, %s and %s uploaded to %s/%s.\n", names.Certificate, names.Password, names.Profile, cfg.GitHub.Owner, cfg.GitHub.Repo)
	} else {
		printProviderSecrets(res.Provider, names, res.Files.P12, res.Files.Profile)
	}
	printSigningNext(res.BuildProfile, res.Type)
	fmt.Println("Run builder signing setup again any time: it reuses what is valid and renews only what expired or changed.")
}

// printProviderSecrets tells Codemagic and Bitrise users what to paste into
// the dashboard, since Builder cannot write secrets there.
func printProviderSecrets(provider string, names config.SigningSecrets, p12Path, profilePath string) {
	fmt.Printf("%s secrets are set in its dashboard, not by Builder. Add:\n", provider)
	fmt.Printf("  %-*s  base64 of %s\n", len(names.Password), names.Certificate, p12Path)
	fmt.Printf("  %s  the .p12 password\n", names.Password)
	fmt.Printf("  %-*s  base64 of %s\n", len(names.Password), names.Profile, profilePath)
	fmt.Printf("Steps: %s\n", providerSecretsDoc)
}

// printSigningNext names the build that reads the set just written.
func printSigningNext(buildProfile string, typ signing.Type) {
	fmt.Printf("Next: builder ios build --profile %s\n", buildProfile)
	if typ == signing.TypeStore {
		fmt.Println("then builder ios upload --wait.")
	}
}
