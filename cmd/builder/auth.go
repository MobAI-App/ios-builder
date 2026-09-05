package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/MobAI-App/ios-builder/internal/auth"
	"github.com/MobAI-App/ios-builder/internal/ci"
	"github.com/spf13/cobra"
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

var authLogoutCmd = &cobra.Command{
	Use:   "logout [github|codemagic|bitrise]",
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
	if provider != "github" && os.Getenv(strings.ToUpper(provider)+"_API_TOKEN") != "" {
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

func runAuthStatus(_ *cobra.Command, _ []string) error {
	for _, name := range []string{"github", "codemagic", "bitrise"} {
		_, err := auth.GetProviderToken(name)
		state := "login available (not checked remotely)"
		if err != nil {
			state = "not logged in"
		}
		fmt.Printf("%s: %s\n", name, state)
	}
	return nil
}
