package main

import (
	"os"
	"strings"

	"github.com/MobAI-App/ios-builder/internal/exitcode"
	"github.com/spf13/cobra"
)

// version is set at build time via -ldflags "-X main.version=vX.Y.Z".
var version = "dev"

func main() {
	usageErrors(rootCmd)
	os.Exit(exitCode(rootCmd.Execute()))
}

// exitCode is exitcode.Code plus the usage errors cobra raises itself. Flag
// parsing and argument validators are tagged at the source (usageErrors);
// an unknown subcommand and a missing required flag are only recognisable by
// their message.
func exitCode(err error) int {
	if err != nil && exitcode.Code(err) == exitcode.Failure {
		msg := err.Error()
		for _, prefix := range []string{"unknown command", "required flag(s)", "if any flags in the group", "at least one of the flags"} {
			if strings.HasPrefix(msg, prefix) {
				return exitcode.Usage
			}
		}
	}
	return exitcode.Code(err)
}

// usageErrors makes cobra's flag and argument errors exit with Usage (2):
// the flag error hook covers parsing, and every command's Args validator is
// wrapped so "accepts 1 arg(s)" and the like are tagged too.
func usageErrors(root *cobra.Command) {
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return exitcode.With(exitcode.Usage, err)
	})
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		// cobra shows help and succeeds for `builder ios nope`, since a
		// group without a Run is never validated; give groups a help Run
		// so a mistyped subcommand is an unknown-command error instead.
		if c.HasParent() && c.HasSubCommands() && !c.Runnable() {
			c.Args = cobra.NoArgs
			c.RunE = func(cmd *cobra.Command, _ []string) error { return cmd.Help() }
		}
		if args := c.Args; args != nil {
			c.Args = func(cmd *cobra.Command, a []string) error {
				return exitcode.With(exitcode.Usage, args(cmd, a))
			}
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)
}
