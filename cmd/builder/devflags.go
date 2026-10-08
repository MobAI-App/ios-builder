package main

import (
	"encoding/json"
	"os"
	"sync"

	"github.com/MobAI-App/ios-builder/internal/dev"
	"github.com/spf13/cobra"
)

// appleIDPasswordEnv carries the re-sign password, which has no flag so it
// stays out of shell history and process lists.
const appleIDPasswordEnv = "BUILDER_APPLE_ID_PASSWORD"

// addDevAgentFlags adds the flags that answer a dev session's questions.
func addDevAgentFlags(c *cobra.Command) {
	c.Flags().BoolP("yes", "y", false, "Take the default answers: first device, no re-sign, the IPA's bundle ID")
	c.Flags().Bool("resign", false, "Re-sign the IPA on install (--resign=false: install as is) without asking; needs --apple-id and "+appleIDPasswordEnv+" off a terminal")
	c.Flags().String("apple-id", "", "Apple ID to re-sign with (password from "+appleIDPasswordEnv+")")
	c.Flags().Bool("json", false, "Print session events as JSON lines on stdout (all other output goes to stderr); implies --no-input")
}

// devInput reads those flags into a dev.Input.
func devInput(cmd *cobra.Command) dev.Input {
	in := dev.Input{Interactive: interactive(cmd), Password: os.Getenv(appleIDPasswordEnv)}
	in.Yes, _ = cmd.Flags().GetBool("yes")
	in.AppleID, _ = cmd.Flags().GetString("apple-id")
	if cmd.Flags().Changed("resign") {
		resign, _ := cmd.Flags().GetBool("resign")
		in.Resign = &resign
	}
	return in
}

// devEvents sets up --json for a dev session: events go to stdout as one
// JSON object per line, and everything the session and the tools it runs
// (flutter attach, Metro) print goes to stderr. The handlers print with fmt,
// so os.Stdout itself is pointed at stderr until restore.
func devEvents(cmd *cobra.Command) (emit func(dev.Event), restore func()) {
	if !newOutput(cmd).json {
		return nil, func() {}
	}
	enc := json.NewEncoder(cmd.OutOrStdout())
	var mu sync.Mutex
	stdout := os.Stdout
	os.Stdout = os.Stderr
	return func(e dev.Event) {
			mu.Lock()
			defer mu.Unlock()
			_ = enc.Encode(e)
		}, func() {
			os.Stdout = stdout
		}
}

// configureDevSession applies the flags and --json to session.
func configureDevSession(cmd *cobra.Command, session *dev.Session, emit func(dev.Event)) {
	session.SetInput(devInput(cmd))
	if emit != nil {
		session.SetEvents(emit)
	}
}
