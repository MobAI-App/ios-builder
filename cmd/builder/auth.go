package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/MobAI-App/ios-builder/internal/asc"
	"github.com/MobAI-App/ios-builder/internal/auth"
	"github.com/MobAI-App/ios-builder/internal/ci"
	"github.com/MobAI-App/ios-builder/internal/exitcode"
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
	Long: `Authenticates with GitHub using OAuth Device Flow and stores the token securely in your system keychain.

Without a terminal (agents, CI) pipe a token in instead:

  printenv GH_TOKEN | builder auth github --token-stdin

A classic token needs the repo, workflow and gist scopes; missing ones are
named and the token is not saved (exit 3). Fine-grained tokens do not report
their permissions, so they are saved unchecked. --device-flow runs the
browser flow without a terminal: it prints the code and waits for approval.`,
	RunE: runAuthGitHub,
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
	authGitHubCmd.Flags().Bool("token-stdin", false, "Read a GitHub token from stdin (classic token with repo, workflow and gist) instead of the browser flow")
	authGitHubCmd.Flags().Bool("device-flow", false, "Run the browser flow without a terminal: print the code, wait for approval")
	authGitHubCmd.Flags().Bool("json", false, "Print the result as JSON lines (progress goes to stderr)")
	authCmd.AddCommand(authGitHubCmd)
	authLogoutCmd.Flags().Bool("json", false, "Print the result as JSON")
	authCmd.AddCommand(authLogoutCmd)
	for _, name := range []string{"codemagic", "bitrise"} {
		cmd := &cobra.Command{Use: name, Short: "Authenticate with " + name, Args: cobra.NoArgs, RunE: runAuthProvider}
		cmd.Flags().Bool("token-stdin", false, "Read API token from stdin instead of a hidden-input prompt")
		cmd.Flags().Bool("json", false, "Print the result as JSON")
		authCmd.AddCommand(cmd)
	}
	authAppleCmd.Flags().String("issuer-id", "", "Issuer ID from App Store Connect → Users and Access → Integrations")
	authAppleCmd.Flags().String("key-id", "", "Key ID of the API key")
	authAppleCmd.Flags().String("key", "", "Path to the AuthKey_<KEYID>.p8 private key")
	authAppleCmd.Flags().Bool("json", false, "Print the result as JSON")
	authCmd.AddCommand(authAppleCmd)
	statusCmd := &cobra.Command{Use: "status", Short: "Show login availability for all providers", Args: cobra.NoArgs, RunE: runAuthStatus}
	statusCmd.Flags().Bool("json", false, "Print the result as JSON")
	authCmd.AddCommand(statusCmd)
}

// checkGitHubToken and saveGitHubToken are vars so tests need neither GitHub
// nor the keychain.
var (
	checkGitHubToken = auth.CheckGitHubToken
	saveGitHubToken  = auth.SaveToken
)

// githubAuthResult is the JSON of `auth github`. With --device-flow a
// {"event":"device_code",...} line comes first; both are one line each.
type githubAuthResult struct {
	Event         string   `json:"event"` // "authenticated"
	Method        string   `json:"method"`
	Login         string   `json:"login,omitempty"`
	Scopes        []string `json:"scopes"`
	ScopesChecked bool     `json:"scopes_checked"`
	MissingScopes []string `json:"missing_scopes"`
}

func runAuthGitHub(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	out := newOutput(cmd)
	emit := func(v any) { _ = json.NewEncoder(cmd.OutOrStdout()).Encode(v) }

	if fromStdin, _ := cmd.Flags().GetBool("token-stdin"); fromStdin {
		data, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 64*1024))
		if err != nil {
			return err
		}
		token := strings.TrimSpace(string(data))
		if token == "" {
			return exitcode.Usagef("no token on stdin; pipe one in, e.g. printenv GH_TOKEN | builder auth github --token-stdin")
		}
		id, err := checkGitHubToken(ctx, token)
		if err != nil {
			return err
		}
		res := githubAuthResult{Event: "authenticated", Method: "token", Login: id.Login, Scopes: id.Scopes, ScopesChecked: id.ScopesKnown, MissingScopes: []string{}}
		if id.Scopes == nil {
			res.Scopes = []string{}
		}
		if id.ScopesKnown {
			res.MissingScopes = auth.MissingScopes(id.Scopes)
		}
		if len(res.MissingScopes) > 0 {
			if out.json {
				emit(res)
			}
			return exitcode.With(exitcode.Auth, fmt.Errorf("the token lacks the %s scope(s) Builder needs (it has: %s); create a classic token with %s, or run builder auth github in a terminal",
				strings.Join(res.MissingScopes, ", "), strings.Join(id.Scopes, ", "), strings.Join(auth.RequiredScopes, ", ")))
		}
		if err := saveGitHubToken(token); err != nil {
			return err
		}
		if out.json {
			emit(res)
			return nil
		}
		fmt.Fprintf(out.log, "Saved the GitHub token of %s.\n", id.Login)
		if !id.ScopesKnown {
			fmt.Fprintf(out.log, "GitHub does not report the permissions of fine-grained tokens; it needs contents, actions, secrets and workflows read/write on the repository, and gists for ios distribute.\n")
		}
		return nil
	}

	if deviceFlow, _ := cmd.Flags().GetBool("device-flow"); !deviceFlow && !interactive(cmd) {
		return needInput("a GitHub login", "--token-stdin (a token with "+strings.Join(auth.RequiredScopes, ", ")+")", "--device-flow (prints a code to approve in a browser and waits for it)")
	}

	fmt.Fprintln(out.log, "Authenticating with GitHub...")
	token, err := auth.LoginWith(ctx, func(code *auth.DeviceCode) {
		if out.json {
			emit(map[string]any{"event": "device_code", "verification_uri": code.VerificationURI, "user_code": code.UserCode, "expires_in": code.ExpiresIn})
			return
		}
		fmt.Fprintln(out.log)
		fmt.Fprintf(out.log, "  Open: %s\n", code.VerificationURI)
		fmt.Fprintf(out.log, "  Enter code: %s\n", code.UserCode)
		fmt.Fprintln(out.log)
		fmt.Fprintln(out.log, "Waiting for authorization...")
	})
	if err != nil {
		// Denied or expired codes are auth failures; a timeout or Ctrl-C keeps its own code.
		code := exitcode.Code(err)
		if code == exitcode.Failure {
			code = exitcode.Auth
		}
		return exitcode.With(code, fmt.Errorf("authentication failed: %w", err))
	}
	scopes := auth.ParseScopes(token.Scope)
	if out.json {
		emit(githubAuthResult{Event: "authenticated", Method: "device_flow", Scopes: scopes, ScopesChecked: true, MissingScopes: auth.MissingScopes(scopes)})
		return nil
	}
	fmt.Fprintln(out.log)
	fmt.Fprintf(out.log, "Authenticated successfully (scope: %s)\n", token.Scope)
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
	if newOutput(cmd).json {
		return printJSON(cmd, map[string]any{"provider": provider, "removed": true})
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
		if !interactive(cmd) {
			return needInput("a "+name+" API token", "--token-stdin", "set "+strings.ToUpper(name)+"_API_TOKEN")
		}
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
		return exitcode.With(exitcode.Auth, fmt.Errorf("%s authentication failed: %w", name, err))
	}
	if err := auth.StoreProviderToken(name, token); err != nil {
		return err
	}
	if newOutput(cmd).json {
		return printJSON(cmd, map[string]any{"provider": name, "saved": true, "env_override": os.Getenv(strings.ToUpper(name)+"_API_TOKEN") != ""})
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
		if stdin, ok := cmd.InOrStdin().(*os.File); !ok || !term.IsTerminal(int(stdin.Fd())) || !interactive(cmd) {
			return exitcode.Usagef("--issuer-id, --key-id and --key are required without a terminal (or set ASC_ISSUER_ID, ASC_KEY_ID and ASC_KEY_PATH)")
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
		return exitcode.With(exitcode.Auth, fmt.Errorf("the key was rejected by App Store Connect: %w", err))
	}
	if err := auth.StoreAppleCredentials(creds); err != nil {
		return err
	}
	if newOutput(cmd).json {
		return printJSON(cmd, map[string]any{"provider": "apple", "saved": true, "issuer_id": creds.IssuerID, "key_id": creds.KeyID, "env_override": os.Getenv("ASC_ISSUER_ID") != ""})
	}
	fmt.Printf("Verified and saved App Store Connect API key %s.\n", creds.KeyID)
	if os.Getenv("ASC_ISSUER_ID") != "" {
		fmt.Println("ASC_* environment variables are set and take precedence over this saved login.")
	}
	return nil
}

// loginStatus is one provider in `auth status --json`.
type loginStatus struct {
	LoggedIn bool   `json:"logged_in"`
	Source   string `json:"source,omitempty"` // apple: "environment" or "stored"
	KeyID    string `json:"key_id,omitempty"`
	Error    string `json:"error,omitempty"`
}

func runAuthStatus(cmd *cobra.Command, _ []string) error {
	if newOutput(cmd).json {
		status := map[string]loginStatus{}
		for _, name := range []string{"github", "codemagic", "bitrise"} {
			_, err := auth.GetProviderToken(name)
			status[name] = loginStatus{LoggedIn: err == nil}
		}
		creds, source, err := auth.GetAppleCredentials()
		switch {
		case errors.Is(err, auth.ErrNotAuthenticated):
			status["apple"] = loginStatus{}
		case err != nil:
			status["apple"] = loginStatus{Error: err.Error()}
		default:
			status["apple"] = loginStatus{LoggedIn: true, Source: string(source), KeyID: creds.KeyID}
		}
		return printJSON(cmd, status)
	}
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
