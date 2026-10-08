package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/MobAI-App/ios-builder/internal/auth"
	"github.com/MobAI-App/ios-builder/internal/build"
	"github.com/MobAI-App/ios-builder/internal/config"
	"github.com/MobAI-App/ios-builder/internal/exitcode"
	"github.com/MobAI-App/ios-builder/internal/github"
	"github.com/MobAI-App/ios-builder/internal/otainstall"
	"github.com/MobAI-App/ios-builder/internal/release"
	"github.com/MobAI-App/ios-builder/internal/signing"
	"github.com/MobAI-App/ios-builder/internal/update"
	"github.com/MobAI-App/ios-builder/internal/workflow"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var verbose bool

var rootCmd = &cobra.Command{
	Use:   "builder",
	Short: "Build iOS apps remotely using macOS CI providers",
	Long: `Builder builds iOS apps using GitHub Actions (default), Codemagic, or Bitrise.
Perfect for developers on Windows/Linux who need to build iOS IPAs.`,
	SilenceUsage: true,
	Version:      version,
}

func initConfig() {
	viper.SetConfigName("builder")
	viper.SetConfigType("json")
	viper.AddConfigPath(".")
	viper.AutomaticEnv()
	// Ignore error: config file is optional
	_ = viper.ReadInConfig()
}

func getGitHubClient() (*github.Client, error) {
	token, err := auth.GetToken()
	if err != nil {
		return nil, exitcode.With(exitcode.Auth, fmt.Errorf("not authenticated. Run: builder auth github (or pipe a token: builder auth github --token-stdin)"))
	}
	return github.NewClient(token), nil
}

func loadConfig() (*config.Config, error) {
	mgr := config.NewManager()
	cfg, err := mgr.Load()
	if err != nil {
		if err == config.ErrConfigNotFound {
			return nil, fmt.Errorf("builder.json not found. Run: builder init")
		}
		return nil, err
	}
	return cfg, nil
}

// stdinReader is shared so bytes buffered by one prompt are not lost by the next.
var stdinReader = bufio.NewReader(os.Stdin)

// promptString reads a line of free text from stdin.
//
// It deliberately avoids promptui here: promptui redraws the entire prompt on
// every keystroke and its screen buffer assumes the rendered prompt occupies a
// single terminal line. Pasting a value long enough to wrap breaks that
// assumption, so each redraw strands a copy of the prompt on screen and the
// visible text is garbled. Reading the line in the terminal's normal cooked
// mode lets the terminal handle echo, wrapping and paste on its own.
func promptString(label, defaultVal string) (string, error) {
	if defaultVal != "" {
		fmt.Printf("%s [%s]: ", label, defaultVal)
	} else {
		fmt.Printf("%s: ", label)
	}

	line, err := stdinReader.ReadString('\n')
	if err != nil && (err != io.EOF || line == "") {
		return "", err
	}

	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return defaultVal, nil
	}
	return line, nil
}

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize iOS builds for this repository",
	Long: `Sets up GitHub Actions workflow for iOS builds in the current repository.

This command:
- Detects your GitHub repository from git remote
- Adds the iOS build workflow to .github/workflows/
- Creates builder.json configuration`,
	RunE: runInit,
}

func isFlutterProject() bool {
	_, err := os.Stat("pubspec.yaml")
	return err == nil
}

// isExpoProject reports whether package.json declares a dependency on Expo.
// It reads the dependency maps rather than searching the raw text, so a
// package named "expo", a script that shells out to it, or a keyword does not
// make an unrelated Node project look like an Expo app. An unparseable
// package.json falls back to the substring test the runners use, so the CLI
// and the runners still agree on such a file.
func isExpoProject() bool {
	data, err := os.ReadFile("package.json")
	if err != nil {
		return false
	}
	var pkg struct {
		Dependencies    map[string]json.RawMessage `json:"dependencies"`
		DevDependencies map[string]json.RawMessage `json:"devDependencies"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return strings.Contains(string(data), `"expo"`)
	}
	_, dep := pkg.Dependencies["expo"]
	_, devDep := pkg.DevDependencies["expo"]
	return dep || devDep
}

// expoManagedFramework names a managed Expo project: one that depends on Expo
// but keeps no Xcode project in git, because `expo prebuild` generates it. The
// runner runs that prebuild, so the iOS path is still "ios" — that is where
// prebuild puts the project.
const expoManagedFramework = "Expo (managed)"

// kmpPluginRe matches a declaration of the Kotlin Multiplatform Gradle plugin,
// in the Kotlin DSL (`kotlin("multiplatform")`) or Groovy/plugin-id form. It
// must stay in step with the detection in the workflow template: a project the
// CLI calls KMP but the runner does not gets no JDK, and vice versa.
var kmpPluginRe = regexp.MustCompile(`kotlin\("multiplatform"\)|org\.jetbrains\.kotlin\.multiplatform|id\(["']org\.jetbrains\.kotlin\.multiplatform["']\)`)

// isKMPProject reports whether the current directory looks like a Kotlin
// Multiplatform project. The multiplatform plugin usually lives in a module's
// build file (e.g. shared/build.gradle.kts) rather than the root, so we scan
// root and immediate subdirectory Gradle files.
//
// Projects using a version catalog declare the plugin id once in
// gradle/libs.versions.toml and reference it as `alias(libs.plugins.…)` in the
// build files, so the catalog is scanned too — that is how most current KMP
// projects (KaMPKit, PeopleInSpace) are set up.
func isKMPProject() bool {
	hasMultiplatform := func(path string) bool {
		data, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		return kmpPluginRe.Match(data)
	}

	roots := []string{
		"settings.gradle.kts", "settings.gradle",
		"build.gradle.kts", "build.gradle",
		filepath.Join("gradle", "libs.versions.toml"),
	}
	if slices.ContainsFunc(roots, hasMultiplatform) {
		return true
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		for _, name := range []string{"build.gradle.kts", "build.gradle"} {
			if hasMultiplatform(filepath.Join(e.Name(), name)) {
				return true
			}
		}
	}
	return false
}

func getLocalFlutterVersion() string {
	cmd := exec.Command("flutter", "--version", "--machine")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	// Parse JSON output: {"frameworkVersion":"3.24.0",...}
	var result struct {
		FrameworkVersion string `json:"frameworkVersion"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return ""
	}
	return result.FrameworkVersion
}

func detectIOSPath() (string, string) {
	patterns := []struct {
		path      string
		framework string
	}{
		{"ios", "React Native/Expo"},
		{"iosApp", "Kotlin Multiplatform"},
		{"platforms/ios", "Cordova/Ionic"},
	}

	for _, p := range patterns {
		if entries, err := os.ReadDir(p.path); err == nil {
			for _, e := range entries {
				if strings.HasSuffix(e.Name(), ".xcworkspace") || strings.HasSuffix(e.Name(), ".xcodeproj") {
					return p.path, p.framework
				}
			}
		}
	}

	if entries, err := os.ReadDir("."); err == nil {
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".xcworkspace") || strings.HasSuffix(e.Name(), ".xcodeproj") {
				return "", "Native iOS"
			}
		}
	}

	// No Xcode project anywhere, but the app depends on Expo: a managed
	// project, whose ios/ directory the runner generates with `expo prebuild`.
	// Flutter is checked first for the same reason the runners check
	// pubspec.yaml before package.json: a Flutter repo that does not commit
	// ios/ is not a managed Expo project, and the runners would never prebuild
	// it.
	if !isFlutterProject() && isExpoProject() {
		return "ios", expoManagedFramework
	}

	return "", ""
}

// bundleIDRe matches PRODUCT_BUNDLE_IDENTIFIER assignments in a project.pbxproj.
var bundleIDRe = regexp.MustCompile(`PRODUCT_BUNDLE_IDENTIFIER\s*=\s*"?([^";\s]+)"?\s*;`)

// detectBundleID reads the app's bundle identifier from the Xcode project
// under iosPath, skipping test targets and $(…) values. Anything still
// ambiguous yields "" so init leaves the field for `signing setup` to resolve.
func detectBundleID(iosPath string) string {
	if iosPath == "" {
		iosPath = "."
	}
	projects, _ := filepath.Glob(filepath.Join(iosPath, "*.xcodeproj", "project.pbxproj"))
	var found []string
	for _, path := range projects {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		found = append(found, bundleIDsFromPbxproj(string(data))...)
	}
	if len(found) == 1 {
		return found[0]
	}
	return ""
}

// bundleIDsFromPbxproj returns the distinct app bundle identifiers in pbxproj text.
func bundleIDsFromPbxproj(text string) []string {
	var ids []string
	for _, m := range bundleIDRe.FindAllStringSubmatch(text, -1) {
		id := m[1]
		if strings.Contains(id, "$") || strings.HasSuffix(id, "Tests") || slices.Contains(ids, id) {
			continue
		}
		ids = append(ids, id)
	}
	return ids
}

func detectGitHubRepo(remoteName string) (owner, repo string, err error) {
	// Try to get GitHub remote URL from git
	cmd := exec.Command("git", "remote", "get-url", remoteName)
	output, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("not a git repository or no '%s' remote", remoteName)
	}

	remoteURL := strings.TrimSpace(string(output))

	// Parse GitHub URL formats:
	// https://github.com/owner/repo.git
	// git@github.com:owner/repo.git
	// git@github-alias:owner/repo.git (SSH config aliases)
	// https://github.com/owner/repo

	remoteURL = strings.TrimSuffix(remoteURL, ".git")

	if path, found := strings.CutPrefix(remoteURL, "https://github.com/"); found {
		parts := strings.Split(path, "/")
		if len(parts) >= 2 {
			return parts[0], parts[1], nil
		}
	}

	if strings.HasPrefix(remoteURL, "git@") {
		if colonIdx := strings.Index(remoteURL, ":"); colonIdx > 0 {
			path := remoteURL[colonIdx+1:]
			parts := strings.Split(path, "/")
			if len(parts) >= 2 {
				return parts[0], parts[1], nil
			}
		}
	}

	return "", "", fmt.Errorf("could not parse GitHub URL from: %s", remoteURL)
}

func runInit(cmd *cobra.Command, args []string) error {
	provider, _ := cmd.Flags().GetString("provider")
	if provider != "" && provider != "github" {
		return runProviderInit(cmd)
	}
	out := newOutput(cmd)
	w := out.log
	ask := newAsker(cmd)
	res := &initResult{Files: []string{}}
	fmt.Fprintln(w, "Builder - iOS Build Setup")
	fmt.Fprintln(w)

	// Get remote name from flag
	remoteName, _ := cmd.Flags().GetString("remote")

	// Detect GitHub repo from git remote
	githubOwner, repoName, err := detectGitHubRepo(remoteName)
	if err != nil {
		return fmt.Errorf("failed to detect GitHub repository: %w\nMake sure you're in a git repository with a GitHub remote", err)
	}

	fmt.Fprintf(w, "Detected repository: %s/%s (from remote '%s')\n", githubOwner, repoName, remoteName)
	fmt.Fprintln(w)

	// Get project name
	projectName, _ := cmd.Flags().GetString("project")
	if projectName == "" {
		cwd, _ := os.Getwd()
		defaultProject := filepath.Base(cwd)
		projectName, err = ask.text("Project name", defaultProject, "--project")
		if err != nil {
			return err
		}
	}

	// Detect iOS path
	iosPath, _ := cmd.Flags().GetString("ios-path")
	scheme, _ := cmd.Flags().GetString("scheme")

	if !cmd.Flags().Changed("ios-path") {
		detectedPath, framework := detectIOSPath()
		res.Framework = framework
		if detectedPath != "" {
			fmt.Fprintf(w, "Detected %s project (iOS at '%s')\n", framework, detectedPath)
			if framework == expoManagedFramework {
				fmt.Fprintf(w, "There is no '%s' directory yet; the build generates it on the runner with 'expo prebuild'.\n", detectedPath)
				fmt.Fprintln(w, "app.json / app.config.js must set ios.bundleIdentifier, or prebuild cannot run unattended.")
			}
			use, err := ask.confirm("Use this path", true, "--ios-path")
			if err != nil {
				return err
			}
			if use {
				iosPath = detectedPath
			}
		} else if framework != "" {
			fmt.Fprintf(w, "Detected %s project\n", framework)
		}

		if iosPath == "" && framework == "" {
			fmt.Fprintln(w, "No iOS project detected in current directory.")
			fmt.Fprintln(w, "If this is a hybrid app (React Native, Flutter, etc.),")
			if iosPath, err = ask.text("Path to iOS folder (leave empty for root)", "", "--ios-path"); err != nil {
				return err
			}
		}
	}

	// Detect Flutter and prompt for version
	flutterVersion, _ := cmd.Flags().GetString("flutter-version")
	if isFlutterProject() {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Detected Flutter project")
		if !cmd.Flags().Changed("flutter-version") {
			localVersion := getLocalFlutterVersion()
			if localVersion != "" {
				fmt.Fprintf(w, "Local Flutter version: %s\n", localVersion)
			}
			flutterVersion, err = ask.text("Flutter version for builds (leave empty for latest)", localVersion, "--flutter-version")
			if err != nil {
				return err
			}
		}
	}

	// Detect Kotlin Multiplatform and prompt for JDK version
	jdkVersion, _ := cmd.Flags().GetString("jdk-version")
	if isKMPProject() {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Detected Kotlin Multiplatform project")
		fmt.Fprintln(w, "Note: KMP has no hot reload on iOS - rebuild for code changes.")
		if !cmd.Flags().Changed("jdk-version") {
			jdkVersion, err = ask.text("JDK version for Gradle builds", "17", "--jdk-version")
			if err != nil {
				return err
			}
		}
	}

	// The commit and build questions come last in a terminal, but without
	// one they are settled here, before any file is written.
	commit, _ := cmd.Flags().GetBool("commit")
	runFirstBuild, _ := cmd.Flags().GetBool("build")
	if !ask.interactive && !ask.yes {
		if !cmd.Flags().Changed("commit") {
			return needInput(`an answer to "Commit and push workflow"`, "--commit", "--commit=false", "--yes (no)")
		}
		if !cmd.Flags().Changed("build") {
			return needInput(`an answer to "Run build now"`, "--build", "--build=false", "--yes (no)")
		}
	}

	fmt.Fprintln(w)
	fmt.Fprintf(w, "Project:    %s\n", projectName)
	fmt.Fprintf(w, "Repository: %s/%s\n", githubOwner, repoName)
	if iosPath != "" {
		fmt.Fprintf(w, "iOS Path:   %s\n", iosPath)
	}
	if flutterVersion != "" {
		fmt.Fprintf(w, "Flutter:    %s\n", flutterVersion)
	}
	if jdkVersion != "" {
		fmt.Fprintf(w, "JDK:        %s\n", jdkVersion)
	}
	fmt.Fprintln(w)

	// Create workflow file locally
	fmt.Fprintln(w, "Creating workflow file...")
	workflowDir := ".github/workflows"
	if err := os.MkdirAll(workflowDir, 0755); err != nil {
		return fmt.Errorf("failed to create workflow directory: %w", err)
	}

	workflowContent, err := workflow.GetWorkflowTemplate()
	if err != nil {
		return fmt.Errorf("failed to get workflow template: %w", err)
	}

	workflowPath := filepath.Join(workflowDir, "ios-build.yml")
	if err := os.WriteFile(workflowPath, workflowContent, 0644); err != nil {
		return fmt.Errorf("failed to write workflow file: %w", err)
	}
	fmt.Fprintf(w, "  Created: %s\n", workflowPath)
	res.Files = append(res.Files, workflowPath)

	// Ship the share workflow next to the build one so `builder ios share` needs
	// no extra setup. Dispatch-only, so it costs nothing until used.
	shareContent, err := workflow.GetShareWorkflowTemplate()
	if err != nil {
		return fmt.Errorf("failed to get simulator workflow template: %w", err)
	}
	sharePath := filepath.Join(workflowDir, "ios-share.yml")
	if err := os.WriteFile(sharePath, shareContent, 0644); err != nil {
		return fmt.Errorf("failed to write simulator workflow file: %w", err)
	}
	fmt.Fprintf(w, "  Created: %s\n", sharePath)
	res.Files = append(res.Files, sharePath)

	// Save config
	cfg, err := config.NewManager().Load()
	if err != nil && err != config.ErrConfigNotFound {
		return err
	}
	if cfg == nil {
		cfg = &config.Config{Provider: "github"}
	}
	cfg.Project, cfg.Platform = projectName, "ios"
	cfg.GitHub = config.GitHubConfig{Owner: githubOwner, Repo: repoName}
	cfg.IOS.Path, cfg.IOS.Scheme = iosPath, scheme
	if cfg.IOS.BundleID == "" {
		cfg.IOS.BundleID = detectBundleID(iosPath)
	}
	signing.SyncExtensions(cfg, w)
	if flutterVersion != "" {
		cfg.Flutter.Version = flutterVersion
	}
	cfg.ReactNative.Expo = isExpoProject()
	if jdkVersion != "" {
		cfg.KMP.JDKVersion = jdkVersion
	}
	setDefault, _ := cmd.Flags().GetBool("set-default")
	if setDefault {
		cfg.Provider = "github"
	}

	fmt.Fprintln(w, "Creating builder.json...")
	mgr := config.NewManager()
	if err := mgr.Save(cfg); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	fmt.Fprintln(w, "  Created: builder.json")
	res.Files = append(res.Files, "builder.json")
	res.Project, res.Repository, res.IOSPath = projectName, githubOwner+"/"+repoName, iosPath
	res.BundleID, res.FlutterVersion, res.JDKVersion = cfg.IOS.BundleID, flutterVersion, jdkVersion

	fmt.Fprintln(w)
	fmt.Fprintln(w, "Setup complete!")
	fmt.Fprintln(w)

	// Ask to commit and push
	if !cmd.Flags().Changed("commit") {
		if commit, err = ask.confirm("Commit and push workflow", false, "--commit"); err != nil {
			return err
		}
	}

	if commit {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Committing and pushing...")
		res.Committed, res.Pushed = commitAndPushInit(w)
		fmt.Fprintln(w)
	}

	// Ask to run build
	if !cmd.Flags().Changed("build") {
		if runFirstBuild, err = ask.confirm("Run build now", false, "--build"); err != nil {
			return err
		}
	}

	if runFirstBuild {
		fmt.Fprintln(w)
		ctx := cmd.Context()
		if ctx == nil {
			ctx = context.Background()
		}
		result, err := runBuild(ctx, cfg, &build.BuildOptions{
			OutputDir: "dist",
			Timeout:   build.DefaultTimeout,
			Remote:    remoteName,
		}, w)
		if result != nil {
			res.Build = newBuildJSON(result, "github", "")
			if !out.json {
				fmt.Fprintf(w, "IPA: %s\n", result.IPAPath)
				fmt.Fprintf(w, "Workflow: %s\n", result.WorkflowURL)
			}
		}
		if out.json {
			_ = printJSON(cmd, res)
		}
		return err
	}

	if out.json {
		return printJSON(cmd, res)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "To build later, run:")
	fmt.Fprintln(w, "  builder ios build")
	fmt.Fprintln(w)

	return nil
}

// initResult is the JSON of `init --json`.
type initResult struct {
	Project        string     `json:"project"`
	Repository     string     `json:"repository"`
	IOSPath        string     `json:"ios_path"`
	Framework      string     `json:"framework,omitempty"`
	BundleID       string     `json:"bundle_id,omitempty"`
	FlutterVersion string     `json:"flutter_version,omitempty"`
	JDKVersion     string     `json:"jdk_version,omitempty"`
	Files          []string   `json:"files"`
	Committed      bool       `json:"committed"`
	Pushed         bool       `json:"pushed"`
	Build          *buildJSON `json:"build,omitempty"`
}

// commitAndPushInit commits the files init wrote and pushes them. Failures
// are warnings: the files are in place either way.
func commitAndPushInit(w io.Writer) (committed, pushed bool) {
	addCmd := exec.Command("git", "add", ".github/workflows/ios-build.yml", ".github/workflows/ios-share.yml", "builder.json")
	if output, err := addCmd.CombinedOutput(); err != nil {
		fmt.Fprintf(w, "  Warning: git add failed: %s\n", strings.TrimSpace(string(output)))
	} else {
		fmt.Fprintln(w, "  Added files to staging")
	}

	commitCmd := exec.Command("git", "commit", "-m", "Add iOS build workflow")
	if output, err := commitCmd.CombinedOutput(); err != nil {
		outputStr := strings.TrimSpace(string(output))
		if strings.Contains(outputStr, "nothing to commit") {
			fmt.Fprintln(w, "  Nothing to commit (already committed)")
			committed = true
		} else {
			fmt.Fprintf(w, "  Warning: git commit failed: %s\n", outputStr)
		}
	} else {
		fmt.Fprintln(w, "  Committed changes")
		committed = true
	}

	pushCmd := exec.Command("git", "push")
	if output, err := pushCmd.CombinedOutput(); err != nil {
		fmt.Fprintf(w, "  Warning: git push failed: %s\n", strings.TrimSpace(string(output)))
	} else {
		fmt.Fprintln(w, "  Pushed to remote")
		pushed = true
	}
	return committed, pushed
}

var iosCmd = &cobra.Command{
	Use:   "ios",
	Short: "iOS build commands",
}

var iosBuildCmd = &cobra.Command{
	Use:   "build",
	Short: "Trigger a remote iOS build",
	Long:  `Triggers an iOS build on the selected provider (GitHub Actions by default) and downloads the IPA artifact.`,
	RunE:  runIOSBuild,
}

var iosShareCmd = &cobra.Command{
	Use:   "share",
	Short: "Try this build on a simulator, from the MobAI app",
	Long: `Builds the working tree for the iOS simulator on the selected provider and makes that
simulator usable from the MobAI app, so a build can be tried by hand without a
Mac.

The simulator appears in MobAI under CI Devices. It stays available while it is
being used and closes once it is released there, or left unused for a while.

Free with any MobAI account; needs a MOBAI_API_KEY secret on the selected provider.
GitHub Actions is the default. Codemagic/Bitrise return a submitted session and
workflow URL; the simulator appears in MobAI once its build and bridge start.`,
	RunE: runIOSShare,
}

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update builder to the latest release",
	Long:  "Checks GitHub for a newer release and, if there is one, replaces this binary with it. Works on macOS, Windows, and Linux.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return update.Run(cmd.Context(), version)
	},
}

func init() {
	// Root command setup
	cobra.OnInitialize(initConfig)
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Enable verbose output")
	rootCmd.PersistentFlags().BoolVar(&noInput, "no-input", false, "Never prompt: take --yes defaults or fail naming the flag to pass (also BUILDER_NO_INPUT=1 or CI=true)")
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(updateCmd)
	rootCmd.AddCommand(iosCmd)
	rootCmd.AddCommand(ascCmd)
	rootCmd.AddCommand(authCmd)
	rootCmd.AddCommand(signingCmd)
	rootCmd.AddCommand(devCmd)
	rootCmd.AddCommand(mobaiCmd)

	// Init command flags
	initCmd.Flags().StringP("project", "p", "", "Project name (defaults to directory name)")
	initCmd.Flags().String("ios-path", "", "Path to iOS project (e.g., 'ios' for React Native)")
	initCmd.Flags().String("scheme", "", "Xcode scheme to build (auto-detected if empty)")
	initCmd.Flags().StringP("remote", "r", "origin", "Git remote name to use for GitHub repository")
	initCmd.Flags().String("provider", "", "CI provider to configure (default github)")
	initCmd.Flags().String("app-id", "", "Codemagic app ID or Bitrise app slug")
	initCmd.Flags().String("branch", "", "Committed branch containing the provider workflow")
	initCmd.Flags().Bool("set-default", false, "Make this provider the project default")
	initCmd.Flags().String("flutter-version", "", "Flutter version for builds (default: the local one; empty for latest)")
	initCmd.Flags().String("jdk-version", "", "JDK version for Kotlin Multiplatform Gradle builds (default 17)")
	initCmd.Flags().Bool("commit", false, "Commit and push the workflow and builder.json without asking (--commit=false: don't)")
	initCmd.Flags().Bool("build", false, "Run the first build without asking (--build=false: don't)")
	initCmd.Flags().BoolP("yes", "y", false, "Accept every detected value and default without asking; does not commit or build unless --commit/--build")
	initCmd.Flags().Bool("json", false, "Print the result as JSON (progress goes to stderr); implies --no-input")

	// iOS build command flags
	iosBuildCmd.Flags().StringP("output", "o", "dist", "Output directory for IPA")
	iosBuildCmd.Flags().Duration("timeout", build.DefaultTimeout, "Build timeout")
	iosBuildCmd.Flags().Bool("unsigned", false, "Build unsigned IPA (skip code signing even if configured)")
	iosBuildCmd.Flags().StringP("remote", "r", "origin", "Git remote to push the working-tree snapshot to")
	iosBuildCmd.Flags().String("provider", "", "Override CI provider (default github or builder.json provider)")
	iosBuildCmd.Flags().String("profile", "", "Build profile from builder.json (default: defaultProfile, else the top-level ios settings)")
	iosBuildCmd.Flags().Bool("submit", false, "Also upload to App Store Connect and process for TestFlight (short for: ios release)")
	iosBuildCmd.Flags().Bool("json", false, "Print the result as JSON (progress goes to stderr); with --submit or --distribute their JSON follows")
	iosCmd.AddCommand(iosBuildCmd)

	// iOS share command flags
	iosShareCmd.Flags().Duration("duration", 30*time.Minute, "How long the simulator stays available while unused")
	iosShareCmd.Flags().StringP("remote", "r", "origin", "Git remote to push the working-tree snapshot to")
	iosShareCmd.Flags().String("provider", "", "Override CI provider (default github or builder.json provider)")
	iosShareCmd.Flags().Bool("json", false, "Print the result as JSON (progress goes to stderr)")
	iosCmd.AddCommand(iosShareCmd)
}

// effectiveProvider is the --provider flag, else the selected profile's
// provider, else builder.json's. The coordinator resolves the same chain; this
// exists so the GitHub client and signal handling agree with it.
func effectiveProvider(cfg *config.Config, profile, flag string) (string, error) {
	if flag != "" {
		return flag, nil
	}
	s, err := cfg.ResolveProfile(profile)
	if err != nil {
		return "", err
	}
	return s.Provider, nil
}

func runIOSBuild(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	opts, err := buildOptionsFromFlags(cmd, cfg)
	if err != nil {
		return err
	}
	opts.Unsigned, _ = cmd.Flags().GetBool("unsigned")
	submit, _ := cmd.Flags().GetBool("submit")
	distribute, _ := cmd.Flags().GetBool("distribute")
	if submit && distribute {
		return fmt.Errorf("pass only one of --submit (TestFlight) or --distribute (over-the-air install)")
	}
	if submit {
		if opts.Unsigned {
			return fmt.Errorf("--submit uploads to App Store Connect, which needs a signed build; drop --unsigned")
		}
		return runRelease(cmd, cfg, &release.Options{Build: opts})
	}
	if distribute {
		if opts.Unsigned {
			return fmt.Errorf("--distribute installs on a device, which needs a signed build; drop --unsigned")
		}
		s, err := cfg.ResolveProfile(opts.Profile)
		if err != nil {
			return err
		}
		if err := otainstall.CheckDistribution(&s); err != nil {
			return err
		}
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	name, err := cfg.ProviderName(opts.Provider)
	if err != nil {
		return err
	}
	if name != "github" {
		var stop context.CancelFunc
		ctx, stop = signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()
	}
	out := newOutput(cmd)
	result, err := runBuild(ctx, cfg, &opts, out.log)
	if err != nil {
		return err
	}
	if out.json {
		if err := printJSON(cmd, newBuildJSON(result, name, opts.Profile)); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(out.log, "IPA: %s\n", result.IPAPath)
		fmt.Fprintf(out.log, "Workflow: %s\n", result.WorkflowURL)
	}
	if !distribute {
		return nil
	}
	return runDistribute(cmd, cfg, result.IPAPath)
}

// buildJSON is the JSON of `ios build --json` (and init's "build").
type buildJSON struct {
	BuildID     string  `json:"build_id"`
	IPAPath     string  `json:"ipa"`
	IPASize     int64   `json:"ipa_size"`
	WorkflowURL string  `json:"workflow_url"`
	Provider    string  `json:"provider"`
	Profile     string  `json:"profile,omitempty"`
	Duration    float64 `json:"duration_seconds"`
}

func newBuildJSON(r *build.BuildResult, provider, profile string) *buildJSON {
	return &buildJSON{
		BuildID: r.BuildID, IPAPath: r.IPAPath, IPASize: r.IPASize, WorkflowURL: r.WorkflowURL,
		Provider: provider, Profile: profile, Duration: r.Duration.Round(time.Second).Seconds(),
	}
}

// shareJSON is the JSON of `ios share --json`. Ready means the simulator is
// in MobAI now; Submitted means the provider accepted the run and it shows up
// once the build and bridge start.
type shareJSON struct {
	BuildID       string `json:"build_id"`
	Provider      string `json:"provider"`
	WorkflowURL   string `json:"workflow_url"`
	RunID         string `json:"run_id,omitempty"`
	Ready         bool   `json:"ready"`
	Submitted     bool   `json:"submitted"`
	CancelCommand string `json:"cancel_command,omitempty"`
}

func runIOSShare(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	duration, _ := cmd.Flags().GetDuration("duration")
	remote, _ := cmd.Flags().GetString("remote")
	// A simulator build takes no profile, so the provider is the flag, else
	// builder.json's.
	provider, _ := cmd.Flags().GetString("provider")
	out := newOutput(cmd)

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	// Ctrl+C cancels ctx; Share cancels the run if interrupted before the sim is
	// shared. After that it has returned and the job outlives the command.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	ghClient, err := clientForProvider(cfg, provider)
	if err != nil {
		return err
	}
	result, err := build.NewCoordinatorWithOutput(cfg, ghClient, out.log).Share(ctx, build.ShareOptions{
		Provider: provider,
		Duration: duration,
		Remote:   remote,
	})
	if err != nil {
		return err
	}

	name, _ := cfg.ProviderName(provider)
	if out.json {
		res := shareJSON{BuildID: result.BuildID, Provider: name, WorkflowURL: result.WorkflowURL, Ready: !result.Submitted, Submitted: result.Submitted}
		if result.Submitted {
			res.RunID = result.ProviderRunID
			res.CancelCommand = fmt.Sprintf("builder ios cancel --provider %s --run-id %s", name, result.ProviderRunID)
		} else if result.RunID != 0 {
			res.RunID = strconv.FormatInt(result.RunID, 10)
		}
		return printJSON(cmd, res)
	}

	w := out.log
	fmt.Fprintln(w)
	if result.Submitted {
		fmt.Fprintln(w, "Simulator session submitted. The build and bridge must start before it appears in MobAI under CI Devices.")
		fmt.Fprintln(w, "The workflow is limited to 90 minutes including setup/build; idle duration is not a guaranteed session length.")
		fmt.Fprintf(w, "Workflow: %s\n", result.WorkflowURL)
		fmt.Fprintf(w, "Cancel: builder ios cancel --provider %s --run-id %s\n", name, result.ProviderRunID)
		fmt.Fprintf(w, "After the run finishes, remove its snapshot: git push %s --delete refs/ios-builder/jobs/%s\n", remote, result.BuildID)
		return nil
	}
	fmt.Fprintln(w, "Simulator ready. Open MobAI and find it under CI Devices.")
	fmt.Fprintln(w, "It closes when you stop its bridge there, or after being left unused.")
	fmt.Fprintf(w, "Workflow: %s\n", result.WorkflowURL)

	return nil
}

// runBuild provisions a missing signing set (GitHub) and runs the build,
// with progress on log.
func runBuild(ctx context.Context, cfg *config.Config, opts *build.BuildOptions, log io.Writer) (*build.BuildResult, error) {
	ghClient, err := clientForProvider(cfg, opts.Provider)
	if err != nil {
		return nil, err
	}
	// A GitHub build with a distribution needs its signing set in the
	// repository; ensureSigningSecrets leaves Codemagic and Bitrise alone.
	if ghClient != nil && !opts.Unsigned {
		if err := ensureSigningSecrets(ctx, cfg, ghClient, getASCClient, opts.Profile, opts.Provider, log); err != nil {
			return nil, err
		}
	}
	return build.NewCoordinatorWithOutput(cfg, ghClient, log).Build(ctx, opts)
}
