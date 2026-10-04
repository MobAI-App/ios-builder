package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/MobAI-App/ios-builder/internal/config"
	"github.com/MobAI-App/ios-builder/internal/dev"
	"github.com/MobAI-App/ios-builder/internal/exitcode"
	"github.com/MobAI-App/ios-builder/internal/mobai"
	"github.com/spf13/cobra"
)

var devKMPCmd = &cobra.Command{
	Use:     "kmp",
	Aliases: []string{"kotlin"},
	Short:   "Install and launch a Kotlin Multiplatform app on device",
	Long: `Installs and launches a Kotlin Multiplatform iOS app on a device using MobAI.

Kotlin Multiplatform has no hot reload on iOS: the shared Kotlin code is compiled
to a native framework at build time. For code changes, rebuild with
'builder ios build' and re-run this command.

Prerequisites:
- MobAI running with an iOS device connected
- IPA built with 'builder ios build'`,
	RunE: runDevKMP,
}

func init() {
	devCmd.AddCommand(devKMPCmd)
	devKMPCmd.Flags().StringP("device", "d", "", "Device ID (default: first available)")
	devKMPCmd.Flags().String("mobai-url", mobai.DefaultBaseURL, "MobAI API URL")
	devKMPCmd.Flags().String("ipa", "", "Path to IPA (default: auto-detect from dist/)")
	devKMPCmd.Flags().Bool("skip-install", false, "Skip app installation (app must already be installed)")
	devKMPCmd.Flags().String("bundle-id", "", "Bundle ID (required with --skip-install; else used when MobAI does not report it)")
	addDevAgentFlags(devKMPCmd)
	devKMPCmd.Flags().Bool("logs", false, "Show app logs")
}

func runDevKMP(cmd *cobra.Command, args []string) error {
	deviceID, _ := cmd.Flags().GetString("device")
	mobaiURL, _ := cmd.Flags().GetString("mobai-url")
	ipaPath, _ := cmd.Flags().GetString("ipa")
	skipInstall, _ := cmd.Flags().GetBool("skip-install")
	bundleID, _ := cmd.Flags().GetString("bundle-id")
	showLogs, _ := cmd.Flags().GetBool("logs")

	if cfg, err := config.NewManager().Load(); err == nil {
		if !cmd.Flags().Changed("mobai-url") && cfg.MobAI.URL != "" {
			mobaiURL = cfg.MobAI.URL
		}
		if !cmd.Flags().Changed("device") && cfg.MobAI.DeviceID != "" {
			deviceID = cfg.MobAI.DeviceID
		}
	}

	emit, restore := devEvents(cmd)
	defer restore()

	if skipInstall {
		if bundleID == "" {
			return exitcode.Usagef("--bundle-id is required when using --skip-install")
		}
	} else {
		if ipaPath == "" {
			var err error
			ipaPath, err = dev.FindIPA("dist", interactive(cmd))
			if err != nil {
				return err
			}
		}

		if _, err := os.Stat(ipaPath); os.IsNotExist(err) {
			return fmt.Errorf("IPA not found: %s", ipaPath)
		}

		fmt.Printf("Found IPA: %s\n", ipaPath)
		fmt.Println()
	}

	handler := dev.NewKMPHandler(showLogs)
	session := dev.NewSession(mobaiURL, deviceID, ipaPath, handler)
	session.SetSkipInstall(skipInstall, bundleID)
	configureDevSession(cmd, session, emit)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt)

	go func() {
		<-sigChan
		fmt.Println("\nShutting down...")
		cancel()
		session.Stop()
	}()

	err := session.Start(ctx)
	session.Stop()

	if _, ok := err.(*dev.RuntimeError); ok {
		fmt.Println()
		fmt.Println("Tip: If connection failed, try:")
		fmt.Println("  1. Reconnect the device (unplug/replug)")
		fmt.Println("  2. Restart MobAI")
		fmt.Println("  3. Run 'builder mobai ping' to verify connection")
	}

	return err
}
