// Package dev provides development session management for Flutter and React Native hot reload.
package dev

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/MobAI-App/ios-builder/internal/mobai"
	"github.com/gorilla/websocket"
	"github.com/manifoldco/promptui"
	"howett.net/plist"
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
}

// NewSession creates a new development session.
func NewSession(mobaiURL, deviceID, ipaPath string, h FrameworkHandler) *Session {
	return &Session{
		mobai:    mobai.NewClient(mobaiURL),
		mobaiURL: mobaiURL,
		deviceID: deviceID,
		ipaPath:  ipaPath,
		handler:  h,
	}
}

// SetSkipInstall configures the session to skip installation.
func (s *Session) SetSkipInstall(skip bool, bundleID string) {
	s.skipInstall = skip
	if bundleID != "" {
		s.bundleID = bundleID
	}
}

// FindIPA lists IPAs in distDir and lets user select if multiple.
func FindIPA(distDir string) (string, error) {
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
	}

	debugOutput, err := s.launchApp(ctx)
	if err != nil {
		return err
	}

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
	} else if len(devices) == 1 {
		device = &devices[0]
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
	return s.mobai.Claim(ctx, s.deviceID)
}

func (s *Session) installApp(ctx context.Context) error {
	absPath, err := filepath.Abs(s.ipaPath)
	if err != nil {
		return fmt.Errorf("get absolute path: %w", err)
	}

	// Read the IPA from the local path; on WSL absPath becomes a Windows path
	// that only MobAI can open.
	ipaBundleID := extractBundleIDFromIPA(absPath)
	absPath = toWindowsPathIfWSL(absPath)

	req := mobai.InstallAppRequest{Path: absPath}

	resignPrompt := promptui.Select{
		Label: "Resign app",
		Items: []string{"No", "Yes"},
	}
	idx, _, err := resignPrompt.Run()
	if err != nil {
		return err
	}

	if idx == 1 {
		req.Resign = true

		appleIDPrompt := promptui.Prompt{Label: "Apple ID"}
		req.AppleID, err = appleIDPrompt.Run()
		if err != nil {
			return err
		}

		passwordPrompt := promptui.Prompt{Label: "Password", Mask: '*'}
		req.Password, err = passwordPrompt.Run()
		if err != nil {
			return err
		}
	}

	fmt.Println("Installing app...")
	resp, err := s.mobai.InstallApp(ctx, s.deviceID, req)
	if err != nil {
		return fmt.Errorf("install app: %w", err)
	}

	s.bundleID = resp.Data.BundleID
	if s.bundleID == "" {
		bundlePrompt := promptui.Prompt{Label: "Bundle ID", Default: guessBundleID(resp, ipaBundleID, req.Resign)}
		s.bundleID, err = bundlePrompt.Run()
		if err != nil {
			return err
		}
	}

	fmt.Printf("Installed: %s\n", s.bundleID)
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

func extractBundleIDFromIPA(ipaPath string) string {
	r, err := zip.OpenReader(ipaPath)
	if err != nil {
		return ""
	}
	defer func() { _ = r.Close() }()

	for _, f := range r.File {
		if strings.HasPrefix(f.Name, "Payload/") && strings.HasSuffix(f.Name, ".app/Info.plist") {
			rc, err := f.Open()
			if err != nil {
				return ""
			}
			data, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				return ""
			}

			var info struct {
				BundleID string `plist:"CFBundleIdentifier"`
			}
			if _, err := plist.Unmarshal(data, &info); err != nil {
				return ""
			}
			return info.BundleID
		}
	}
	return ""
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
