package main

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"text/tabwriter"

	"github.com/MobAI-App/ios-builder/internal/config"
	"github.com/spf13/cobra"
)

var envCmd = &cobra.Command{
	Use:   "env",
	Short: "Manage the plain environment variables builds get",
	Long: `Plain (non-secret) environment variables live in builder.json: the top-level
"env" applies to every build, and profiles.<name>.env overrides it per key.
They are exported on the runner before dependencies install and the app builds.

Values are committed with builder.json, so never put a secret here; use
builder secret set for those.`,
}

var envSetCmd = &cobra.Command{
	Use:   "set NAME VALUE",
	Short: "Set a variable for every build, or for one profile with --profile",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		profile, _ := cmd.Flags().GetString("profile")
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		if err := cfg.SetEnv(profile, args[0], args[1]); err != nil {
			return err
		}
		if err := config.NewManager().Save(cfg); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Set %s for %s in builder.json.\n", args[0], scopeName(profile))
		return nil
	},
}

var envUnsetCmd = &cobra.Command{
	Use:   "unset NAME",
	Short: "Remove a variable from the top level, or from one profile with --profile",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		profile, _ := cmd.Flags().GetString("profile")
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		removed, err := cfg.UnsetEnv(profile, args[0])
		if err != nil {
			return err
		}
		if !removed {
			return fmt.Errorf("%s is not set for %s", args[0], scopeName(profile))
		}
		if err := config.NewManager().Save(cfg); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Removed %s from %s in builder.json.\n", args[0], scopeName(profile))
		return nil
	},
}

// envEntry is one row of env list. Profile is where the value is set; empty
// for the top level.
type envEntry struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	Profile string `json:"profile,omitempty"`
}

var envListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the variables: everything in builder.json, or what one profile's builds get",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		profile, _ := cmd.Flags().GetString("profile")
		asJSON, _ := cmd.Flags().GetBool("json")
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		entries, err := envEntries(cfg, profile)
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		if asJSON {
			enc := json.NewEncoder(out)
			enc.SetIndent("", "  ")
			return enc.Encode(entries)
		}
		if len(entries) == 0 {
			fmt.Fprintln(out, "No env variables in builder.json. Add one with: builder env set NAME VALUE")
			return nil
		}
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tVALUE\tSET FOR")
		for _, e := range entries {
			fmt.Fprintf(w, "%s\t%s\t%s\n", e.Name, e.Value, scopeName(e.Profile))
		}
		return w.Flush()
	},
}

// envEntries lists every level's variables without a profile, and with one
// the variables its builds get, each with the level that sets it.
func envEntries(cfg *config.Config, profile string) ([]envEntry, error) {
	entries := []envEntry{}
	add := func(level string, env map[string]string, skip map[string]string) {
		for _, k := range slices.Sorted(maps.Keys(env)) {
			if _, ok := skip[k]; !ok {
				entries = append(entries, envEntry{Name: k, Value: env[k], Profile: level})
			}
		}
	}
	if profile == "" {
		add("", cfg.Env, nil)
		for _, name := range cfg.ProfileNames() {
			add(name, cfg.Profiles[name].Env, nil)
		}
		return entries, nil
	}
	if _, err := cfg.ResolveProfile(profile); err != nil {
		return nil, err
	}
	own := cfg.Profiles[profile].Env
	add(profile, own, nil)
	add("", cfg.Env, own)
	slices.SortFunc(entries, func(a, b envEntry) int { return cmp.Compare(a.Name, b.Name) })
	return entries, nil
}

// scopeName describes a level of builder.json for messages.
func scopeName(profile string) string {
	if profile == "" {
		return "all builds"
	}
	return "profile " + profile
}

func init() {
	for _, c := range []*cobra.Command{envSetCmd, envUnsetCmd, envListCmd} {
		c.Flags().String("profile", "", "builder.json profile (default: the top level, which every build gets)")
	}
	envListCmd.Flags().Bool("json", false, "Print JSON")
	envCmd.AddCommand(envSetCmd, envUnsetCmd, envListCmd)
	rootCmd.AddCommand(envCmd)
}
