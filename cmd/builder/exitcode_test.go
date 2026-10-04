package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/MobAI-App/ios-builder/internal/exitcode"
	"github.com/MobAI-App/ios-builder/internal/github"
	"github.com/spf13/cobra"
)

// exitTree is a small command tree with the same usage wiring as rootCmd.
func exitTree(runErr error) *cobra.Command {
	root := &cobra.Command{Use: "builder", SilenceUsage: true, SilenceErrors: true}
	one := &cobra.Command{Use: "one <arg>", Args: cobra.ExactArgs(1), RunE: func(*cobra.Command, []string) error { return runErr }}
	one.Flags().Bool("flag", false, "")
	req := &cobra.Command{Use: "req", RunE: func(*cobra.Command, []string) error { return nil }}
	req.Flags().String("must", "", "")
	_ = req.MarkFlagRequired("must")
	group := &cobra.Command{Use: "group"}
	group.AddCommand(&cobra.Command{Use: "leaf", RunE: func(*cobra.Command, []string) error { return nil }})
	root.AddCommand(one, req, group)
	usageErrors(root)
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	return root
}

func TestExitCodeMapping(t *testing.T) {
	tests := []struct {
		name   string
		args   []string
		runErr error
		want   int
	}{
		{"ok", []string{"one", "x"}, nil, exitcode.OK},
		{"generic failure", []string{"one", "x"}, errors.New("boom"), exitcode.Failure},
		{"unknown flag", []string{"one", "x", "--nope"}, nil, exitcode.Usage},
		{"wrong arg count", []string{"one"}, nil, exitcode.Usage},
		{"unknown command", []string{"nope"}, nil, exitcode.Usage},
		{"unknown subcommand", []string{"group", "nope"}, nil, exitcode.Usage},
		{"group alone shows help", []string{"group"}, nil, exitcode.OK},
		{"missing required flag", []string{"req"}, nil, exitcode.Usage},
		{"auth missing", []string{"one", "x"}, getGitHubClientError(), exitcode.Auth},
		{"build failed", []string{"one", "x"}, fmt.Errorf("build failed: %w", &github.RunFailedError{Conclusion: "failure"}), exitcode.BuildFailed},
		{"timeout", []string{"one", "x"}, fmt.Errorf("wait: %w", context.DeadlineExceeded), exitcode.Timeout},
		{"interrupted", []string{"one", "x"}, fmt.Errorf("upload: %w", context.Canceled), exitcode.Interrupted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := exitTree(tt.runErr)
			root.SetArgs(tt.args)
			if got := exitCode(root.Execute()); got != tt.want {
				t.Errorf("exit code = %d, want %d", got, tt.want)
			}
		})
	}
}

// getGitHubClientError is what a command sees without a GitHub login.
func getGitHubClientError() error {
	return exitcode.With(exitcode.Auth, errors.New("not authenticated. Run: builder auth github"))
}

// TestRootArgsAreUsageErrors checks the wiring on the real command tree:
// an argument error must exit 2 without running anything.
func TestRootArgsAreUsageErrors(t *testing.T) {
	usageErrors(rootCmd)
	rootCmd.SetOut(io.Discard)
	rootCmd.SetErr(io.Discard)
	t.Cleanup(func() { rootCmd.SetArgs(nil); rootCmd.SetOut(nil); rootCmd.SetErr(nil) })
	for _, args := range [][]string{{"mobai", "install"}, {"auth", "apple", "extra"}, {"ios", "nope"}} {
		rootCmd.SetArgs(args)
		if got := exitCode(rootCmd.Execute()); got != exitcode.Usage {
			t.Errorf("builder %v: exit code %d, want %d", args, got, exitcode.Usage)
		}
	}
}
