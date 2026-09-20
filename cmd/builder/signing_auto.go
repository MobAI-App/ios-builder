package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/MobAI-App/ios-builder/internal/asc"
	"github.com/MobAI-App/ios-builder/internal/config"
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
	*signing.SetupResult
	// GeneratedPassword is set when no password was given: it is printed
	// exactly once, here.
	GeneratedPassword string `json:"generated_password,omitempty"`
}

func stdinIsTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// runSigningAuto is `signing setup` without --certificate/--profile: it
// provisions everything through the App Store Connect API. The prompts, the
// plan and the summary live here; the work is signing.Setup.
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
	signing.SyncExtensions(cfg, out.log)
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

	res := &signingAutoResult{GeneratedPassword: generated}
	res.SetupResult, err = signing.Setup(ctx, client, store, storeErr, cfg, &signing.SetupOptions{
		Type: typ, ProfileName: profileName, BundleID: bundleID, Devices: devices, KeyPEM: keyPEM,
		Password: password, Force: force, OutDir: outDir, OutDirAsGiven: outDirFlag, Log: out.log,
	})
	if err != nil {
		return finish(out, cmd, res, err, nil)
	}
	uploadErr := res.UploadError
	if uploadErr != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "Error: %v\n", uploadErr)
	}
	fmt.Fprintln(out.log, profileWritten(profileName, typ, res.ReplacedDistribution))

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

// The signing-set helpers live in internal/signing so a program embedding
// Builder can provision the same way; these names are what this package and
// its tests call them.
type secretStore = signing.SecretStore

func uploadSigningSet(ctx context.Context, store secretStore, storeErr error, cfg *config.Config, log io.Writer, set string, p12 []byte, password string, profile []byte, extensions map[string][]byte) error {
	return signing.UploadSet(ctx, store, storeErr, cfg, log, set, p12, password, profile, extensions)
}

func uploadSigningSecrets(ctx context.Context, gh secretStore, cfg *config.Config, log io.Writer, set string, p12 []byte, password string, profile []byte, extensions map[string][]byte) error {
	return signing.UploadSecrets(ctx, gh, cfg, log, set, p12, password, profile, extensions)
}

func ensureSigningSecrets(ctx context.Context, cfg *config.Config, store secretStore, ascClient func() (*asc.Client, error), profile, provider string, log io.Writer) error {
	return signing.EnsureSecrets(ctx, cfg, store, ascClient, &signing.EnsureOptions{Profile: profile, Provider: provider, Log: log})
}

func writeSigningProfile(cfg *config.Config, name string, typ signing.Type) (replaced string) {
	return signing.WriteProfile(cfg, name, typ)
}

func configuredBundleID(cfg *config.Config, log io.Writer) string {
	return signing.ConfiguredBundleID(cfg, log)
}

func signingKey(keyPath string, typ signing.Type, dirs ...string) (keyPEM []byte, path string, err error) {
	return signing.ReadKey(keyPath, typ, dirs...)
}

func randomPassword() (string, error) { return signing.RandomPassword() }

func printSigningFiles(w io.Writer, res *signing.AutoResult, generatedPassword string) {
	signing.PrintFiles(w, res, generatedPassword)
}

// signingUploadFailed is what `signing setup` ends with when the set did not
// reach the repository: everything is printed by then, so this only carries
// the exit code and says what is left to do.
func signingUploadFailed(cfg *config.Config) error {
	return fmt.Errorf("the signing set was not uploaded to %s/%s; add the secrets above by hand, or fix the access and run builder signing setup again", cfg.GitHub.Owner, cfg.GitHub.Repo)
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
