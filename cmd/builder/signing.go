package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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

Either way the command uploads the three GitHub repository secrets of the
distribution's signing set — IOS_CERTIFICATE_<SET>, IOS_CERTIFICATE_PASSWORD_<SET>,
IOS_PROVISIONING_PROFILE_<SET>, with SET one of DEVELOPMENT, AD_HOC, STORE,
ENTERPRISE — and writes a profile in builder.json (--name, default the
distribution name) with that distribution. 'builder ios build --profile <name>'
then signs with the set, and provisions it the same way when it is missing.
For Codemagic and Bitrise the command writes the files and prints the secret
names to add in the dashboard instead.`,
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

	signingSetupCmd.Flags().StringP("certificate", "c", "", "Path to certificate file (.p12, or .cer from the Apple Developer portal)")
	signingSetupCmd.Flags().StringP("profile", "p", "", "Path to .mobileprovision file")
	signingSetupCmd.Flags().StringP("key", "k", "", "Path to the private key from 'builder signing csr' (required with a .cer; automatic mode reuses it and its certificate)")
	signingSetupCmd.Flags().String("bundle-id", "", "App bundle ID (default: ios.bundleId in builder.json, else the newest IPA in ./dist)")
	signingSetupCmd.Flags().String("distribution", "", "Distribution to sign for: development, ad-hoc (internal), store or enterprise (default: the --name profile's, else development; with --profile: read from the file)")
	signingSetupCmd.Flags().String("name", "", "builder.json profile to write the distribution to (default: the distribution name; an existing profile keeps its other fields, a different distribution in it is replaced)")
	signingSetupCmd.Flags().StringArray("device", nil, "Device UDID to register (repeatable)")
	signingSetupCmd.Flags().Bool("devices-from-mobai", false, "Register the physical iOS devices connected to MobAI")
	signingSetupCmd.Flags().String("out-dir", ".", "Directory for the private key, .p12 and .mobileprovision")
	signingSetupCmd.Flags().String("password", "", "Password to protect the .p12 (prompted; generated with --yes)")
	signingSetupCmd.Flags().Bool("force", false, "Issue a new certificate and profile even when valid ones exist")
	signingSetupCmd.Flags().BoolP("yes", "y", false, "Skip confirmations")
	signingSetupCmd.Flags().Bool("json", false, "Print the result as JSON (progress goes to stderr)")
	signingSetupCmd.Flags().String("provider", "", "CI provider the secrets are for: github, codemagic or bitrise (default: provider in builder.json, else github)")

	signingCSRCmd.Flags().String("name", "", "Your name (certificate common name)")
	signingCSRCmd.Flags().String("email", "", "Email address of your Apple Developer account")

	signingP12Cmd.Flags().StringP("certificate", "c", "", "Path to the .cer downloaded from the Apple Developer portal")
	signingP12Cmd.Flags().StringP("key", "k", "", "Path to the private key from 'builder signing csr'")
	signingP12Cmd.Flags().StringP("out", "o", "ios-signing.p12", "Path to write the .p12 to")
	signingP12Cmd.Flags().String("password", "", "Password to protect the .p12 (prompted if omitted)")
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

// expandPath normalizes a path typed at a prompt. The shell never sees these,
// so a leading ~ is not expanded, and dragging a file into the terminal can
// wrap it in quotes and escape spaces.
func expandPath(path string) string {
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
	providerFlag, _ := cmd.Flags().GetString("provider")
	provider, err := cfg.ProviderName(providerFlag)
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
	fmt.Printf("Certificate: %s (%.1f KB)\n", certPath, float64(len(certData))/1024)

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
	fmt.Printf("Profile: %s (%.1f KB)\n", profilePath, float64(len(profileData))/1024)

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
	fmt.Printf("Distribution: %s (read from the profile), signing set %s, build profile %q\n", typ, set, profileName)

	var password string
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
		password, err = promptPassword("Password to protect the .p12")
		if err != nil {
			return err
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
		fmt.Printf("Assembled .p12: %s (do not commit it)\n", p12Path)
	} else {
		password, err = promptPassword("Certificate password")
		if err != nil {
			return err
		}
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	fmt.Println()
	if store != nil {
		fmt.Printf("Uploading secrets to %s/%s...\n", cfg.GitHub.Owner, cfg.GitHub.Repo)
		if err := uploadSigningSecrets(ctx, store, cfg, os.Stdout, set, certData, password, profileData); err != nil {
			return err
		}
	} else {
		printProviderSecrets(provider, config.SigningSecretNames(set), p12Path, profilePath)
	}

	replaced := writeSigningProfile(cfg, profileName, typ)
	if err := config.NewManager().Save(cfg); err != nil {
		return fmt.Errorf("failed to update config: %w", err)
	}
	fmt.Println(profileWritten(profileName, typ, replaced))

	fmt.Println()
	fmt.Println("Code signing configured successfully!")
	fmt.Println()
	printSigningNext(profileName, typ)
	fmt.Println("To build unsigned, use:")
	fmt.Printf("  builder ios build --profile %s --unsigned\n", profileName)

	return nil
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
