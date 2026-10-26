// Package dev exposes the hot reload pieces of the dev commands to code
// outside this module: the MobAI iOS custom device that flutter attach
// drives, the Dart VM service URL a debug build prints, the file watcher
// that triggers reloads, and the launch arguments that point a React Native
// debug build at Metro.
//
// A program that runs its own dev sessions through MobAI supplies the custom
// device's commands itself instead of builder's mobai subcommands.
package dev

import (
	"github.com/MobAI-App/ios-builder/internal/hotreload"
)

// CustomDeviceID is the Flutter device id of the MobAI iOS custom device:
// flutter attach -d mobai-ios.
const CustomDeviceID = hotreload.CustomDeviceID

// CustomDevice is the commands Flutter's custom device runs for the MobAI
// iOS device. Flutter fills in ${localPath}, ${appName}, ${devicePort} and
// ${hostPort}; each success regex must match a line of the command's output.
// Flutter keeps the forward command running for the life of the forward and
// takes its exit as a failed forward.
type CustomDevice = hotreload.CustomDevice

// WriteCustomDevice turns on Flutter's custom devices and writes d as the
// mobai-ios device to Flutter's custom_devices.json, replacing what is there.
func WriteCustomDevice(d *CustomDevice) error {
	return hotreload.WriteCustomDevice(d)
}

// VMServiceURL returns the Dart VM service URL in a line of a Flutter debug
// build's output, or "".
func VMServiceURL(line string) string {
	return hotreload.VMServiceURL(line)
}

// FileWatcherConfig is what a Watcher watches: directories, the file
// suffixes that count, the suffixes to ignore, the debounce, and the call
// made after a change.
type FileWatcherConfig = hotreload.FileWatcherConfig

// Watcher watches files and calls OnChange, until Stop.
type Watcher = hotreload.Watcher

// NewWatcher returns the watcher that works for cfg's directories: a polling
// one for Windows files under WSL2, where change events do not arrive.
func NewWatcher(cfg *FileWatcherConfig) (Watcher, error) {
	return hotreload.NewWatcher(cfg)
}

// LocalIP is this machine's address on the local network, the one a phone on
// the same Wi-Fi reaches it at; under WSL, the Windows host's. "localhost"
// when there is none.
func LocalIP() string {
	return hotreload.LocalIP()
}

// MetroLaunchArguments are the launch arguments that point a React Native
// debug build at the Metro bundler on host:port.
func MetroLaunchArguments(host string, port int) []string {
	return hotreload.MetroLaunchArguments(host, port)
}
