package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/MobAI-App/ios-builder/internal/exitcode"
	"github.com/manifoldco/promptui"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// noInput is the global --no-input flag.
var noInput bool

// stdinIsTerminal reports whether stdin is a terminal. Tests replace it.
var stdinIsTerminal = func() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// envTrue reads a boolean environment variable the way CI systems set them.
func envTrue(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// interactive reports whether cmd may ask questions: stdin is a terminal and
// nothing asked for non-interactive behaviour (--no-input, BUILDER_NO_INPUT,
// CI, or --json, whose stdout belongs to the result). Every prompt checks it
// first; without it a question either takes its default under --yes or fails
// with needInput naming the flag that answers it.
func interactive(cmd *cobra.Command) bool {
	if noInput || envTrue("BUILDER_NO_INPUT") || envTrue("CI") {
		return false
	}
	if cmd != nil {
		if f := cmd.Flags().Lookup("json"); f != nil && f.Value.String() == "true" {
			return false
		}
	}
	return stdinIsTerminal()
}

// needInput is the usage error (exit 2) for a question that cannot be asked.
// flags name what answers it, e.g. "--project" or "--yes".
func needInput(what string, flags ...string) error {
	return exitcode.Usagef("%s is needed and there is no terminal to ask on (or --no-input/CI is set); pass %s", what, joinOr(flags))
}

func joinOr(items []string) string {
	switch len(items) {
	case 0:
		return "the matching flag"
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " or " + items[len(items)-1]
}

// asker answers the questions of one command: --yes takes each default, a
// terminal prompts, anything else is a needInput error naming the flag.
type asker struct {
	interactive bool
	yes         bool
}

func newAsker(cmd *cobra.Command) asker {
	yes, _ := cmd.Flags().GetBool("yes")
	return asker{interactive: interactive(cmd), yes: yes}
}

// text asks for a line of text; def is what --yes and an empty answer take.
func (a asker) text(label, def, flag string) (string, error) {
	if a.yes {
		return def, nil
	}
	if !a.interactive {
		return "", needInput(fmt.Sprintf("%s (default %q)", strings.ToLower(label[:1])+label[1:], def), flag, "--yes for the default")
	}
	return promptString(label, def)
}

// required asks for a value that has no default, so --yes cannot answer it.
func (a asker) required(label, flag string) (string, error) {
	if !a.interactive {
		return "", needInput(strings.ToLower(label[:1])+label[1:], flag)
	}
	return promptString(label, "")
}

// password asks for a password with masked input; only its flag answers it
// without a terminal.
func (a asker) password(label, flag string) (string, error) {
	if !a.interactive {
		return "", needInput(strings.ToLower(label[:1])+label[1:], flag)
	}
	return promptPassword(label)
}

// confirm asks a yes/no question; def is what --yes takes. In a terminal an
// empty answer is no, as promptui's confirm has always had it.
func (a asker) confirm(label string, def bool, flag string) (bool, error) {
	if a.yes {
		return def, nil
	}
	if !a.interactive {
		return false, needInput(fmt.Sprintf("an answer to %q", label), flag, "--yes")
	}
	_, err := (&promptui.Prompt{Label: label, IsConfirm: true}).Run()
	return err == nil, nil
}
