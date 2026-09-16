package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/MobAI-App/ios-builder/internal/asc"
	"github.com/MobAI-App/ios-builder/internal/auth"
	"github.com/MobAI-App/ios-builder/internal/ci"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Authentication commands",
}

var authGitHubCmd = &cobra.Command{
	Use:   "github",
	Short: "Authenticate with GitHub",
	Long:  `Authenticates with GitHub using OAuth Device Flow and stores the token securely in your system keychain.`,
	RunE:  runAuthGitHub,
}

var authAppleCmd = &cobra.Command{
	Use:   "apple",
	Short: "Authenticate with App Store Connect (API key)",
	Long: `Saves an App Store Connect API key for builder ios upload and builder ios submit.

Create the key in App Store Connect under Users and Access → Integrations →
App Store Connect API (Team key, role App Manager or Admin). Note the Issuer ID
and Key ID shown there and download the AuthKey_<KEYID>.p8 file; Apple lets you
download it only once.

Flags left out are prompted for. In CI, set ASC_ISSUER_ID, ASC_KEY_ID and
ASC_PRIVATE_KEY (or ASC_KEY_PATH) instead; they take precedence over the saved login.`,
	Args: cobra.NoArgs,
	RunE: runAuthApple,
}

var authLogoutCmd = &cobra.Command{
	Use:   "logout [github|codemagic|bitrise|apple]",
	Args:  cobra.MaximumNArgs(1),
	Short: "Remove stored credentials",
	RunE:  runAuthLogout,
}

func init() {
	authCmd.AddCommand(authGitHubCmd)
	authCmd.AddCommand(authLogoutCmd)
	for _, name := range []string{"codemagic", "bitrise"} {
		cmd := &cobra.Command{Use: name, Short: "Authenticate with " + name, Args: cobra.NoArgs, RunE: runAuthProvider}
		cmd.Flags().Bool("token-stdin", false, "Read API token from stdin instead of a hidden-input prompt")
		authCmd.AddCommand(cmd)
	}
	authAppleCmd.Flags().String("issuer-id", "", "Issuer ID from App Store Connect → Users and Access → Integrations")
	authAppleCmd.Flags().String("key-id", "", "Key ID of the API key")
	authAppleCmd.Flags().String("key", "", "Path to the AuthKey_<KEYID>.p8 private key")
	authCmd.AddCommand(authAppleCmd)
	authCmd.AddCommand(&cobra.Command{Use: "status", Short: "Show login availability for all providers", Args: cobra.NoArgs, RunE: runAuthStatus})
}

func runAuthGitHub(cmd *cobra.Command, args []string) error {
	fmt.Println("Authenticating with GitHub...")

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	token, err := auth.Login(ctx)
	if err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}

	fmt.Println()
	fmt.Printf("Authenticated successfully (scope: %s)\n", token.Scope)
	return nil
}

func runAuthLogout(cmd *cobra.Command, args []string) error {
	provider := "github"
	if len(args) == 1 {
		provider = args[0]
	}
	if err := auth.LogoutProvider(provider); err != nil {
		return err
	}
	fmt.Printf("Removed saved %s login\n", provider)
	switch {
	case provider == "apple" && os.Getenv("ASC_ISSUER_ID") != "":
		fmt.Println("ASC_* environment variables are still set; unset them in your shell to stop using them.")
	case provider != "github" && provider != "apple" && os.Getenv(strings.ToUpper(provider)+"_API_TOKEN") != "":
		fmt.Println("An environment token is still set; unset it in your shell to stop using it.")
	}
	return nil
}

func runAuthProvider(cmd *cobra.Command, _ []string) error {
	name := cmd.Name()
	fromStdin, _ := cmd.Flags().GetBool("token-stdin")
	var token string
	if fromStdin {
		data, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 64*1024))
		if err != nil {
			return err
		}
		token = strings.TrimSpace(string(data))
	} else {
		fmt.Printf("Create a personal API token in your %s account settings.\n", name)
		var err error
		token, err = readProviderToken(cmd.Context(), cmd.InOrStdin(), cmd.ErrOrStderr())
		if err != nil {
			return err
		}
		token = strings.TrimSpace(token)
	}
	if token == "" {
		return fmt.Errorf("API token is empty")
	}
	if err := ci.ValidateToken(cmd.Context(), name, token); err != nil {
		return fmt.Errorf("%s authentication failed: %w", name, err)
	}
	if err := auth.StoreProviderToken(name, token); err != nil {
		return err
	}
	fmt.Printf("Saved %s login. Other provider logins are unchanged.\n", name)
	if os.Getenv(strings.ToUpper(name)+"_API_TOKEN") != "" {
		fmt.Printf("%s_API_TOKEN is set and takes precedence over this saved login.\n", strings.ToUpper(name))
	}
	return nil
}

func runAuthApple(cmd *cobra.Command, _ []string) error {
	issuerID, _ := cmd.Flags().GetString("issuer-id")
	keyID, _ := cmd.Flags().GetString("key-id")
	keyPath, _ := cmd.Flags().GetString("key")
	if issuerID == "" || keyID == "" || keyPath == "" {
		if stdin, ok := cmd.InOrStdin().(*os.File); !ok || !term.IsTerminal(int(stdin.Fd())) {
			return fmt.Errorf("--issuer-id, --key-id and --key are required without a terminal (or set ASC_ISSUER_ID, ASC_KEY_ID and ASC_KEY_PATH)")
		}
		fmt.Println("App Store Connect → Users and Access → Integrations → App Store Connect API")
		var err error
		if issuerID == "" {
			if issuerID, err = promptString("Issuer ID", ""); err != nil {
				return err
			}
		}
		if keyID == "" {
			if keyID, err = promptString("Key ID", ""); err != nil {
				return err
			}
		}
		if keyPath == "" {
			if keyPath, err = promptString("Path to AuthKey_"+keyID+".p8", ""); err != nil {
				return err
			}
		}
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return fmt.Errorf("read private key: %w", err)
	}
	creds := auth.AppleCredentials{IssuerID: strings.TrimSpace(issuerID), KeyID: strings.TrimSpace(keyID), PrivateKey: auth.NormalizePEM(string(keyPEM))}
	client, err := asc.NewClient(asc.Credentials{IssuerID: creds.IssuerID, KeyID: creds.KeyID, PrivateKey: creds.PrivateKey})
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if err := client.CheckAccess(ctx); err != nil {
		return fmt.Errorf("the key was rejected by App Store Connect: %w", err)
	}
	if err := auth.StoreAppleCredentials(creds); err != nil {
		return err
	}
	fmt.Printf("Saved Apple login (key %s). Other provider logins are unchanged.\n", creds.KeyID)
	if os.Getenv("ASC_ISSUER_ID") != "" {
		fmt.Println("ASC_* environment variables are set and take precedence over this saved login.")
	}
	return nil
}

func runAuthStatus(_ *cobra.Command, _ []string) error {
	for _, name := range []string{"github", "codemagic", "bitrise"} {
		_, err := auth.GetProviderToken(name)
		state := "login available (not checked remotely)"
		if err != nil {
			state = "not logged in"
		}
		fmt.Printf("%s: %s\n", name, state)
	}
	creds, source, err := auth.GetAppleCredentials()
	switch {
	case errors.Is(err, auth.ErrNotAuthenticated):
		fmt.Println("apple: not logged in")
	case err != nil:
		fmt.Printf("apple: %v\n", err)
	case source == auth.AppleSourceEnv:
		fmt.Printf("apple: login available from ASC_* environment (key %s, not checked remotely)\n", creds.KeyID)
	default:
		fmt.Printf("apple: login available (key %s, not checked remotely)\n", creds.KeyID)
	}
	return nil
}
