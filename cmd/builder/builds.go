package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/MobAI-App/ios-builder/internal/build"
	"github.com/MobAI-App/ios-builder/internal/builds"
	"github.com/MobAI-App/ios-builder/internal/config"
	"github.com/spf13/cobra"
)

// newBuildsSource opens the history of one provider. A package var so tests
// can point it at fake servers.
var newBuildsSource = func(cfg *config.Config, name string) (builds.Source, error) {
	if name == "github" {
		gh, err := getGitHubClient()
		if err != nil {
			return nil, err
		}
		return &builds.GitHub{Client: gh, Config: cfg, Out: os.Stderr}, nil
	}
	p, ciCfg, err := build.RemoteProvider(cfg, name)
	if err != nil {
		return nil, err
	}
	rp, ok := p.(builds.RemoteProvider)
	if !ok {
		return nil, fmt.Errorf("%s has no build history support", name)
	}
	return &builds.Remote{P: rp, CI: ciCfg, Project: cfg.Project}, nil
}

// buildsLogInterval is how often `builds logs --follow` polls.
var buildsLogInterval = 10 * time.Second

var buildsCmd = &cobra.Command{
	Use:   "builds",
	Short: "List Builder builds and inspect, download or cancel one",
	Long: `Lists the builds Builder started on the selected provider (GitHub Actions by
default, or Codemagic/Bitrise), newest first. The provider stays the source of
truth: nothing is stored locally.

<id> in the subcommands is the 8-character Builder build ID, a provider run ID,
or a run URL (whose host also picks the provider).`,
	Args: cobra.NoArgs,
	RunE: runBuildsList,
}

var buildsShowCmd = &cobra.Command{
	Use:   "show <id>",
	Short: "Show a build: status, jobs and steps, failure, artifacts",
	Args:  cobra.ExactArgs(1),
	RunE:  runBuildsShow,
}

var buildsLogsCmd = &cobra.Command{
	Use:   "logs <id>",
	Short: "Print a build's logs",
	Long: `Prints the logs of a build: GitHub job logs, Codemagic step logs, or the Bitrise
build log. GitHub and Codemagic publish a job's or step's log once it ends, so
--follow prints those as they end (and GitHub step names as they start), until
the build finishes.`,
	Args: cobra.ExactArgs(1),
	RunE: runBuildsLogs,
}

var buildsDownloadCmd = &cobra.Command{
	Use:   "download <id>",
	Short: "Download a build's IPA",
	Long:  "Downloads the IPA of a finished build, for example after `ios build` was interrupted. GitHub keeps artifacts for 7 days.",
	Args:  cobra.ExactArgs(1),
	RunE:  runBuildsDownload,
}

var buildsCancelCmd = &cobra.Command{
	Use:   "cancel <id>",
	Short: "Cancel a running build",
	Args:  cobra.ExactArgs(1),
	RunE:  runBuildsCancel,
}

func init() {
	rootCmd.AddCommand(buildsCmd)
	buildsCmd.PersistentFlags().String("provider", "", "Provider to read (default builder.json provider, else github; a run URL picks its own)")
	buildsCmd.Flags().Int("limit", 20, "Number of builds to list")
	buildsCmd.Flags().String("status", "", "Only builds that are running, failed or succeeded")
	buildsCmd.Flags().Bool("json", false, "Print JSON")
	buildsShowCmd.Flags().Bool("json", false, "Print JSON")
	buildsLogsCmd.Flags().Bool("failed", false, "Only the logs of failed jobs or steps")
	buildsLogsCmd.Flags().BoolP("follow", "f", false, "Keep printing until the build finishes")
	buildsDownloadCmd.Flags().StringP("output", "o", "dist", "Output directory for the IPA")
	buildsCmd.AddCommand(buildsShowCmd, buildsLogsCmd, buildsDownloadCmd, buildsCancelCmd)
}

// buildsSource picks the provider: --provider, else the one a run URL names,
// else builder.json's.
func buildsSource(cmd *cobra.Command, ref string) (builds.Source, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	flag, _ := cmd.Flags().GetString("provider")
	if flag == "" {
		flag = builds.ProviderFromURL(ref)
	}
	name, err := cfg.ProviderName(flag)
	if err != nil {
		return nil, err
	}
	return newBuildsSource(cfg, name)
}

// findBuild resolves <id> on the selected provider.
func findBuild(cmd *cobra.Command, ref string) (builds.Source, builds.Build, error) {
	src, err := buildsSource(cmd, ref)
	if err != nil {
		return nil, builds.Build{}, err
	}
	b, err := src.Find(cmd.Context(), ref)
	return src, b, err
}

func runBuildsList(cmd *cobra.Command, _ []string) error {
	limit, _ := cmd.Flags().GetInt("limit")
	status, _ := cmd.Flags().GetString("status")
	asJSON, _ := cmd.Flags().GetBool("json")
	if limit <= 0 {
		return fmt.Errorf("--limit must be positive")
	}
	if err := builds.ValidStatus(status); err != nil {
		return err
	}
	src, err := buildsSource(cmd, "")
	if err != nil {
		return err
	}
	list, err := src.List(cmd.Context(), builds.ListOptions{Limit: limit, Status: status})
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if asJSON {
		if list == nil {
			list = []builds.Build{}
		}
		return writeJSONTo(out, list)
	}
	if len(list) == 0 {
		fmt.Fprintf(out, "No builds on %s.\n", src.Provider())
		return nil
	}
	now := time.Now()
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "BUILD ID\tKIND\tPROFILE\tSTATUS\tSTARTED\tDURATION\tRUN")
	for i := range list {
		b := &list[i]
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", orDash(b.ID), b.Kind, orDash(b.Profile), statusText(b),
			started(b), duration(b, now), runText(b))
	}
	return tw.Flush()
}

func runBuildsShow(cmd *cobra.Command, args []string) error {
	src, b, err := findBuild(cmd, args[0])
	if err != nil {
		return err
	}
	d, err := src.Show(cmd.Context(), &b)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
		return writeJSONTo(out, &d)
	}
	printDetail(out, &d, time.Now())
	return nil
}

func printDetail(w io.Writer, d *builds.Detail, now time.Time) {
	title := "Build " + orDash(d.ID)
	extra := []string{d.Kind}
	if d.Profile != "" {
		extra = append(extra, "profile "+d.Profile)
	}
	fmt.Fprintf(w, "%s (%s)\n", title, strings.Join(extra, ", "))
	fmt.Fprintf(w, "  Provider: %s, run %s\n", d.Provider, d.RunID)
	if d.URL != "" {
		fmt.Fprintf(w, "  URL:      %s\n", d.URL)
	}
	fmt.Fprintf(w, "  Status:   %s\n", statusText(&d.Build))
	fmt.Fprintf(w, "  Started:  %s\n", started(&d.Build))
	fmt.Fprintf(w, "  Duration: %s\n", duration(&d.Build, now))
	for _, j := range d.Jobs {
		fmt.Fprintf(w, "\nJob %s: %s\n", j.Name, j.Status)
		for _, s := range j.Steps {
			fmt.Fprintf(w, "  %2d. %-40s %s\n", s.Number, s.Name, s.Status)
		}
	}
	if f := d.Failure; f != nil {
		fmt.Fprintln(w)
		switch {
		case f.Step != "":
			fmt.Fprintf(w, "Failed step: %s (job %s)\n", f.Step, f.Job)
		case f.Job != "":
			fmt.Fprintf(w, "Failed job: %s\n", f.Job)
		}
		for _, m := range f.Messages {
			fmt.Fprintf(w, "  %s\n", strings.ReplaceAll(m, "\n", "\n  "))
		}
	}
	fmt.Fprintln(w)
	if len(d.Artifacts) == 0 {
		fmt.Fprintln(w, "Artifacts: none")
		return
	}
	fmt.Fprintln(w, "Artifacts:")
	for _, a := range d.Artifacts {
		note := ""
		switch {
		case a.Expired:
			note = "  expired"
		case a.ExpiresAt != nil:
			note = "  expires " + a.ExpiresAt.Local().Format("2006-01-02 15:04")
		}
		fmt.Fprintf(w, "  %s  %.2f MB%s\n", a.Name, float64(a.Size)/(1024*1024), note)
	}
}

func runBuildsLogs(cmd *cobra.Command, args []string) error {
	src, b, err := findBuild(cmd, args[0])
	if err != nil {
		return err
	}
	failed, _ := cmd.Flags().GetBool("failed")
	follow, _ := cmd.Flags().GetBool("follow")
	out := cmd.OutOrStdout()
	stream := src.Logs(&b, failed)
	if follow {
		return builds.Follow(cmd.Context(), stream, out, buildsLogInterval)
	}
	done, err := stream.Read(cmd.Context(), out)
	if err != nil {
		return err
	}
	if !done {
		fmt.Fprintf(cmd.ErrOrStderr(), "Build %s is still %s; pass --follow to keep printing until it ends.\n", orDash(b.ID), b.Status)
	}
	return nil
}

func runBuildsDownload(cmd *cobra.Command, args []string) error {
	src, b, err := findBuild(cmd, args[0])
	if err != nil {
		return err
	}
	dir, _ := cmd.Flags().GetString("output")
	path, size, err := src.Download(cmd.Context(), &b, dir)
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "IPA: %s (%.2f MB)\n", path, float64(size)/(1024*1024))
	return nil
}

func runBuildsCancel(cmd *cobra.Command, args []string) error {
	return cancelBuild(cmd, args[0])
}

func cancelBuild(cmd *cobra.Command, ref string) error {
	src, err := buildsSource(cmd, ref)
	if err != nil {
		return err
	}
	var b builds.Build
	if src.Provider() != "github" && !builds.IsBuildID(ref) && !strings.Contains(ref, "://") {
		// A Codemagic or Bitrise run ID goes straight to the cancel API, which
		// confirms the run has stopped, as `ios cancel --run-id` always did.
		b = builds.Build{RunID: ref, Provider: src.Provider(), Status: builds.StatusRunning}
	} else if b, err = src.Find(cmd.Context(), ref); err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if b.Done() {
		fmt.Fprintf(out, "Build %s already ended: %s.\n", orDash(b.ID), statusText(&b))
		return nil
	}
	if err := src.Cancel(cmd.Context(), &b); err != nil {
		return fmt.Errorf("cancellation could not be confirmed; check %s: %w", orDash(b.URL), err)
	}
	if src.Provider() == "github" {
		fmt.Fprintf(out, "Cancellation requested for build %s (run %s).\n", orDash(b.ID), b.RunID)
		return nil
	}
	fmt.Fprintf(out, "Build %s (run %s) has stopped.\n", orDash(b.ID), b.RunID)
	return nil
}

func writeJSONTo(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// statusText is the normalized status, with the provider's word when it says
// more ("failed (timed_out)").
func statusText(b *builds.Build) string {
	if b.State == "" || strings.EqualFold(b.State, b.Status) ||
		(b.Status == builds.StatusSucceeded && b.State == "success") ||
		(b.Status == builds.StatusFailed && b.State == "failure") {
		return b.Status
	}
	return b.Status + " (" + b.State + ")"
}

func started(b *builds.Build) string {
	t := b.CreatedAt
	if b.StartedAt != nil {
		t = *b.StartedAt
	}
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04")
}

func duration(b *builds.Build, now time.Time) string {
	d := b.Duration(now)
	if d == 0 {
		return "-"
	}
	return d.Round(time.Second).String()
}

func runText(b *builds.Build) string {
	if b.URL != "" {
		return b.URL
	}
	return b.RunID
}
