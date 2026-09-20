package main

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/MobAI-App/ios-builder/internal/config"
	"github.com/MobAI-App/ios-builder/internal/signing"
	"github.com/manifoldco/promptui"
	"github.com/spf13/cobra"
)

var signingCmd = &cobra.Command{
	Use:   "signing",
	Short: "Code signing commands",
}

var signingSetupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Set up code signing for iOS builds",
	Long: `Sets up code signing for one distribution and writes the build profile that uses it.

Without --certificate/--profile the whole thing is automatic, using the App
Store Connect API key from 'builder auth apple': the App ID is registered if
missing, a certificate is issued for a private key generated here (or --key),
devices are registered (--device, --devices-from-mobai) and a provisioning
profile named "Builder <distribution> <bundle id>" is created. Running it
again is safe: valid material is reused and only what is missing, expired,
invalid or changed is recreated. Nothing is ever revoked.

  --distribution development  Apple Development certificate, devices required (default)
  --distribution ad-hoc       Apple Distribution certificate, devices required
                              (internal is the same thing)
  --distribution store        Apple Distribution certificate, no devices;
                              TestFlight and App Store uploads need this

With --certificate and --profile the files are taken as they are:
- A .p12 file (exported from Keychain Access on a Mac)
- A .cer file downloaded from the Apple Developer portal, together with the
  private key from 'builder signing csr' (--key) — the .p12 is then assembled
  locally, so no Mac is needed at any point
The distribution is read from the .mobileprovision (development, ad-hoc,
store or enterprise).

Extension targets (widgets, share/notification extensions, watch apps, app
clips) need a profile each. Their bundle IDs are ios.extensions in
builder.json, filled in from the local Xcode project; automatic mode creates
"Builder <distribution> <bundle id>" for each, manual mode takes one
--extension-profile per extension.

Either way the command uploads the GitHub repository secrets of the
distribution's signing set — IOS_CERTIFICATE_<SET>, IOS_CERTIFICATE_PASSWORD_<SET>,
IOS_PROVISIONING_PROFILE_<SET> and IOS_EXTENSION_PROFILES_<SET>, with SET one of
DEVELOPMENT, AD_HOC, STORE, ENTERPRISE — and writes a profile in builder.json
(--name, default the distribution name) with that distribution. 'builder ios
build --profile <name>' then signs with the set, and provisions it the same
way when it is missing.

The names and the values to put in them are always printed too, for
Codemagic, Bitrise or a repository this login cannot write to. A failed upload
is reported and the command carries on — the files and the build profile are
written regardless — and it exits non-zero at the end.`,
	RunE: runSigningSetup,
}

var signingCSRCmd = &cobra.Command{
	Use:   "csr",
	Short: "Create a certificate signing request (no Mac needed)",
	Long: `Creates a private key and a certificate signing request (CSR) for the
Apple Developer portal — the same thing Keychain Access does on a Mac.

Writes ios-signing.key and ios-signing.csr to the current directory. Keep the
key private and do not commit it. Upload the CSR at developer.apple.com to
create a certificate, then run:
'builder signing p12 --certificate <downloaded>.cer --key ios-signing.key'.`,
	RunE: runSigningCSR,
}

var signingP12Cmd = &cobra.Command{
	Use:   "p12",
	Short: "Assemble a .p12 from a private key and an Apple certificate",
	Long: `Combines the private key from 'builder signing csr' with the certificate
downloaded from the Apple Developer portal into a password-protected .p12 —
the same file Keychain Access exports on a Mac.

The .p12 works anywhere a Keychain-exported one does: 'builder signing setup',
Sideloadly, AltStore, or importing it on a Mac.`,
	RunE: runSigningP12,
}

func init() {
	signingCmd.AddCommand(signingSetupCmd)
	signingCmd.AddCommand(signingCSRCmd)
	signingCmd.AddCommand(signingP12Cmd)

	addSigningSetupFlags(signingSetupCmd)

	signingCSRCmd.Flags().String("name", "", "Your name (certificate common name)")
	signingCSRCmd.Flags().String("email", "", "Email address of your Apple Developer account")

	signingP12Cmd.Flags().StringP("certificate", "c", "", "Path to the .cer downloaded from the Apple Developer portal")
	signingP12Cmd.Flags().StringP("key", "k", "", "Path to the private key from 'builder signing csr'")
	signingP12Cmd.Flags().StringP("out", "o", "ios-signing.p12", "Path to write the .p12 to")
	signingP12Cmd.Flags().String("password", "", "Password to protect the .p12 (prompted if omitted)")
}

// addSigningSetupFlags registers the flags of `signing setup`; tests build
// their own command with them.
func addSigningSetupFlags(cmd *cobra.Command) {
	cmd.Flags().StringP("certificate", "c", "", "Path to certificate file (.p12, or .cer from the Apple Developer portal)")
	cmd.Flags().StringP("profile", "p", "", "Path to .mobileprovision file")
	cmd.Flags().StringArray("extension-profile", nil, "Path to the .mobileprovision of an extension target listed in ios.extensions (repeatable; with --profile)")
	cmd.Flags().StringP("key", "k", "", "Path to the private key from 'builder signing csr' (required with a .cer; automatic mode reuses it and its certificate)")
	cmd.Flags().String("bundle-id", "", "App bundle ID (default: ios.bundleId in builder.json, else the newest IPA in ./dist)")
	cmd.Flags().String("distribution", "", "Distribution to sign for: development, ad-hoc (internal), store or enterprise (default: the --name profile's, else development; with --profile: read from the file)")
	cmd.Flags().String("name", "", "builder.json profile to write the distribution to (default: the distribution name; an existing profile keeps its other fields, a different distribution in it is replaced)")
	cmd.Flags().StringArray("device", nil, "Device UDID to register (repeatable)")
	cmd.Flags().Bool("devices-from-mobai", false, "Register the physical iOS devices connected to MobAI")
	cmd.Flags().String("out-dir", ".", "Directory for the private key, .p12 and .mobileprovision")
	cmd.Flags().String("password", "", "Password to protect the .p12 (prompted; generated with --yes)")
	cmd.Flags().Bool("force", false, "Issue a new certificate and profile even when valid ones exist")
	cmd.Flags().BoolP("yes", "y", false, "Skip confirmations")
	cmd.Flags().Bool("json", false, "Print the result as JSON (progress goes to stderr)")
}

func runSigningCSR(cmd *cobra.Command, args []string) error {
	name, _ := cmd.Flags().GetString("name")
	email, _ := cmd.Flags().GetString("email")

	var err error
	if name == "" {
		if name, err = promptString("Your name (as on the certificate)", ""); err != nil {
			return err
		}
	}
	if email == "" {
		if email, err = promptString("Apple Developer account email", ""); err != nil {
			return err
		}
	}
	if name == "" || email == "" {
		return fmt.Errorf("name and email are required")
	}

	keyPath := "ios-signing.key"
	csrPath := "ios-signing.csr"
	if _, err := os.Stat(keyPath); err == nil {
		fmt.Printf("%s already exists. Regenerating it invalidates any\n", keyPath)
		fmt.Println("certificate created from the previous CSR.")
		confirm := promptui.Prompt{Label: "Generate a new key", IsConfirm: true}
		if _, err := confirm.Run(); err != nil {
			return fmt.Errorf("keeping the existing key")
		}
	}

	keyPEM, csrPEM, err := signing.GenerateKeyAndCSR(name, email)
	if err != nil {
		return err
	}

	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		return fmt.Errorf("failed to write private key: %w", err)
	}
	if err := os.WriteFile(csrPath, csrPEM, 0644); err != nil {
		return fmt.Errorf("failed to write CSR: %w", err)
	}

	fmt.Println()
	fmt.Printf("Private key: %s\n", keyPath)
	fmt.Printf("CSR:         %s\n", csrPath)
	fmt.Println()
	fmt.Println("Keep the private key safe and do not commit it (add it to .gitignore).")
	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Println("  1. Go to https://developer.apple.com/account/resources/certificates/add")
	fmt.Println("  2. Choose 'Apple Development' (or 'Apple Distribution' for App Store)")
	fmt.Printf("  3. Upload %s and download the certificate (.cer)\n", csrPath)
	fmt.Printf("  4. Run: builder signing setup --certificate <downloaded>.cer --key %s\n", keyPath)

	return nil
}

func promptPassword(label string) (string, error) {
	prompt := promptui.Prompt{Label: label, Mask: '*'}
	password, err := prompt.Run()
	if err != nil {
		return "", fmt.Errorf("failed to read password: %w", err)
	}
	if password == "" {
		return "", fmt.Errorf("a password is required")
	}
	return password, nil
}

// buildP12From reads a key and certificate from disk and assembles a .p12.
func buildP12From(keyPath, certPath, password string) ([]byte, error) {
	keyPEM, err := os.ReadFile(expandPath(keyPath))
	if err != nil {
		return nil, fmt.Errorf("failed to read private key %s: %w", keyPath, err)
	}
	certData, err := os.ReadFile(expandPath(certPath))
	if err != nil {
		return nil, fmt.Errorf("failed to read certificate %s: %w", certPath, err)
	}
	return signing.BuildP12(keyPEM, certData, password)
}

func runSigningP12(cmd *cobra.Command, args []string) error {
	certPath, _ := cmd.Flags().GetString("certificate")
	keyPath, _ := cmd.Flags().GetString("key")
	outPath, _ := cmd.Flags().GetString("out")
	password, _ := cmd.Flags().GetString("password")

	var err error
	if certPath == "" {
		if certPath, err = promptString("Path to certificate (.cer from the Apple Developer portal)", ""); err != nil {
			return err
		}
	}
	if keyPath == "" {
		if keyPath, err = promptString("Path to private key", "ios-signing.key"); err != nil {
			return err
		}
	}
	if certPath == "" || keyPath == "" {
		return fmt.Errorf("certificate and key are required")
	}
	if password == "" {
		if password, err = promptPassword("Password to protect the .p12"); err != nil {
			return err
		}
	}

	p12, err := buildP12From(keyPath, certPath, password)
	if err != nil {
		return err
	}
	if err := os.WriteFile(expandPath(outPath), p12, 0600); err != nil {
		return fmt.Errorf("failed to write .p12: %w", err)
	}

	fmt.Printf("Created %s (do not commit it)\n", outPath)
	return nil
}

// isPortalCertificate reports whether the path looks like a certificate from
// the Apple Developer portal rather than a ready-made .p12 bundle.
func isPortalCertificate(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".cer", ".crt", ".pem":
		return true
	}
	return false
}

// expandPath normalizes a path typed at a prompt (~, quotes, escaped spaces).
func expandPath(path string) string { return signing.ExpandPath(path) }

func runSigningSetup(cmd *cobra.Command, args []string) error {
	if certFlag, _ := cmd.Flags().GetString("certificate"); certFlag == "" {
		if profileFlag, _ := cmd.Flags().GetString("profile"); profileFlag == "" {
			return runSigningAuto(cmd)
		}
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	// A GitHub client that cannot be built is reported with the upload, after
	// the files are read: the values are printed either way.
	store, storeErr := signingSecretStore()
	out := cmd.OutOrStdout()

	// Get certificate path
	certPath, _ := cmd.Flags().GetString("certificate")
	if certPath == "" {
		certPath, err = promptString("Path to .p12 certificate file", "")
		if err != nil {
			return err
		}
	}

	// Validate certificate file
	certPath = expandPath(certPath)
	certData, err := os.ReadFile(certPath)
	if err != nil {
		return fmt.Errorf("failed to read certificate %s: %w", certPath, err)
	}
	fmt.Fprintf(out, "Certificate: %s (%.1f KB)\n", certPath, float64(len(certData))/1024)

	// Get provisioning profile path
	profilePath, _ := cmd.Flags().GetString("profile")
	if profilePath == "" {
		profilePath, err = promptString("Path to .mobileprovision file", "")
		if err != nil {
			return err
		}
	}

	// Validate profile file
	profilePath = expandPath(profilePath)
	profileData, err := os.ReadFile(profilePath)
	if err != nil {
		return fmt.Errorf("failed to read provisioning profile %s: %w", profilePath, err)
	}
	fmt.Fprintf(out, "Profile: %s (%.1f KB)\n", profilePath, float64(len(profileData))/1024)

	distributionFlag, _ := cmd.Flags().GetString("distribution")
	typ, err := manualSigningType(profileData, distributionFlag)
	if err != nil {
		return err
	}
	set, err := config.SigningSet(string(typ))
	if err != nil {
		return err
	}
	profileName, _ := cmd.Flags().GetString("name")
	if profileName == "" {
		profileName = string(typ)
	}
	fmt.Fprintf(out, "Distribution: %s (read from the profile), signing set %s, build profile %q\n", typ, set, profileName)

	signing.SyncExtensions(cfg, out)
	extensionPaths, _ := cmd.Flags().GetStringArray("extension-profile")
	extensionFiles := make(map[string][]byte, len(extensionPaths))
	for _, path := range extensionPaths {
		path = expandPath(path)
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("failed to read provisioning profile %s: %w", path, err)
		}
		extensionFiles[path] = data
	}
	extensionPathByID, err := matchExtensionProfiles(cfg.IOS.Extensions, extensionFiles, typ)
	if err != nil {
		return err
	}
	extensionProfiles := make(map[string][]byte, len(extensionPathByID))
	for _, id := range cfg.IOS.Extensions {
		extensionProfiles[id] = extensionFiles[extensionPathByID[id]]
		fmt.Fprintf(out, "Extension: %s (%s)\n", id, extensionPathByID[id])
	}

	password, _ := cmd.Flags().GetString("password")
	p12Path := certPath
	if isPortalCertificate(certPath) {
		// A .cer from the Apple Developer portal: assemble the .p12 locally
		// from the private key that produced the CSR.
		keyPath, _ := cmd.Flags().GetString("key")
		if keyPath == "" {
			keyPath, err = promptString("Path to private key file (from 'builder signing csr')", "ios-signing.key")
			if err != nil {
				return err
			}
		}
		keyPEM, err := os.ReadFile(expandPath(keyPath))
		if err != nil {
			return fmt.Errorf("failed to read private key %s: %w", keyPath, err)
		}
		if password == "" {
			if password, err = promptPassword("Password to protect the .p12"); err != nil {
				return err
			}
		}
		certData, err = signing.BuildP12(keyPEM, certData, password)
		if err != nil {
			return err
		}
		// Save the .p12: it is the reusable signing identity (Sideloadly,
		// another machine, re-running setup), not a throwaway.
		p12Path = signing.P12FileName(typ)
		if err := os.WriteFile(p12Path, certData, 0600); err != nil {
			return fmt.Errorf("failed to write .p12: %w", err)
		}
		fmt.Fprintf(out, "Assembled .p12: %s (do not commit it)\n", p12Path)
	} else if password == "" {
		if password, err = promptPassword("Certificate password"); err != nil {
			return err
		}
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	fmt.Fprintln(out)
	uploadErr := uploadSigningSet(ctx, store, storeErr, cfg, out, set, certData, password, profileData, extensionProfiles)
	if uploadErr != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "Error: %v\n", uploadErr)
	}

	// The profile is written whatever the upload did: the files exist and the
	// build that uses them is the same either way.
	replaced := writeSigningProfile(cfg, profileName, typ)
	if err := config.NewManager().Save(cfg); err != nil {
		return fmt.Errorf("failed to update config: %w", err)
	}
	fmt.Fprintln(out, profileWritten(profileName, typ, replaced))

	names := config.SigningSecretNames(set)
	fmt.Fprintln(out)
	fmt.Fprintln(out, signingUploadLine(cfg, names, uploadErr))
	fmt.Fprintln(out)
	printSigningSecretValues(out, names, p12Path, profilePath, extensionPathByID)
	fmt.Fprintln(out)
	printSigningNext(out, profileName, typ)
	fmt.Fprintln(out, "To build unsigned, use:")
	fmt.Fprintf(out, "  builder ios build --profile %s --unsigned\n", profileName)

	if uploadErr != nil {
		return signingUploadFailed(cfg)
	}
	return nil
}

// matchExtensionProfiles pairs every extension in ios.extensions with the
// path of the --extension-profile (path → contents) whose app id covers it.
// Each must be of the app profile's type; an extension without a profile, or
// a profile for no listed extension, is an error naming it.
func matchExtensionProfiles(extensions []string, files map[string][]byte, typ signing.Type) (map[string]string, error) {
	appIDs := make(map[string]string, len(files))
	for _, path := range slices.Sorted(maps.Keys(files)) {
		fileType, err := signing.ProfileType(files[path])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if fileType != typ {
			return nil, fmt.Errorf("%s is a %s profile, but the app profile is %s; every extension profile must be of the same type", path, fileType, typ)
		}
		if appIDs[path], err = signing.ProfileBundleID(files[path]); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	// The longest app id is the most specific, so an exact profile wins over
	// a wildcard one covering the same extension.
	paths := slices.SortedFunc(maps.Keys(appIDs), func(a, b string) int {
		return len(appIDs[b]) - len(appIDs[a])
	})
	profiles := make(map[string]string, len(extensions))
	var problems []string
	for _, path := range paths {
		covered := false
		for _, id := range extensions {
			if signing.Covers(appIDs[path], id) {
				covered = true
				if _, ok := profiles[id]; !ok {
					profiles[id] = path
				}
			}
		}
		if !covered {
			problems = append(problems, fmt.Sprintf("%s covers %s, which is not in ios.extensions", path, appIDs[path]))
		}
	}
	for _, id := range extensions {
		if _, ok := profiles[id]; !ok {
			problems = append(problems, fmt.Sprintf("extension %s has no profile; pass --extension-profile <mobileprovision> for it", id))
		}
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("extension profiles do not match ios.extensions in builder.json:\n  %s", strings.Join(problems, "\n  "))
	}
	return profiles, nil
}

// manualSigningType is what the .mobileprovision says it is. A --distribution
// that disagrees is an error, since the runner refuses such a pair.
func manualSigningType(profileData []byte, distributionFlag string) (signing.Type, error) {
	typ, err := signing.ProfileType(profileData)
	if err != nil {
		return "", err
	}
	if distributionFlag == "" {
		return typ, nil
	}
	want, err := signing.ParseType(distributionFlag)
	if err != nil {
		return "", err
	}
	if want != typ {
		return "", fmt.Errorf("the profile is a %s profile, but --distribution %s was given; builds with distribution %s would refuse it", typ, want, want)
	}
	return typ, nil
}
