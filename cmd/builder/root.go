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
	"strings"
	"syscall"
	"time"

	"github.com/MobAI-App/ios-builder/internal/auth"
	"github.com/MobAI-App/ios-builder/internal/build"
	"github.com/MobAI-App/ios-builder/internal/config"
	"github.com/MobAI-App/ios-builder/internal/github"
	"github.com/MobAI-App/ios-builder/internal/update"
	"github.com/MobAI-App/ios-builder/internal/workflow"
	"github.com/manifoldco/promptui"
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
		return nil, fmt.Errorf("not authenticated. Run: builder auth github")
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

func isExpoProject() bool {
	data, err := os.ReadFile("package.json")
	if err != nil {
		return false
	}
	return strings.Contains(string(data), `"expo"`)
}

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

	return "", ""
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
	fmt.Println("Builder - iOS Build Setup")
	fmt.Println()

	// Get remote name from flag
	remoteName, _ := cmd.Flags().GetString("remote")

	// Detect GitHub repo from git remote
	githubOwner, repoName, err := detectGitHubRepo(remoteName)
	if err != nil {
		return fmt.Errorf("failed to detect GitHub repository: %w\nMake sure you're in a git repository with a GitHub remote", err)
	}

	fmt.Printf("Detected repository: %s/%s (from remote '%s')\n", githubOwner, repoName, remoteName)
	fmt.Println()

	// Get project name
	projectName, _ := cmd.Flags().GetString("project")
	if projectName == "" {
		cwd, _ := os.Getwd()
		defaultProject := filepath.Base(cwd)
		projectName, err = promptString("Project name", defaultProject)
		if err != nil {
			return err
		}
	}

	// Detect iOS path
	iosPath, _ := cmd.Flags().GetString("ios-path")
	scheme, _ := cmd.Flags().GetString("scheme")

	if iosPath == "" {
		detectedPath, framework := detectIOSPath()
		if detectedPath != "" {
			fmt.Printf("Detected %s project (iOS at '%s')\n", framework, detectedPath)
			confirmPrompt := promptui.Prompt{
				Label:     "Use this path",
				IsConfirm: true,
			}
			_, err := confirmPrompt.Run()
			if err == nil {
				iosPath = detectedPath
			}
		} else if framework != "" {
			fmt.Printf("Detected %s project\n", framework)
		}

		if iosPath == "" && framework == "" {
			fmt.Println("No iOS project detected in current directory.")
			fmt.Println("If this is a hybrid app (React Native, Flutter, etc.),")
			iosPath, _ = promptString("Path to iOS folder (leave empty for root)", "")
		}
	}

	// Detect Flutter and prompt for version
	var flutterVersion string
	if isFlutterProject() {
		fmt.Println()
		fmt.Println("Detected Flutter project")
		localVersion := getLocalFlutterVersion()
		if localVersion != "" {
			fmt.Printf("Local Flutter version: %s\n", localVersion)
		}
		flutterVersion, err = promptString("Flutter version for builds (leave empty for latest)", localVersion)
		if err != nil {
			return err
		}
	}

	// Detect Kotlin Multiplatform and prompt for JDK version
	var jdkVersion string
	if isKMPProject() {
		fmt.Println()
		fmt.Println("Detected Kotlin Multiplatform project")
		fmt.Println("Note: KMP has no hot reload on iOS - rebuild for code changes.")
		jdkVersion, err = promptString("JDK version for Gradle builds", "17")
		if err != nil {
			return err
		}
	}

	fmt.Println()
	fmt.Printf("Project:    %s\n", projectName)
	fmt.Printf("Repository: %s/%s\n", githubOwner, repoName)
	if iosPath != "" {
		fmt.Printf("iOS Path:   %s\n", iosPath)
	}
	if flutterVersion != "" {
		fmt.Printf("Flutter:    %s\n", flutterVersion)
	}
	if jdkVersion != "" {
		fmt.Printf("JDK:        %s\n", jdkVersion)
	}
	fmt.Println()

	// Create workflow file locally
	fmt.Println("Creating workflow file...")
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
	fmt.Printf("  Created: %s\n", workflowPath)

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
	fmt.Printf("  Created: %s\n", sharePath)

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

	fmt.Println("Creating builder.json...")
	mgr := config.NewManager()
	if err := mgr.Save(cfg); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	fmt.Println("  Created: builder.json")

	fmt.Println()
	fmt.Println("Setup complete!")
	fmt.Println()

	// Ask to commit and push
	commitPrompt := promptui.Prompt{
		Label:     "Commit and push workflow",
		IsConfirm: true,
	}
	_, commitErr := commitPrompt.Run()

	if commitErr == nil {
		fmt.Println()
		fmt.Println("Committing and pushing...")

		// Git add
		addCmd := exec.Command("git", "add", ".github/workflows/ios-build.yml", ".github/workflows/ios-share.yml", "builder.json")
		if output, err := addCmd.CombinedOutput(); err != nil {
			fmt.Printf("  Warning: git add failed: %s\n", strings.TrimSpace(string(output)))
		} else {
			fmt.Println("  Added files to staging")
		}

		// Git commit
		commitCmd := exec.Command("git", "commit", "-m", "Add iOS build workflow")
		if output, err := commitCmd.CombinedOutput(); err != nil {
			outputStr := strings.TrimSpace(string(output))
			if strings.Contains(outputStr, "nothing to commit") {
				fmt.Println("  Nothing to commit (already committed)")
			} else {
				fmt.Printf("  Warning: git commit failed: %s\n", outputStr)
			}
		} else {
			fmt.Println("  Committed changes")
		}

		// Git push
		pushCmd := exec.Command("git", "push")
		if output, err := pushCmd.CombinedOutput(); err != nil {
			fmt.Printf("  Warning: git push failed: %s\n", strings.TrimSpace(string(output)))
		} else {
			fmt.Println("  Pushed to remote")
		}
		fmt.Println()
	}

	// Ask to run build
	buildPrompt := promptui.Prompt{
		Label:     "Run build now",
		IsConfirm: true,
	}
	_, buildErr := buildPrompt.Run()

	if buildErr == nil {
		fmt.Println()
		return runBuild(context.Background(), cfg, build.BuildOptions{
			OutputDir: "dist",
			Timeout:   30 * time.Minute,
			Remote:    remoteName,
		})
	}

	fmt.Println()
	fmt.Println("To build later, run:")
	fmt.Println("  builder ios build")
	fmt.Println()

	return nil
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

Requires MobAI Pro and a MOBAI_API_KEY secret on the selected provider.
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
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(updateCmd)
	rootCmd.AddCommand(iosCmd)
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

	// iOS build command flags
	iosBuildCmd.Flags().StringP("output", "o", "dist", "Output directory for IPA")
	iosBuildCmd.Flags().Duration("timeout", 30*time.Minute, "Build timeout")
	iosBuildCmd.Flags().Bool("unsigned", false, "Build unsigned IPA (skip code signing even if configured)")
	iosBuildCmd.Flags().StringP("remote", "r", "origin", "Git remote to push the working-tree snapshot to")
	iosBuildCmd.Flags().String("provider", "", "Override CI provider (default github or builder.json provider)")
	iosCmd.AddCommand(iosBuildCmd)

	// iOS share command flags
	iosShareCmd.Flags().Duration("duration", 30*time.Minute, "How long the simulator stays available while unused")
	iosShareCmd.Flags().StringP("remote", "r", "origin", "Git remote to push the working-tree snapshot to")
	iosShareCmd.Flags().String("provider", "", "Override CI provider (default github or builder.json provider)")
	iosCmd.AddCommand(iosShareCmd)
}

func runIOSBuild(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	outputDir, _ := cmd.Flags().GetString("output")
	timeout, _ := cmd.Flags().GetDuration("timeout")
	unsigned, _ := cmd.Flags().GetBool("unsigned")
	remote, _ := cmd.Flags().GetString("remote")
	provider, _ := cmd.Flags().GetString("provider")

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	name, err := cfg.ProviderName(provider)
	if err != nil {
		return err
	}
	if name != "github" {
		var stop context.CancelFunc
		ctx, stop = signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()
	}
	return runBuild(ctx, cfg, build.BuildOptions{
		Provider:  provider,
		OutputDir: outputDir,
		Timeout:   timeout,
		Unsigned:  unsigned,
		Remote:    remote,
	})
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
	provider, _ := cmd.Flags().GetString("provider")

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
	result, err := build.NewCoordinator(cfg, ghClient).Share(ctx, build.ShareOptions{
		Provider: provider,
		Duration: duration,
		Remote:   remote,
	})
	if err != nil {
		return err
	}

	fmt.Println()
	if result.Submitted {
		name, _ := cfg.ProviderName(provider)
		fmt.Println("Simulator session submitted. The build and bridge must start before it appears in MobAI under CI Devices.")
		fmt.Println("The workflow is limited to 90 minutes including setup/build; idle duration is not a guaranteed session length.")
		fmt.Printf("Workflow: %s\n", result.WorkflowURL)
		fmt.Printf("Cancel: builder ios cancel --provider %s --run-id %s\n", name, result.ProviderRunID)
		fmt.Printf("After the run finishes, remove its snapshot: git push %s --delete refs/ios-builder/jobs/%s\n", remote, result.BuildID)
		return nil
	}
	fmt.Println("Simulator ready. Open MobAI and find it under CI Devices.")
	fmt.Println("It closes when you stop its bridge there, or after being left unused.")
	fmt.Printf("Workflow: %s\n", result.WorkflowURL)

	return nil
}

func runBuild(ctx context.Context, cfg *config.Config, opts build.BuildOptions) error {
	ghClient, err := clientForProvider(cfg, opts.Provider)
	if err != nil {
		return err
	}

	coordinator := build.NewCoordinator(cfg, ghClient)

	result, err := coordinator.Build(ctx, opts)
	if err != nil {
		return err
	}

	fmt.Printf("IPA: %s\n", result.IPAPath)
	fmt.Printf("Workflow: %s\n", result.WorkflowURL)

	return nil
}
