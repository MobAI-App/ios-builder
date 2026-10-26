package hotreload

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
)

// CustomDeviceID is the Flutter device id of the MobAI iOS custom device:
// flutter attach -d mobai-ios.
const CustomDeviceID = "mobai-ios"

// CustomDevice is the commands Flutter's custom device runs for the MobAI
// iOS device. Flutter fills in ${localPath}, ${appName}, ${devicePort} and
// ${hostPort}; each success regex must match a line of the command's output.
// Flutter keeps the forward command running for the life of the forward and
// takes its exit as a failed forward.
type CustomDevice struct {
	Ping                    []string
	PingSuccessRegex        string
	Install                 []string
	Uninstall               []string
	RunDebug                []string
	ForwardPort             []string
	ForwardPortSuccessRegex string
}

// WriteCustomDevice turns on Flutter's custom devices and writes d as the
// mobai-ios device to Flutter's custom_devices.json.
func WriteCustomDevice(d *CustomDevice) error {
	if err := exec.Command("flutter", "config", "--enable-custom-devices").Run(); err != nil {
		return fmt.Errorf("failed to enable custom devices - run 'flutter config --enable-custom-devices' manually first: %w", err)
	}

	var configPath string
	if runtime.GOOS == "windows" {
		appData := os.Getenv("APPDATA")
		if appData == "" {
			return fmt.Errorf("APPDATA environment variable not set")
		}
		configPath = filepath.Join(appData, ".flutter_custom_devices.json")
	} else {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("get home dir: %w", err)
		}
		flutterDir := filepath.Join(homeDir, ".config", "flutter")
		if err := os.MkdirAll(flutterDir, 0755); err != nil {
			return fmt.Errorf("create flutter config dir: %w", err)
		}
		configPath = filepath.Join(flutterDir, "custom_devices.json")
	}

	config := map[string]any{
		"custom-devices": []any{
			map[string]any{
				"id":                      CustomDeviceID,
				"label":                   "MobAI iOS Device",
				"sdkNameAndVersion":       "iOS (via MobAI)",
				"platform":                nil,
				"enabled":                 true,
				"ping":                    d.Ping,
				"pingSuccessRegex":        d.PingSuccessRegex,
				"install":                 d.Install,
				"uninstall":               d.Uninstall,
				"runDebug":                d.RunDebug,
				"forwardPort":             d.ForwardPort,
				"forwardPortSuccessRegex": d.ForwardPortSuccessRegex,
			},
		},
	}

	output, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	if err := os.WriteFile(configPath, output, 0644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	return nil
}

// vmServiceRE finds the Dart VM service URL a debug build prints at launch.
var vmServiceRE = regexp.MustCompile(`(?:Observatory|Dart VM service)[^h]*(http://[^\s]+)`)

// VMServiceURL returns the Dart VM service URL in a line of a Flutter debug
// build's output, or "".
func VMServiceURL(line string) string {
	if m := vmServiceRE.FindStringSubmatch(line); len(m) >= 2 {
		return m[1]
	}
	return ""
}
