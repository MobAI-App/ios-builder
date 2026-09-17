package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/MobAI-App/ios-builder/internal/asc"
	"github.com/MobAI-App/ios-builder/internal/config"
	"github.com/MobAI-App/ios-builder/internal/distribute"
	"github.com/MobAI-App/ios-builder/internal/ipa"
	"github.com/spf13/cobra"
)

var ascCmd = &cobra.Command{
	Use:   "asc",
	Short: "App Store Connect: apps, builds, TestFlight groups and testers",
	Long: `Lists and manages what App Store Connect knows about the app, from any
platform. Every command is non-interactive and takes --json.

The app is identified by --bundle-id, else ios.bundleId in builder.json, else
the newest IPA in ./dist. Needs an App Store Connect API key: builder auth apple.`,
}

var ascAppsCmd = &cobra.Command{
	Use:   "apps",
	Short: "List the team's apps",
	Args:  cobra.NoArgs,
	RunE:  runASCApps,
}

var ascBuildsCmd = &cobra.Command{
	Use:   "builds",
	Short: "List the app's builds, newest first",
	Long: `Lists the builds of the newest marketing version with their processing
state and TestFlight groups; --all-versions lists every version.`,
	Args: cobra.NoArgs,
	RunE: runASCBuilds,
}

var ascBuildsExpireCmd = &cobra.Command{
	Use:   "expire",
	Short: "Expire a build so TestFlight stops offering it",
	Args:  cobra.NoArgs,
	RunE:  runASCBuildsExpire,
}

var ascGroupsCmd = &cobra.Command{
	Use:   "groups",
	Short: "List the app's TestFlight groups",
	Args:  cobra.NoArgs,
	RunE:  runASCGroups,
}

var ascGroupsCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a TestFlight group (internal unless --external)",
	Args:  cobra.ExactArgs(1),
	RunE:  runASCGroupsCreate,
}

var ascGroupsDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a TestFlight group (its testers stay on the team)",
	Args:  cobra.ExactArgs(1),
	RunE:  runASCGroupsDelete,
}

var ascGroupsAddBuildCmd = &cobra.Command{
	Use:   "add-build <name>",
	Short: "Add the newest VALID build (or --build-number) to a group",
	Long: `Adds a processed build to the group, creating the group when it does not
exist, exactly like builder ios submit --testflight --group. An external group
gets the build submitted for beta review first.`,
	Args: cobra.ExactArgs(1),
	RunE: runASCGroupsAddBuild,
}

var ascTestersCmd = &cobra.Command{
	Use:   "testers",
	Short: "List the app's TestFlight testers (or one group's with --group)",
	Args:  cobra.NoArgs,
	RunE:  runASCTesters,
}

var ascTestersAddCmd = &cobra.Command{
	Use:   "add <email>...",
	Short: "Invite testers to a TestFlight group",
	Long: `External groups take anyone: each tester is created in the group, which sends
the TestFlight invitation, or added to it when the team already has them.

Internal groups take App Store Connect team members only. A member is added
to the group; anyone else is invited to the team first (--role, default
CUSTOMER_SUPPORT, with only this app visible; --first and --last required).
They must accept that email before the build can reach them: rerun afterwards.`,
	Args: cobra.MinimumNArgs(1),
	RunE: runASCTestersAdd,
}

var ascUsersCmd = &cobra.Command{
	Use:   "users",
	Short: "List the App Store Connect team (email, roles, TestFlight access)",
	Args:  cobra.NoArgs,
	RunE:  runASCUsers,
}

var ascUsersInviteCmd = &cobra.Command{
	Use:   "invite <email>",
	Short: "Invite a person to the App Store Connect team",
	Long: `Sends a team invitation with the given role (default CUSTOMER_SUPPORT) and
only this app visible; --all-apps makes every app visible instead.`,
	Args: cobra.ExactArgs(1),
	RunE: runASCUsersInvite,
}

var ascTestersRemoveCmd = &cobra.Command{
	Use:   "remove <email>...",
	Short: "Remove testers from a group (--group) or from TestFlight entirely (--yes)",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runASCTestersRemove,
}

func init() {
	ascAppsCmd.Flags().Bool("json", false, "Print the result as JSON")
	ascBuildsCmd.Flags().Int("limit", 20, "Newest builds to list (0 for all)")
	ascBuildsCmd.Flags().Bool("all-versions", false, "List builds of every marketing version, not only the newest")
	ascBuildsExpireCmd.Flags().String("build-number", "", "Build number (CFBundleVersion) to expire")
	_ = ascBuildsExpireCmd.MarkFlagRequired("build-number")
	ascBuildsExpireCmd.Flags().Bool("yes", false, "Confirm; expiring cannot be undone")
	ascGroupsCreateCmd.Flags().Bool("external", false, "Create an external group (builds need beta review)")
	ascGroupsCreateCmd.Flags().Bool("public-link", false, "Enable the public invitation link (external groups only)")
	ascGroupsCreateCmd.Flags().Bool("no-auto-builds", false, "Internal group without automatic distribution: builds are added by hand")
	ascGroupsDeleteCmd.Flags().Bool("yes", false, "Delete even when the group has testers")
	ascGroupsAddBuildCmd.Flags().String("build-number", "", "Build number (CFBundleVersion) to add (default: newest VALID build)")
	ascGroupsAddBuildCmd.Flags().Bool("no-encryption", false, "Declare the app uses no non-exempt encryption (export compliance)")
	ascTestersCmd.Flags().String("group", "", "Only the testers of this group")
	ascTestersAddCmd.Flags().String("group", "", "TestFlight group to invite the testers to")
	_ = ascTestersAddCmd.MarkFlagRequired("group")
	ascTestersAddCmd.Flags().String("first", "", "First name")
	ascTestersAddCmd.Flags().String("last", "", "Last name")
	ascTestersAddCmd.Flags().String("role", asc.RoleCustomerSupport, "Team role for a person an internal group needs invited to the team")
	ascTestersRemoveCmd.Flags().String("group", "", "Remove from this group only")
	ascTestersRemoveCmd.Flags().Bool("yes", false, "Confirm removing the testers from TestFlight entirely (without --group)")
	ascUsersCmd.Flags().Bool("json", false, "Print the result as JSON")
	ascUsersInviteCmd.Flags().String("role", asc.RoleCustomerSupport, "Team role (ADMIN, APP_MANAGER, DEVELOPER, MARKETING, CUSTOMER_SUPPORT, ...)")
	ascUsersInviteCmd.Flags().String("first", "", "First name (required)")
	ascUsersInviteCmd.Flags().String("last", "", "Last name (required)")
	ascUsersInviteCmd.Flags().Bool("all-apps", false, "Make every app visible, not only this one")
	for _, cmd := range []*cobra.Command{ascBuildsCmd, ascBuildsExpireCmd, ascGroupsCmd, ascGroupsCreateCmd, ascGroupsDeleteCmd, ascGroupsAddBuildCmd, ascTestersCmd, ascTestersAddCmd, ascTestersRemoveCmd, ascUsersInviteCmd} {
		cmd.Flags().String("bundle-id", "", "App bundle ID (default: ios.bundleId in builder.json, else the newest IPA in ./dist)")
		cmd.Flags().Bool("json", false, "Print the result as JSON (progress goes to stderr)")
	}
	ascBuildsCmd.AddCommand(ascBuildsExpireCmd)
	ascGroupsCmd.AddCommand(ascGroupsCreateCmd, ascGroupsDeleteCmd, ascGroupsAddBuildCmd)
	ascTestersCmd.AddCommand(ascTestersAddCmd, ascTestersRemoveCmd)
	ascUsersCmd.AddCommand(ascUsersInviteCmd)
	ascCmd.AddCommand(ascAppsCmd, ascBuildsCmd, ascGroupsCmd, ascTestersCmd, ascUsersCmd)
}

// resolveBundleID picks the app the command works on: --bundle-id, else
// ios.bundleId in builder.json, else the newest IPA in ./dist.
func resolveBundleID(cmd *cobra.Command) (string, error) {
	if id, _ := cmd.Flags().GetString("bundle-id"); id != "" {
		return id, nil
	}
	cfg, err := config.NewManager().Load()
	if err != nil && !errors.Is(err, config.ErrConfigNotFound) {
		return "", err
	}
	if cfg != nil && cfg.IOS.BundleID != "" {
		return cfg.IOS.BundleID, nil
	}
	path, err := ipa.Newest("dist")
	if err != nil {
		return "", fmt.Errorf("cannot tell which app: pass --bundle-id, set ios.bundleId in builder.json, or build an IPA into ./dist")
	}
	info, err := ipa.ReadInfo(path)
	if err != nil {
		return "", err
	}
	return info.BundleID, nil
}

// ascSession is what every asc command working on one app starts with.
type ascSession struct {
	ctx    context.Context
	client *asc.Client
	app    *asc.App
	out    output
}

func openASC(cmd *cobra.Command) (*ascSession, context.CancelFunc, error) {
	client, err := getASCClient()
	if err != nil {
		return nil, nil, err
	}
	bundleID, err := resolveBundleID(cmd)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := commandContext(cmd, false)
	app, err := client.AppByBundleID(ctx, bundleID)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	return &ascSession{ctx: ctx, client: client, app: app, out: newOutput(cmd)}, cancel, nil
}

// group finds the app's TestFlight group by name, case-insensitively.
func (s *ascSession) group(name string) (*asc.BetaGroup, error) {
	groups, err := s.client.ListBetaGroups(s.ctx, s.app.ID)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(groups))
	for _, g := range groups {
		if strings.EqualFold(g.Name, name) {
			return &g, nil
		}
		names = append(names, g.Name)
	}
	has := "(none)"
	if len(names) > 0 {
		has = strings.Join(names, ", ")
	}
	return nil, fmt.Errorf("no TestFlight group named %s; %s has: %s", name, s.app.Name, has)
}

// printTable writes rows as aligned columns; the first row is the header.
func printTable(w io.Writer, rows [][]string) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, r := range rows {
		fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	_ = tw.Flush()
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return ""
}

type appRow struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	BundleID string `json:"bundle_id"`
	SKU      string `json:"sku"`
}

func runASCApps(cmd *cobra.Command, _ []string) error {
	client, err := getASCClient()
	if err != nil {
		return err
	}
	ctx, cancel := commandContext(cmd, false)
	defer cancel()
	apps, err := client.ListApps(ctx)
	if err != nil {
		return err
	}
	rows := make([]appRow, 0, len(apps))
	for _, a := range apps {
		rows = append(rows, appRow{ID: a.ID, Name: a.Name, BundleID: a.BundleID, SKU: a.SKU})
	}
	return finish(newOutput(cmd), cmd, &rows, nil, func() {
		table := [][]string{{"NAME", "BUNDLE ID", "ID", "SKU"}}
		for _, r := range rows {
			table = append(table, []string{r.Name, r.BundleID, r.ID, r.SKU})
		}
		printTable(cmd.OutOrStdout(), table)
	})
}

type buildRow struct {
	ID              string    `json:"id"`
	Version         string    `json:"version"`
	BuildNumber     string    `json:"build_number"`
	ProcessingState string    `json:"processing_state"`
	UploadedDate    time.Time `json:"uploaded_date"`
	Expired         bool      `json:"expired"`
	Groups          []string  `json:"groups"`
}

func toBuildRow(b *asc.Build) buildRow {
	groups := b.BetaGroups
	if groups == nil {
		groups = []string{}
	}
	return buildRow{ID: b.ID, Version: b.Version, BuildNumber: b.BuildNumber, ProcessingState: b.ProcessingState, UploadedDate: b.UploadedDate, Expired: b.Expired, Groups: groups}
}

func runASCBuilds(cmd *cobra.Command, _ []string) error {
	s, cancel, err := openASC(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	limit, _ := cmd.Flags().GetInt("limit")
	allVersions, _ := cmd.Flags().GetBool("all-versions")
	f := &asc.BuildFilter{AppID: s.app.ID, Platform: asc.PlatformIOS, Details: true, Limit: limit}
	if !allVersions {
		newest, err := s.client.ListBuilds(s.ctx, &asc.BuildFilter{AppID: s.app.ID, Platform: asc.PlatformIOS, Details: true, Limit: 1})
		if err != nil {
			return err
		}
		if len(newest) > 0 {
			f.Version = newest[0].Version
		}
	}
	builds, err := s.client.ListBuilds(s.ctx, f)
	if err != nil {
		return err
	}
	rows := make([]buildRow, 0, len(builds))
	for i := range builds {
		rows = append(rows, toBuildRow(&builds[i]))
	}
	return finish(s.out, cmd, &rows, nil, func() {
		if len(rows) == 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "%s has no builds; upload one with builder ios upload --wait\n", s.app.Name)
			return
		}
		table := [][]string{{"VERSION", "BUILD", "STATE", "UPLOADED", "EXPIRED", "GROUPS"}}
		for _, r := range rows {
			table = append(table, []string{r.Version, r.BuildNumber, r.ProcessingState, r.UploadedDate.Local().Format("2006-01-02 15:04"), yesNo(r.Expired), strings.Join(r.Groups, ", ")})
		}
		printTable(cmd.OutOrStdout(), table)
	})
}

func runASCBuildsExpire(cmd *cobra.Command, _ []string) error {
	number, _ := cmd.Flags().GetString("build-number")
	if yes, _ := cmd.Flags().GetBool("yes"); !yes {
		return fmt.Errorf("pass --yes to expire build %s; an expired build leaves TestFlight for good", number)
	}
	s, cancel, err := openASC(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	builds, err := s.client.ListBuilds(s.ctx, &asc.BuildFilter{AppID: s.app.ID, Platform: asc.PlatformIOS, BuildNumber: number, Limit: 1})
	if err != nil {
		return err
	}
	if len(builds) == 0 {
		return fmt.Errorf("%s has no build %s", s.app.Name, number)
	}
	build := builds[0]
	if build.Expired {
		logf(s.out.log, "Build %s (%s) is already expired", build.BuildNumber, build.ID)
	} else {
		updated, err := s.client.ExpireBuild(s.ctx, build.ID)
		if err != nil {
			return err
		}
		build = *updated
		logf(s.out.log, "Expired build %s (%s)", build.BuildNumber, build.ID)
	}
	row := toBuildRow(&build)
	return finish(s.out, cmd, &row, nil, nil)
}

type groupRow struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Internal bool   `json:"internal"`
	// AutoBuilds: an internal group with automatic distribution gets every build.
	AutoBuilds bool   `json:"auto_builds"`
	Testers    int    `json:"testers"`
	PublicLink string `json:"public_link,omitempty"`
}

func toGroupRow(g *asc.BetaGroup) groupRow {
	row := groupRow{ID: g.ID, Name: g.Name, Internal: g.Internal, AutoBuilds: g.Internal && g.HasAccessToAllBuilds}
	if g.PublicLinkEnabled {
		row.PublicLink = g.PublicLink
	}
	return row
}

func (s *ascSession) groupRow(g *asc.BetaGroup) (groupRow, error) {
	testers, err := s.client.ListBetaTesters(s.ctx, &asc.BetaTesterFilter{GroupID: g.ID})
	if err != nil {
		return groupRow{}, err
	}
	row := toGroupRow(g)
	row.Testers = len(testers)
	return row, nil
}

// groupKind names the group type: "internal, all builds" for automatic distribution.
func groupKind(g *asc.BetaGroup) string {
	switch {
	case !g.Internal:
		return "external"
	case g.HasAccessToAllBuilds:
		return "internal, all builds"
	}
	return "internal"
}

func runASCGroups(cmd *cobra.Command, _ []string) error {
	s, cancel, err := openASC(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	groups, err := s.client.ListBetaGroups(s.ctx, s.app.ID)
	if err != nil {
		return err
	}
	rows := make([]groupRow, 0, len(groups))
	kinds := make([]string, 0, len(groups))
	for i := range groups {
		row, err := s.groupRow(&groups[i])
		if err != nil {
			return err
		}
		rows = append(rows, row)
		kinds = append(kinds, groupKind(&groups[i]))
	}
	return finish(s.out, cmd, &rows, nil, func() {
		if len(rows) == 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "%s has no TestFlight groups; create one with builder asc groups create <name>\n", s.app.Name)
			return
		}
		table := [][]string{{"NAME", "TYPE", "TESTERS", "PUBLIC LINK"}}
		for i, r := range rows {
			table = append(table, []string{r.Name, kinds[i], fmt.Sprint(r.Testers), r.PublicLink})
		}
		printTable(cmd.OutOrStdout(), table)
	})
}

func runASCGroupsCreate(cmd *cobra.Command, args []string) error {
	name := args[0]
	external, _ := cmd.Flags().GetBool("external")
	publicLink, _ := cmd.Flags().GetBool("public-link")
	noAutoBuilds, _ := cmd.Flags().GetBool("no-auto-builds")
	if publicLink && !external {
		return fmt.Errorf("public links are only available on external groups; add --external")
	}
	if noAutoBuilds && external {
		return fmt.Errorf("--no-auto-builds only applies to internal groups; external groups always take builds by hand")
	}
	s, cancel, err := openASC(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	if g, err := s.group(name); err == nil {
		return fmt.Errorf("%s already has a TestFlight group named %s (%s)", s.app.Name, g.Name, groupKind(g))
	}
	g, err := s.client.CreateBetaGroup(s.ctx, asc.BetaGroupSpec{AppID: s.app.ID, Name: name, Internal: !external, PublicLinkEnabled: publicLink, HasAccessToAllBuilds: !external && !noAutoBuilds})
	if err != nil {
		return fmt.Errorf("create TestFlight group %s: %w", name, err)
	}
	logf(s.out.log, "Created TestFlight group %s (%s)", g.Name, groupKind(g))
	if g.PublicLinkEnabled && g.PublicLink != "" {
		logf(s.out.log, "Public link: %s", g.PublicLink)
	}
	row := toGroupRow(g)
	return finish(s.out, cmd, &row, nil, nil)
}

func runASCGroupsDelete(cmd *cobra.Command, args []string) error {
	s, cancel, err := openASC(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	g, err := s.group(args[0])
	if err != nil {
		return err
	}
	row, err := s.groupRow(g)
	if err != nil {
		return err
	}
	if yes, _ := cmd.Flags().GetBool("yes"); row.Testers > 0 && !yes {
		return fmt.Errorf("TestFlight group %s has %d testers; pass --yes to delete it anyway (the testers stay on the team)", g.Name, row.Testers)
	}
	if err := s.client.DeleteBetaGroup(s.ctx, g.ID); err != nil {
		return fmt.Errorf("delete TestFlight group %s: %w", g.Name, err)
	}
	logf(s.out.log, "Deleted TestFlight group %s (%s)", g.Name, groupKind(g))
	return finish(s.out, cmd, &row, nil, nil)
}

func runASCGroupsAddBuild(cmd *cobra.Command, args []string) error {
	client, err := getASCClient()
	if err != nil {
		return err
	}
	bundleID, err := resolveBundleID(cmd)
	if err != nil {
		return err
	}
	buildNumber, _ := cmd.Flags().GetString("build-number")
	noEncryption, _ := cmd.Flags().GetBool("no-encryption")
	ctx, cancel := commandContext(cmd, false)
	defer cancel()
	out := newOutput(cmd)
	res, err := distribute.SubmitTestFlight(ctx, client, &distribute.TestFlightOptions{
		BundleID: bundleID, BuildNumber: buildNumber, Groups: []string{args[0]}, NoEncryption: noEncryption, Log: out.log,
	})
	return finish(out, cmd, res, err, func() {
		fmt.Fprintf(cmd.OutOrStdout(), "Build ID: %s (build %s)\n", res.Build.ID, res.Build.BuildNumber)
		if res.BetaReview != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Beta review: %s\n", res.BetaReview.State)
		}
	})
}

type testerRow struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	State     string `json:"state"`
}

func runASCTesters(cmd *cobra.Command, _ []string) error {
	s, cancel, err := openASC(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	f := &asc.BetaTesterFilter{AppID: s.app.ID}
	if name, _ := cmd.Flags().GetString("group"); name != "" {
		g, err := s.group(name)
		if err != nil {
			return err
		}
		f = &asc.BetaTesterFilter{GroupID: g.ID}
	}
	testers, err := s.client.ListBetaTesters(s.ctx, f)
	if err != nil {
		return err
	}
	rows := make([]testerRow, 0, len(testers))
	for _, t := range testers {
		rows = append(rows, testerRow{ID: t.ID, Email: t.Email, FirstName: t.FirstName, LastName: t.LastName, State: t.State})
	}
	return finish(s.out, cmd, &rows, nil, func() {
		if len(rows) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "No testers; invite some with builder asc testers add <email> --group <name>")
			return
		}
		table := [][]string{{"EMAIL", "FIRST", "LAST", "STATE"}}
		for _, r := range rows {
			table = append(table, []string{r.Email, r.FirstName, r.LastName, r.State})
		}
		printTable(cmd.OutOrStdout(), table)
	})
}

func runASCTestersAdd(cmd *cobra.Command, emails []string) error {
	s, cancel, err := openASC(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	groupName, _ := cmd.Flags().GetString("group")
	first, _ := cmd.Flags().GetString("first")
	last, _ := cmd.Flags().GetString("last")
	role, _ := cmd.Flags().GetString("role")
	g, err := s.group(groupName)
	if err != nil {
		return err
	}
	rows := make([]distribute.TesterResult, 0, len(emails))
	for _, email := range emails {
		res, err := distribute.AddTester(s.ctx, s.client, &distribute.TesterOptions{
			AppID: s.app.ID, Group: *g, Email: email, FirstName: first, LastName: last, TeamRole: teamRole(role), Log: s.out.log,
		})
		if err != nil {
			return fmt.Errorf("add %s: %w", email, err)
		}
		rows = append(rows, *res)
	}
	return finish(s.out, cmd, &rows, nil, nil)
}

// teamRole normalizes a --role value to App Store Connect's spelling (APP_MANAGER).
func teamRole(role string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(role), "-", "_"))
}

type userRow struct {
	ID        string   `json:"id"`
	Email     string   `json:"email"`
	FirstName string   `json:"first_name"`
	LastName  string   `json:"last_name"`
	Roles     []string `json:"roles"`
	// TestFlight reports whether the member has a beta tester record, i.e.
	// can be put into internal groups.
	TestFlight bool `json:"testflight"`
}

func runASCUsers(cmd *cobra.Command, _ []string) error {
	client, err := getASCClient()
	if err != nil {
		return err
	}
	ctx, cancel := commandContext(cmd, false)
	defer cancel()
	users, err := client.ListUsers(ctx)
	if err != nil {
		return err
	}
	testers, err := client.ListBetaTesters(ctx, &asc.BetaTesterFilter{})
	if err != nil {
		return err
	}
	hasTester := make(map[string]bool, len(testers))
	for _, t := range testers {
		hasTester[strings.ToLower(t.Email)] = true
	}
	rows := make([]userRow, 0, len(users))
	for _, u := range users {
		roles := u.Roles
		if roles == nil {
			roles = []string{}
		}
		rows = append(rows, userRow{ID: u.ID, Email: u.Email, FirstName: u.FirstName, LastName: u.LastName, Roles: roles, TestFlight: hasTester[strings.ToLower(u.Email)]})
	}
	return finish(newOutput(cmd), cmd, &rows, nil, func() {
		table := [][]string{{"EMAIL", "NAME", "ROLES", "TESTFLIGHT"}}
		for _, r := range rows {
			table = append(table, []string{r.Email, strings.TrimSpace(r.FirstName + " " + r.LastName), strings.Join(r.Roles, ","), yesNo(r.TestFlight)})
		}
		printTable(cmd.OutOrStdout(), table)
	})
}

type invitationRow struct {
	ID      string    `json:"id"`
	Email   string    `json:"email"`
	Roles   []string  `json:"roles"`
	Expires time.Time `json:"expires"`
	// Pending is set when an unaccepted invitation already existed and no new one was sent.
	Pending bool `json:"pending,omitempty"`
}

func runASCUsersInvite(cmd *cobra.Command, args []string) error {
	email := args[0]
	first, _ := cmd.Flags().GetString("first")
	last, _ := cmd.Flags().GetString("last")
	role, _ := cmd.Flags().GetString("role")
	allApps, _ := cmd.Flags().GetBool("all-apps")
	if first == "" || last == "" {
		return fmt.Errorf("a team invitation needs a first and last name; pass --first and --last")
	}
	s, cancel, err := openASC(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	if u, err := s.client.FindUser(s.ctx, email); err != nil {
		return err
	} else if u != nil {
		return fmt.Errorf("%s is already on the team (%s)", u.Email, strings.Join(u.Roles, ","))
	}
	inv, err := s.client.FindUserInvitation(s.ctx, email)
	if err != nil {
		return err
	}
	pending := inv != nil
	if pending {
		logf(s.out.log, "%s already has a pending team invitation (expires %s)", inv.Email, inv.ExpirationDate.Local().Format("2006-01-02"))
	} else {
		inv, err = s.client.InviteUser(s.ctx, &asc.UserInvitationSpec{Email: email, FirstName: first, LastName: last, Roles: []string{teamRole(role)}, AllAppsVisible: allApps, VisibleAppIDs: []string{s.app.ID}})
		if err != nil {
			return fmt.Errorf("invite %s: %w", email, err)
		}
		logf(s.out.log, "Invited %s to the team as %s; they must accept the email to join", inv.Email, teamRole(role))
	}
	row := invitationRow{ID: inv.ID, Email: inv.Email, Roles: inv.Roles, Expires: inv.ExpirationDate, Pending: pending}
	if row.Roles == nil {
		row.Roles = []string{}
	}
	return finish(s.out, cmd, &row, nil, nil)
}

type testerRemoveRow struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	// Group is the group left; empty when the tester was removed from TestFlight.
	Group string `json:"group,omitempty"`
}

func runASCTestersRemove(cmd *cobra.Command, emails []string) error {
	groupName, _ := cmd.Flags().GetString("group")
	if yes, _ := cmd.Flags().GetBool("yes"); groupName == "" && !yes {
		return fmt.Errorf("pass --group <name> to remove the testers from one group, or --yes to remove them from TestFlight entirely")
	}
	s, cancel, err := openASC(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	rows := make([]testerRemoveRow, 0, len(emails))
	if groupName == "" {
		for _, email := range emails {
			t, err := s.client.FindBetaTester(s.ctx, &asc.BetaTesterFilter{AppID: s.app.ID, Email: email})
			if err != nil {
				return err
			}
			if t == nil {
				return fmt.Errorf("%s has no tester %s", s.app.Name, email)
			}
			if err := s.client.DeleteBetaTester(s.ctx, t.ID); err != nil {
				return fmt.Errorf("remove %s: %w", email, err)
			}
			logf(s.out.log, "Removed %s from TestFlight", t.Email)
			rows = append(rows, testerRemoveRow{ID: t.ID, Email: t.Email})
		}
		return finish(s.out, cmd, &rows, nil, nil)
	}
	g, err := s.group(groupName)
	if err != nil {
		return err
	}
	var ids []string
	for _, email := range emails {
		t, err := s.client.FindBetaTester(s.ctx, &asc.BetaTesterFilter{GroupID: g.ID, Email: email})
		if err != nil {
			return err
		}
		if t == nil {
			return fmt.Errorf("TestFlight group %s has no tester %s", g.Name, email)
		}
		ids = append(ids, t.ID)
		rows = append(rows, testerRemoveRow{ID: t.ID, Email: t.Email, Group: g.Name})
	}
	if err := s.client.RemoveBetaTestersFromGroup(s.ctx, g.ID, ids); err != nil {
		return fmt.Errorf("remove from %s: %w", g.Name, err)
	}
	for _, r := range rows {
		logf(s.out.log, "Removed %s from %s", r.Email, g.Name)
	}
	return finish(s.out, cmd, &rows, nil, nil)
}

// logf writes a progress line when w is set.
func logf(w io.Writer, format string, args ...any) {
	if w != nil {
		fmt.Fprintf(w, format+"\n", args...)
	}
}
