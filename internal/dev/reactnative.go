package dev

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/MobAI-App/ios-builder/internal/mobai"
)

const (
	defaultMetroPort           = 8081
	metroStartupTimeoutSeconds = 30
)

// ReactNativeHandler implements FrameworkHandler for React Native projects.
type ReactNativeHandler struct {
	metroPort   int
	showLogs    bool
	metroCmd    *exec.Cmd
	windowsHost string // Windows host IP (extracted from mobaiURL for WSL)
	emit        func(Event)
}

// SetEmitter reports the Metro URL as a "metro" event.
func (h *ReactNativeHandler) SetEmitter(emit func(Event)) { h.emit = emit }

// NewReactNativeHandler creates a new React Native handler.
func NewReactNativeHandler(metroPort int, showLogs bool, mobaiURL string) *ReactNativeHandler {
	if metroPort == 0 {
		metroPort = defaultMetroPort
	}
	windowsHost := extractHostFromURL(mobaiURL)
	return &ReactNativeHandler{
		metroPort:   metroPort,
		showLogs:    showLogs,
		windowsHost: windowsHost,
	}
}

// extractHostFromURL extracts the hostname from a URL.
func extractHostFromURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}

// Setup checks if Metro is running and starts it if not.
func (h *ReactNativeHandler) Setup(ctx context.Context) error {
	if h.checkMetroRunning() {
		fmt.Printf("Metro bundler already running on port %d\n", h.metroPort)
		return nil
	}

	fmt.Printf("Starting Metro bundler on port %d...\n", h.metroPort)
	return h.startMetro(ctx)
}

// Attach monitors app output for React Native development.
func (h *ReactNativeHandler) Attach(ctx context.Context, deviceID string, debugOutput <-chan mobai.DebugOutput) error {
	ip := getLocalIP()

	fmt.Println()
	fmt.Printf("Metro bundler: http://%s:%d\n", ip, h.metroPort)
	fmt.Println()
	if h.emit != nil {
		h.emit(Event{Event: "metro", DeviceID: deviceID, URL: fmt.Sprintf("http://%s:%d", ip, h.metroPort)})
	}
	fmt.Println("React Native hot reload active!")
	fmt.Println("  - Shake device or press 'd' in Metro to open dev menu")
	fmt.Println("  - Press 'r' in Metro terminal to reload")
	fmt.Println("  - Press Ctrl+C to stop")
	fmt.Println()
	fmt.Println("NOTE: First run may fail if iOS prompts for local network permission.")
	fmt.Println("      If so, grant permission and run this command again.")
	fmt.Println()

	for output := range debugOutput {
		switch output.Type {
		case "exit":
			fmt.Println("App exited")
			return nil
		case "error":
			if strings.Contains(output.Message, "launch failed") {
				fmt.Println()
				fmt.Println("Launch failed: developer not trusted.")
				fmt.Println()
				fmt.Println("On device go to: Settings > General > VPN & Device Management")
				fmt.Println("Then tap your developer certificate and trust it.")
				return fmt.Errorf("launch failed: developer not trusted")
			}
			fmt.Printf("App error: %s\n", output.Message)
		case "stdout", "stderr":
			if h.showLogs && output.Data != "" {
				fmt.Print(output.Data)
			}
		}
	}

	return nil
}

// DebugConfig returns launch arguments to configure Metro connection.
// React Native reads RCT_jsLocation from NSUserDefaults, which can be set via launch arguments.
func (h *ReactNativeHandler) DebugConfig() *mobai.DebugConfig {
	ip := getLocalIP()

	fmt.Printf("Detected host IP: %s\n", ip)

	jsLocation := fmt.Sprintf("%s:%d", ip, h.metroPort)

	return &mobai.DebugConfig{
		Arguments: []string{"-RCT_jsLocation", jsLocation},
	}
}

// Stop cleans up Metro if we started it.
func (h *ReactNativeHandler) Stop() {
	if h.metroCmd != nil && h.metroCmd.Process != nil {
		_ = h.metroCmd.Process.Kill()
	}
}

func (h *ReactNativeHandler) checkMetroRunning() bool {
	host := "localhost"
	if isWSL() && h.windowsHost != "" {
		host = h.windowsHost
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(h.metroPort)), 2*time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func (h *ReactNativeHandler) startMetro(ctx context.Context) error {
	port := strconv.Itoa(h.metroPort)
	metroCmd := fmt.Sprintf("npx react-native start --port %s --host 0.0.0.0", port)

	switch runtime.GOOS {
	case "darwin":
		script := fmt.Sprintf(`tell application "Terminal"
			activate
			do script "cd '%s' && %s"
		end tell`, getWorkingDir(), metroCmd)
		h.metroCmd = exec.Command("osascript", "-e", script)
	case "windows":
		h.metroCmd = exec.Command("cmd", "/c", "start", "Metro Bundler", "cmd", "/k", metroCmd)
	default:
		if isWSL() {
			wd := getWorkingDir()
			wslCmd := fmt.Sprintf(`cd '%s' && %s`, wd, metroCmd)
			h.metroCmd = exec.Command("cmd.exe", "/c", "start", "", "cmd.exe", "/k", "wsl.exe", "bash", "-c", wslCmd)
		} else if _, err := exec.LookPath("gnome-terminal"); err == nil {
			h.metroCmd = exec.Command("gnome-terminal", "--", "bash", "-c", metroCmd+"; exec bash")
		} else if _, err := exec.LookPath("xterm"); err == nil {
			h.metroCmd = exec.Command("xterm", "-hold", "-e", metroCmd)
		} else {
			fmt.Println("No terminal emulator found. Starting Metro in background...")
			h.metroCmd = exec.CommandContext(ctx, "npx", "react-native", "start", "--port", port, "--host", "0.0.0.0")
			h.metroCmd.Stdout = os.Stdout
			h.metroCmd.Stderr = os.Stderr
		}
	}

	if err := h.metroCmd.Start(); err != nil {
		return fmt.Errorf("start metro: %w", err)
	}

	fmt.Println("Waiting for Metro to start...")
	for range metroStartupTimeoutSeconds {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
		if h.checkMetroRunning() {
			fmt.Println("Metro bundler ready")
			return nil
		}
	}

	return fmt.Errorf("timeout waiting for Metro to start")
}

func getWorkingDir() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

func getLocalIP() string {
	if isWSL() {
		cmd := exec.Command("powershell.exe", "-NoProfile", "-Command",
			`Get-NetIPAddress -AddressFamily IPv4 | Where-Object { ($_.IPAddress -notlike "169.*") -and ($_.IPAddress -notlike "127.*") -and ($_.IPAddress -notlike "172.*") } | Select-Object -First 1 -ExpandProperty IPAddress`)
		output, err := cmd.Output()
		if err != nil {
			return "localhost"
		}
		ip := strings.TrimSpace(string(output))
		if ip == "" {
			return "localhost"
		}
		return ip
	}

	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "localhost"
	}
	defer func() { _ = conn.Close() }()

	localAddr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return "localhost"
	}
	return localAddr.IP.String()
}
