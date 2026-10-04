// Package dev provides development session management for Flutter and React Native hot reload.
package dev

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/MobAI-App/ios-builder/internal/exitcode"
	"github.com/MobAI-App/ios-builder/internal/ipa"
	"github.com/MobAI-App/ios-builder/internal/mobai"
	"github.com/gorilla/websocket"
	"github.com/manifoldco/promptui"
)

// FrameworkHandler handles framework-specific dev workflow.
type FrameworkHandler interface {
	// Setup runs before app install (e.g., Flutter custom device, Metro start)
	Setup(ctx context.Context) error
	// DebugConfig returns environment variables and arguments for app launch
	DebugConfig() *mobai.DebugConfig
	// Attach runs after app launch to enable hot reload
	Attach(ctx context.Context, deviceID string, debugOutput <-chan mobai.DebugOutput) error
	// Stop cleans up resources
	Stop()
}

// Session manages a development session with MobAI.
type Session struct {
	mobai       *mobai.Client
	mobaiURL    string
	deviceID    string
	ipaPath     string
	bundleID    string
	debugConn   *websocket.Conn
	skipInstall bool
	handler     FrameworkHandler
	input       Input
	emit        func(Event)
}

// Input answers the questions a session asks: which device, whether to
// re-sign and with which Apple ID, and the bundle ID when MobAI does not
// report it.
type Input struct {
	// Interactive allows prompts on the terminal. Without it every question
	// takes its answer from here or fails naming the flag that gives it.
	Interactive bool
	// Yes takes the first device when several are connected.
	Yes bool
	// Resign re-signs the IPA on install; nil asks in a terminal, else no.
	Resign *bool
	// AppleID and Password are the account to re-sign with.
	AppleID  string
	Password string
}

// Event is one line of a session's NDJSON output (--json): "device",
// "installed", "launched", then what the handler adds ("vm_service" for
// Flutter, "metro" for React Native).
type Event struct {
	Event      string `json:"event"`
	DeviceID   string `json:"device_id,omitempty"`
	DeviceName string `json:"device_name,omitempty"`
	BundleID   string `json:"bundle_id,omitempty"`
	Resigned   bool   `json:"resigned,omitempty"`
	Skipped    bool   `json:"skipped,omitempty"` // installed: --skip-install
	URL        string `json:"url,omitempty"`
	Command    string `json:"command,omitempty"`
}

// emitterSetter is implemented by handlers that report events of their own.
type emitterSetter interface {
	SetEmitter(func(Event))
}

// NewSession creates a new development session. It prompts in the terminal
// until SetInput says otherwise.
func NewSession(mobaiURL, deviceID, ipaPath string, h FrameworkHandler) *Session {
	return &Session{
		mobai:    mobai.NewClient(mobaiURL),
		mobaiURL: mobaiURL,
		deviceID: deviceID,
		ipaPath:  ipaPath,
		handler:  h,
		input:    Input{Interactive: true},
		emit:     func(Event) {},
	}
}

// SetInput sets how the session's questions are answered.
func (s *Session) SetInput(in Input) { s.input = in }

// SetEvents sends the session's events to emit, and the handler's when it
// reports any.
func (s *Session) SetEvents(emit func(Event)) {
	s.emit = emit
	if h, ok := s.handler.(emitterSetter); ok {
		h.SetEmitter(emit)
	}
}

// InputError is a question a non-interactive session could not ask; Flags
// name what answers it.
type InputError struct {
	What  string
	Flags []string
}

func (e *InputError) Error() string {
	return fmt.Sprintf("%s is needed and there is no terminal to ask on (or --no-input/CI is set); pass %s", e.What, strings.Join(e.Flags, " or "))
}

// ExitCode makes a missing answer a usage error.
func (e *InputError) ExitCode() int { return exitcode.Usage }

// SetSkipInstall configures the session to skip installation.
func (s *Session) SetSkipInstall(skip bool, bundleID string) {
	s.skipInstall = skip
	if bundleID != "" {
		s.bundleID = bundleID
	}
}

// FindIPA lists IPAs in distDir and lets user select if multiple; without a
// terminal it takes the newest, as ios upload and ios distribute do.
func FindIPA(distDir string, interactive bool) (string, error) {
	if distDir == "" {
		distDir = "dist"
	}

	matches, err := filepath.Glob(filepath.Join(distDir, "*.ipa"))
	if err != nil {
		return "", fmt.Errorf("search IPA: %w", err)
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no IPA in %s - run 'builder ios build' first", distDir)
	}

	if len(matches) == 1 {
		return matches[0], nil
	}
	if !interactive {
		return ipa.Newest(distDir)
	}

	names := make([]string, len(matches))
	for i, p := range matches {
		names[i] = filepath.Base(p)
	}

	prompt := promptui.Select{
		Label: "Select IPA",
		Items: names,
	}

	idx, _, err := prompt.Run()
	if err != nil {
		return "", err
	}

	return matches[idx], nil
}

// Start runs the full dev session: setup, install, launch, attach.
func (s *Session) Start(ctx context.Context) error {
	if s.handler != nil {
		if err := s.handler.Setup(ctx); err != nil {
			fmt.Printf("Warning: setup failed: %v\n", err)
		}
	}

	if err := s.connectDevice(ctx); err != nil {
		return err
	}
	go s.mobai.KeepLease(ctx)

	if !s.skipInstall {
		if err := s.installApp(ctx); err != nil {
			return err
		}
	} else {
		fmt.Printf("Skipping install, using bundle ID: %s\n", s.bundleID)
		s.emit(Event{Event: "installed", DeviceID: s.deviceID, BundleID: s.bundleID, Skipped: true})
	}

	debugOutput, err := s.launchApp(ctx)
	if err != nil {
		return err
	}
	s.emit(Event{Event: "launched", DeviceID: s.deviceID, BundleID: s.bundleID})

	if s.handler != nil {
		return s.handler.Attach(ctx, s.deviceID, debugOutput)
	}

	for range debugOutput {
	}
	return nil
}

// Stop cleans up resources.
func (s *Session) Stop() {
	if s.handler != nil {
		s.handler.Stop()
	}
	if s.debugConn != nil {
		_ = s.debugConn.Close()
	}
}

func (s *Session) connectDevice(ctx context.Context) error {
	fmt.Println("Connecting to MobAI...")

	allDevices, err := s.mobai.ListDevices(ctx)
	if err != nil {
		return fmt.Errorf("connect to MobAI: %w", err)
	}

	// Filter to physical iOS devices only (no simulators)
	var devices []mobai.Device
	for _, d := range allDevices {
		if !d.Virtual {
			devices = append(devices, d)
		}
	}
	if len(devices) == 0 {
		return fmt.Errorf("no physical iOS devices connected")
	}

	var device *mobai.Device
	if s.deviceID != "" {
		for i := range devices {
			if devices[i].ID == s.deviceID {
				device = &devices[i]
				break
			}
		}
		if device == nil {
			return fmt.Errorf("device %s not found", s.deviceID)
		}
	} else if len(devices) == 1 || s.input.Yes {
		device = &devices[0]
	} else if !s.input.Interactive {
		ids := make([]string, len(devices))
		for i, d := range devices {
			ids[i] = fmt.Sprintf("%s (%s)", d.ID, d.Name)
		}
		return &InputError{What: "a device (connected: " + strings.Join(ids, ", ") + ")", Flags: []string{"--device <id>", "--yes for the first"}}
	} else {
		names := make([]string, len(devices))
		for i, d := range devices {
			names[i] = d.Name
		}

		prompt := promptui.Select{
			Label: "Select device",
			Items: names,
		}
		idx, _, err := prompt.Run()
		if err != nil {
			return err
		}
		device = &devices[idx]
	}

	s.deviceID = device.ID
	fmt.Printf("Using device: %s\n", device.Name)
	if err := s.mobai.Claim(ctx, s.deviceID); err != nil {
		return err
	}
	s.emit(Event{Event: "device", DeviceID: device.ID, DeviceName: device.Name})
	return nil
}

// resignRequest settles the re-sign question: the Input answer, else a
// prompt in a terminal, else no. Missing credentials for a re-sign are
// prompted for or are an InputError.
func (s *Session) resignRequest(req *mobai.InstallAppRequest) error {
	in := s.input
	resign := false
	switch {
	case in.Resign != nil:
		resign = *in.Resign
	case in.Interactive && !in.Yes:
		resignPrompt := promptui.Select{
			Label: "Resign app",
			Items: []string{"No", "Yes"},
		}
		idx, _, err := resignPrompt.Run()
		if err != nil {
			return err
		}
		resign = idx == 1
	}
	if !resign {
		return nil
	}
	req.Resign, req.AppleID, req.Password = true, in.AppleID, in.Password
	var err error
	if req.AppleID == "" {
		if !in.Interactive {
			return &InputError{What: "the Apple ID to re-sign with", Flags: []string{"--apple-id"}}
		}
		appleIDPrompt := promptui.Prompt{Label: "Apple ID"}
		if req.AppleID, err = appleIDPrompt.Run(); err != nil {
			return err
		}
	}
	if req.Password == "" {
		if !in.Interactive {
			return &InputError{What: "the Apple ID password to re-sign with", Flags: []string{"BUILDER_APPLE_ID_PASSWORD in the environment"}}
		}
		passwordPrompt := promptui.Prompt{Label: "Password", Mask: '*'}
		if req.Password, err = passwordPrompt.Run(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Session) installApp(ctx context.Context) error {
	absPath, err := filepath.Abs(s.ipaPath)
	if err != nil {
		return fmt.Errorf("get absolute path: %w", err)
	}

	// Read the IPA from the local path; on WSL absPath becomes a Windows path
	// that only MobAI can open.
	ipaBundleID := ipa.BundleID(absPath)
	absPath = toWindowsPathIfWSL(absPath)

	req := mobai.InstallAppRequest{Path: absPath}
	if err := s.resignRequest(&req); err != nil {
		return err
	}

	fmt.Println("Installing app...")
	resp, err := s.mobai.InstallApp(ctx, s.deviceID, req)
	if err != nil {
		return fmt.Errorf("install app: %w", err)
	}

	// MobAI's answer, else --bundle-id, else a prompt (or the guess without
	// a terminal).
	given := s.bundleID
	s.bundleID = resp.Data.BundleID
	if s.bundleID == "" {
		guess := guessBundleID(resp, ipaBundleID, req.Resign)
		switch {
		case given != "":
			s.bundleID = given
		case !s.input.Interactive || s.input.Yes:
			if guess == "" {
				return &InputError{What: "the installed app's bundle ID (MobAI did not report it)", Flags: []string{"--bundle-id"}}
			}
			s.bundleID = guess
		default:
			bundlePrompt := promptui.Prompt{Label: "Bundle ID", Default: guess}
			if s.bundleID, err = bundlePrompt.Run(); err != nil {
				return err
			}
		}
	}

	fmt.Printf("Installed: %s\n", s.bundleID)
	s.emit(Event{Event: "installed", DeviceID: s.deviceID, BundleID: s.bundleID, Resigned: req.Resign})
	return nil
}

// guessBundleID suggests the bundle ID an install left on the device when
// MobAI's response doesn't name it: the IPA's own ID, with the team ID appended
// when the app was re-signed and MobAI reported the team.
func guessBundleID(resp *mobai.InstallAppResponse, ipaBundleID string, resigned bool) string {
	if resigned && ipaBundleID != "" && resp.Data.TeamID != "" {
		return ipaBundleID + "." + resp.Data.TeamID
	}
	return ipaBundleID
}

func (s *Session) launchApp(ctx context.Context) (<-chan mobai.DebugOutput, error) {
	fmt.Println("Launching app with debugger...")

	var config *mobai.DebugConfig
	if s.handler != nil {
		config = s.handler.DebugConfig()
	}

	outputChan, conn, err := s.mobai.DebugStream(ctx, s.deviceID, s.bundleID, config)
	if err != nil {
		errMsg := err.Error()
		if strings.Contains(errMsg, "Error code: 2") || strings.Contains(errMsg, "launch failed") {
			return nil, fmt.Errorf("launch failed: developer not trusted - on device go to Settings > General > VPN & Device Management and trust the developer")
		}
		return nil, fmt.Errorf("debug stream: %w", err)
	}
	s.debugConn = conn

	return outputChan, nil
}

// RuntimeError wraps runtime errors to distinguish from CLI errors
type RuntimeError struct {
	Err error
}

func (e *RuntimeError) Error() string {
	return e.Err.Error()
}

func isWSL() bool {
	data, err := os.ReadFile("/proc/version")
	if err != nil {
		return false
	}
	lower := strings.ToLower(string(data))
	return strings.Contains(lower, "microsoft") || strings.Contains(lower, "wsl")
}

// toWindowsPathIfWSL converts a WSL path to Windows path if running in WSL
func toWindowsPathIfWSL(path string) string {
	if !isWSL() {
		return path
	}

	cmd := exec.Command("wslpath", "-w", path)
	out, err := cmd.Output()
	if err != nil {
		return path
	}

	return strings.TrimSpace(string(out))
}
