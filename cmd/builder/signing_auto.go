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
	"strings"

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
	Provider        string `json:"provider"`
	SecretsUploaded bool   `json:"secrets_uploaded"`
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
	typeFlag, _ := cmd.Flags().GetString("type")
	typ, err := signing.ParseType(typeFlag)
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
	var ghClient *github.Client
	if provider == "github" {
		if ghClient, err = getGitHubClient(); err != nil {
			return err
		}
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
	keyPEM, keyPath, err := signingKey(cmd, outDir)
	if err != nil {
		return err
	}

	// The plan, then one confirmation before anything is created.
	fmt.Fprintf(out.log, "Bundle ID: %s\n", bundleID)
	fmt.Fprintf(out.log, "Type:      %s\n", typ)
	if typ.NeedsDevices() {
		fmt.Fprintf(out.log, "Devices:   %s\n", describeDevices(devices))
	}
	if keyPath != "" {
		fmt.Fprintf(out.log, "Key:       %s (reusing its certificate if one is valid)\n", keyPath)
	} else {
		fmt.Fprintf(out.log, "Key:       new, written to %s\n", filepath.Join(outDir, signing.KeyFileName))
	}
	fmt.Fprintf(out.log, "Provider:  %s\n", provider)
	if force {
		fmt.Fprintln(out.log, "Force:     a new certificate and profile will be issued")
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

	res := &signingAutoResult{Provider: provider, GeneratedPassword: generated}
	res.AutoResult, err = signing.Auto(ctx, client, &signing.AutoOptions{
		BundleID: bundleID, Type: typ, Devices: devices, KeyPEM: keyPEM, CommonName: cfg.Project,
		Password: password, Force: force, OutDir: outDir, Log: out.log,
	})
	if err != nil {
		return finish(out, cmd, res, err, nil)
	}
	if ghClient != nil {
		fmt.Fprintf(out.log, "\nUploading secrets to %s/%s...\n", cfg.GitHub.Owner, cfg.GitHub.Repo)
		if err := uploadSigningSecrets(ctx, ghClient, cfg, out.log, res.P12, password, res.ProfileContent); err != nil {
			return finish(out, cmd, res, err, nil)
		}
		res.SecretsUploaded = true
		cfg.IOS.Signing = true
	}
	if cfg.IOS.BundleID == "" {
		cfg.IOS.BundleID = bundleID
	}
	if err := config.NewManager().Save(cfg); err != nil {
		return finish(out, cmd, res, fmt.Errorf("failed to update config: %w", err), nil)
	}
	fmt.Fprintln(out.log, "  Updated: builder.json")

	return finish(out, cmd, res, nil, func() { printSigningSummary(cfg, res) })
}

// resolveSigningBundleID takes the flag, then builder.json, then the newest
// IPA in ./dist, then asks (only in a terminal).
func resolveSigningBundleID(cmd *cobra.Command, cfg *config.Config, out output) (string, error) {
	if id, _ := cmd.Flags().GetString("bundle-id"); id != "" {
		return strings.TrimSpace(id), nil
	}
	if cfg.IOS.BundleID != "" {
		return cfg.IOS.BundleID, nil
	}
	if path, err := ipa.Newest("dist"); err == nil {
		if id := ipa.BundleID(path); id != "" {
			fmt.Fprintf(out.log, "Bundle ID %s read from %s\n", id, path)
			return id, nil
		}
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
		return nil, fmt.Errorf("--type %s profiles list no devices; drop --device/--devices-from-mobai", typ)
	}
	var devices []signing.Device
	for _, u := range udids {
		devices = append(devices, signing.Device{UDID: strings.TrimSpace(u)})
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
	found := 0
	for _, d := range connected {
		if d.Virtual || (d.Platform != "" && !strings.EqualFold(d.Platform, "ios")) {
			continue
		}
		devices = append(devices, signing.Device{Name: d.Name, UDID: d.ID})
		found++
	}
	if found == 0 {
		return nil, errors.New("MobAI has no physical iOS device connected; plug one in or pass --device <udid>")
	}
	return devices, nil
}

// signingKey returns --key, else the key a previous run left in outDir, else
// nil so a key is generated. keyPath is "" when generating.
func signingKey(cmd *cobra.Command, outDir string) (keyPEM []byte, keyPath string, err error) {
	keyPath, _ = cmd.Flags().GetString("key")
	if keyPath == "" {
		candidate := filepath.Join(outDir, signing.KeyFileName)
		if _, err := os.Stat(candidate); err != nil {
			return nil, "", nil
		}
		keyPath = candidate
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

// uploadSigningSecrets encrypts and stores the three signing secrets.
func uploadSigningSecrets(ctx context.Context, gh *github.Client, cfg *config.Config, log io.Writer, p12 []byte, password string, profile []byte) error {
	publicKey, err := gh.GetPublicKey(ctx, cfg.GitHub.Owner, cfg.GitHub.Repo)
	if err != nil {
		return fmt.Errorf("failed to get repository public key: %w", err)
	}
	secrets := []struct{ name, value string }{
		{"IOS_CERTIFICATE", base64.StdEncoding.EncodeToString(p12)},
		{"IOS_CERTIFICATE_PASSWORD", password},
		{"IOS_PROVISIONING_PROFILE", base64.StdEncoding.EncodeToString(profile)},
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
	if res.Files.Key != "" {
		fmt.Printf("Private key: %s\n", res.Files.Key)
	}
	fmt.Printf("Certificate: %s\n", res.Files.P12)
	fmt.Printf("Profile:     %s\n", res.Files.Profile)
	if res.GeneratedPassword != "" {
		fmt.Printf("Password:    %s (generated; shown only now)\n", res.GeneratedPassword)
	}
	fmt.Println("Keep these out of git (add them to .gitignore); gitignored files are also left out of build snapshots.")
	fmt.Println()
	if res.SecretsUploaded {
		fmt.Printf("Secrets uploaded to %s/%s and ios.signing enabled in builder.json.\n", cfg.GitHub.Owner, cfg.GitHub.Repo)
	} else {
		fmt.Printf("%s secrets are set in its dashboard, not by Builder. Add:\n", res.Provider)
		fmt.Printf("  IOS_CERTIFICATE           base64 of %s\n", res.Files.P12)
		fmt.Println("  IOS_CERTIFICATE_PASSWORD  the .p12 password")
		fmt.Printf("  IOS_PROVISIONING_PROFILE  base64 of %s\n", res.Files.Profile)
		fmt.Printf("then set ios.signing to true in builder.json. Steps: %s\n", providerSecretsDoc)
	}
	fmt.Println()
	fmt.Println("Next: builder ios build")
	if res.Type == signing.TypeAppStore {
		fmt.Println(`App Store builds need "configuration": "Release" under ios in builder.json; then builder ios upload --wait.`)
	}
	fmt.Println("Run builder signing setup again any time: it reuses what is valid and renews only what expired or changed.")
}
