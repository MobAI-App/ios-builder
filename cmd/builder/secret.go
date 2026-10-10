package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/MobAI-App/ios-builder/internal/auth"
	"github.com/MobAI-App/ios-builder/internal/ci"
	"github.com/MobAI-App/ios-builder/internal/config"
	"github.com/MobAI-App/ios-builder/internal/github"
	"github.com/spf13/cobra"
)

var secretCmd = &cobra.Command{
	Use:   "secret",
	Short: "Manage build secrets held by the CI provider",
	Long: `Builder never hosts secret values. builder secret set stores the value on the
CI provider and records only the name in builder.json ("secrets", top level or
per profile); each build exports the listed names as environment variables.

  GitHub     repository Actions secrets (encrypted with the repository key)
  Codemagic  secure variables in the app's "builder" variable group
  Bitrise    protected app secrets

With --profile the value is stored as NAME__<PROFILE> (the profile name upper
cased, other characters as _) and only that profile's builds list it; a build
with the profile takes NAME__<PROFILE> when it exists, else NAME, and exports it
as NAME either way. The provider is --provider, else the profile's provider,
else builder.json's, else GitHub.`,
}

// secretStoreFor opens the secrets API of a provider and names where the
// secrets go. A var so tests can point the real clients at fake servers.
var secretStoreFor = func(cfg *config.Config, provider string) (ci.SecretStore, string, error) {
	switch provider {
	case "github":
		gh, err := getGitHubClient()
		if err != nil {
			return nil, "", err
		}
		return githubSecrets{gh, cfg.GitHub.Owner, cfg.GitHub.Repo}, "GitHub Actions secrets of " + cfg.GitHub.Owner + "/" + cfg.GitHub.Repo, nil
	case "codemagic", "bitrise":
		ciCfg, err := cfg.ProviderConfig(provider)
		if err != nil {
			return nil, "", err
		}
		token, err := auth.GetProviderToken(provider)
		if err != nil {
			return nil, "", fmt.Errorf("not authenticated with %s; run builder auth %s or set %s_API_TOKEN", provider, provider, strings.ToUpper(provider))
		}
		if provider == "codemagic" {
			return ci.NewCodemagicSecrets(ciCfg.AppID, token), "Codemagic app " + ciCfg.AppID + " (variable group " + ci.CodemagicVariableGroup + ")", nil
		}
		return ci.NewBitriseSecrets(ciCfg.AppID, token), "Bitrise app " + ciCfg.AppID + " secrets", nil
	default:
		return nil, "", fmt.Errorf("unknown provider %q", provider)
	}
}

// githubSecrets is the repository's Actions secrets as a ci.SecretStore.
type githubSecrets struct {
	gh          *github.Client
	owner, repo string
}

func (g githubSecrets) List(ctx context.Context) ([]string, error) {
	return g.gh.ListSecretNames(ctx, g.owner, g.repo)
}

func (g githubSecrets) Set(ctx context.Context, name, value string) error {
	return g.gh.SetSecret(ctx, g.owner, g.repo, name, value)
}

func (g githubSecrets) Delete(ctx context.Context, name string) (bool, error) {
	return g.gh.DeleteSecret(ctx, g.owner, g.repo, name)
}

// secretProvider is where a level's secrets live: --provider, else the
// profile's provider, else the top-level one, else GitHub.
func secretProvider(cfg *config.Config, profile, override string) (string, error) {
	if override == "" && profile != "" {
		override = cfg.Profiles[profile].Provider
	}
	return cfg.ProviderName(override)
}

// readSecretValue takes the value from stdin with --value-stdin (one trailing
// newline dropped, as a shell pipe adds it) or from a hidden prompt; never
// from the command line, where it would land in shell history.
func readSecretValue(cmd *cobra.Command, name string) (string, error) {
	fromStdin, _ := cmd.Flags().GetBool("value-stdin")
	var value string
	if fromStdin {
		data, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 1<<20+1))
		if err != nil {
			return "", fmt.Errorf("read %s from stdin: %w", name, err)
		}
		if len(data) > 1<<20 {
			return "", fmt.Errorf("%s is larger than 1 MiB", name)
		}
		value = strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
	} else {
		var err error
		value, err = readHidden(cmd.Context(), cmd.InOrStdin(), cmd.ErrOrStderr(), name, "--value-stdin")
		if err != nil {
			return "", err
		}
	}
	if value == "" {
		return "", fmt.Errorf("%s is empty; providers do not store empty secrets", name)
	}
	return value, nil
}

var secretSetCmd = &cobra.Command{
	Use:   "set NAME",
	Short: "Store a secret on the CI provider and list its name in builder.json",
	Long: `Stores the value on the CI provider and adds NAME to builder.json's "secrets"
(or the profile's with --profile). The value is read from a hidden prompt, or
from stdin with --value-stdin; it is never an argument and never printed.

  builder secret set SENTRY_TOKEN
  printf %s "$TOKEN" | builder secret set SENTRY_TOKEN --profile production --value-stdin`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		profile, _ := cmd.Flags().GetString("profile")
		override, _ := cmd.Flags().GetString("provider")
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		// Every check that needs no network runs before the value is asked for.
		if err := cfg.AddSecret(profile, name); err != nil {
			return err
		}
		provider, err := secretProvider(cfg, profile, override)
		if err != nil {
			return err
		}
		store, where, err := secretStoreFor(cfg, provider)
		if err != nil {
			return err
		}
		value, err := readSecretValue(cmd, name)
		if err != nil {
			return err
		}
		stored := config.SecretStorageName(name, profile)
		if err := store.Set(cmd.Context(), stored, value); err != nil {
			return fmt.Errorf("failed to store %s in %s: %w", stored, where, err)
		}
		if err := config.NewManager().Save(cfg); err != nil {
			return fmt.Errorf("stored %s in %s, but could not add it to builder.json: %w", stored, where, err)
		}
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "Stored %s in %s.\n", stored, where)
		fmt.Fprintf(out, "Listed %s under %s in builder.json; those builds get it as $%s.\n", name, scopeName(profile), name)
		if provider == "github" && workflowLacksSecrets() {
			fmt.Fprintln(out, "Note: .github/workflows/ios-build.yml predates secrets; run builder init to refresh it, then commit and push it.")
		}
		return nil
	},
}

var secretUnsetCmd = &cobra.Command{
	Use:   "unset NAME",
	Short: "Delete a secret from the CI provider and drop its name from builder.json",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		profile, _ := cmd.Flags().GetString("profile")
		override, _ := cmd.Flags().GetString("provider")
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		listed, err := cfg.RemoveSecret(profile, name)
		if err != nil {
			return err
		}
		provider, err := secretProvider(cfg, profile, override)
		if err != nil {
			return err
		}
		store, where, err := secretStoreFor(cfg, provider)
		if err != nil {
			return err
		}
		stored := config.SecretStorageName(name, profile)
		// Listing first separates "not there" from "no access", which a
		// delete's 404 cannot.
		have, err := store.List(cmd.Context())
		if err != nil {
			return err
		}
		deleted := false
		if slices.Contains(have, stored) {
			if deleted, err = store.Delete(cmd.Context(), stored); err != nil {
				return fmt.Errorf("failed to delete %s from %s: %w", stored, where, err)
			}
		}
		if !listed && !deleted {
			return fmt.Errorf("%s is neither listed for %s in builder.json nor stored in %s", name, scopeName(profile), where)
		}
		if listed {
			if err := config.NewManager().Save(cfg); err != nil {
				return err
			}
		}
		out := cmd.OutOrStdout()
		if deleted {
			fmt.Fprintf(out, "Deleted %s from %s.\n", stored, where)
		} else {
			fmt.Fprintf(out, "%s was not stored in %s.\n", stored, where)
		}
		if listed {
			fmt.Fprintf(out, "Removed %s from %s in builder.json.\n", name, scopeName(profile))
		}
		return nil
	},
}

// secretEntry is one row of secret list: a name builder.json lists, where its
// value is stored and whether the provider has it. Never a value.
type secretEntry struct {
	Name     string `json:"name"`
	Profile  string `json:"profile,omitempty"`
	StoredAs string `json:"stored_as"`
	Provider string `json:"provider"`
	Present  bool   `json:"present"`
}

var secretListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the secret names in builder.json and whether the provider holds them",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		profile, _ := cmd.Flags().GetString("profile")
		override, _ := cmd.Flags().GetString("provider")
		asJSON, _ := cmd.Flags().GetBool("json")
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		levels := []string{""}
		if profile != "" {
			if _, err := cfg.ResolveProfile(profile); err != nil {
				return err
			}
			levels = append(levels, profile)
		} else {
			levels = append(levels, cfg.ProfileNames()...)
		}
		entries := []secretEntry{}
		names := map[string][]string{} // provider -> names it holds, fetched once
		for _, level := range levels {
			provider, err := secretProvider(cfg, level, override)
			if err != nil {
				return err
			}
			for _, name := range cfg.SecretsOf(level) {
				if _, ok := names[provider]; !ok {
					store, _, err := secretStoreFor(cfg, provider)
					if err != nil {
						return err
					}
					if names[provider], err = store.List(cmd.Context()); err != nil {
						return err
					}
				}
				stored := config.SecretStorageName(name, level)
				entries = append(entries, secretEntry{Name: name, Profile: level, StoredAs: stored, Provider: provider, Present: slices.Contains(names[provider], stored)})
			}
		}
		out := cmd.OutOrStdout()
		if asJSON {
			enc := json.NewEncoder(out)
			enc.SetIndent("", "  ")
			return enc.Encode(entries)
		}
		if len(entries) == 0 {
			fmt.Fprintln(out, "No secrets in builder.json. Add one with: builder secret set NAME")
			return nil
		}
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tFOR\tSTORED AS\tPROVIDER\tSTATUS")
		for _, e := range entries {
			status := "set"
			if !e.Present {
				status = "missing (builds fail)"
				if e.Profile != "" && slices.Contains(names[e.Provider], e.Name) {
					status = "uses " + e.Name
				}
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", e.Name, scopeName(e.Profile), e.StoredAs, e.Provider, status)
		}
		return w.Flush()
	},
}

// workflowLacksSecrets reports a local ios-build.yml written before
// secrets were exported; false when there is none to look at.
func workflowLacksSecrets() bool {
	data, err := os.ReadFile(filepath.Join(".github", "workflows", "ios-build.yml"))
	return err == nil && !strings.Contains(string(data), "toJSON(secrets)")
}

func init() {
	for _, c := range []*cobra.Command{secretSetCmd, secretUnsetCmd, secretListCmd} {
		c.Flags().String("profile", "", "builder.json profile (default: the top level, which every build gets)")
		c.Flags().String("provider", "", "CI provider holding the secret: github, codemagic or bitrise (default: the profile's, else builder.json's, else github)")
	}
	secretSetCmd.Flags().Bool("value-stdin", false, "Read the value from stdin instead of a hidden prompt")
	secretListCmd.Flags().Bool("json", false, "Print JSON")
	secretCmd.AddCommand(secretSetCmd, secretUnsetCmd, secretListCmd)
	rootCmd.AddCommand(secretCmd)
}
