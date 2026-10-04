package dev

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/MobAI-App/ios-builder/internal/config"
	"github.com/MobAI-App/ios-builder/internal/mobai"
)

// watcher is the interface for file change watchers.
type watcher interface {
	Start(ctx context.Context) error
	Stop()
}

// FlutterHandler implements FrameworkHandler for Flutter projects.
type FlutterHandler struct {
	mobaiURL    string
	noAttach    bool
	noWatch     bool
	watchConfig *config.WatchConfig
	watcher     watcher
	stdinMux    *StdinMux
	cancelWatch context.CancelFunc
	emit        func(Event)
}

// SetEmitter reports the VM Service URL as a "vm_service" event.
func (h *FlutterHandler) SetEmitter(emit func(Event)) { h.emit = emit }

// NewFlutterHandler creates a new Flutter handler.
func NewFlutterHandler(mobaiURL string, noAttach, noWatch bool, watchCfg *config.WatchConfig) *FlutterHandler {
	return &FlutterHandler{
		mobaiURL:    mobaiURL,
		noAttach:    noAttach,
		noWatch:     noWatch,
		watchConfig: watchCfg,
	}
}

// Setup configures Flutter custom device for MobAI.
func (h *FlutterHandler) Setup(ctx context.Context) error {
	return EnsureCustomDevice(h.mobaiURL)
}

// Attach finds the VM Service URL and runs flutter attach.
func (h *FlutterHandler) Attach(ctx context.Context, deviceID string, debugOutput <-chan mobai.DebugOutput) error {
	fmt.Println("Waiting for debug service...")

	debugURL, outputChan, err := h.findDebugURL(ctx, debugOutput)
	if err != nil {
		return err
	}

	return h.runFlutterAttach(ctx, deviceID, debugURL, outputChan)
}

// DebugConfig returns nil for Flutter (no special environment needed).
func (h *FlutterHandler) DebugConfig() *mobai.DebugConfig {
	return nil
}

func (h *FlutterHandler) Stop() {
	if h.watcher != nil {
		h.watcher.Stop()
	}
	if h.stdinMux != nil {
		if err := h.stdinMux.Stop(); err != nil {
			fmt.Printf("[builder] Warning: failed to close stdin mux: %v\n", err)
		}
	}
	if h.cancelWatch != nil {
		h.cancelWatch()
	}
}

func (h *FlutterHandler) findDebugURL(ctx context.Context, debugOutput <-chan mobai.DebugOutput) (string, <-chan mobai.DebugOutput, error) {
	re := regexp.MustCompile(`(?:Observatory|Dart VM service)[^h]*(http://[^\s]+)`)
	timeout := time.After(30 * time.Second)

	for {
		select {
		case <-ctx.Done():
			return "", nil, ctx.Err()
		case <-timeout:
			return "", nil, fmt.Errorf("timeout waiting for debug service - is this a debug build?")
		case output, ok := <-debugOutput:
			if !ok {
				return "", nil, fmt.Errorf("debug stream closed")
			}
			if output.Type == "error" {
				return "", nil, fmt.Errorf("debug error: %s", output.Message)
			}
			if m := re.FindStringSubmatch(output.Data); len(m) >= 2 {
				fmt.Printf("Found debug service: %s\n", m[1])
				return m[1], debugOutput, nil
			}
		}
	}
}

func (h *FlutterHandler) runFlutterAttach(ctx context.Context, deviceID, debugURL string, outputChan <-chan mobai.DebugOutput) error {
	fmt.Println()
	fmt.Printf("VM Service URL (on device): %s\n", debugURL)
	fmt.Println()
	if h.emit != nil {
		h.emit(Event{Event: "vm_service", DeviceID: deviceID, URL: debugURL,
			Command: fmt.Sprintf("MOBAI_DEVICE_ID=%s flutter attach -d mobai-ios --debug-url=%s", deviceID, debugURL)})
	}

	go func() {
		for output := range outputChan {
			if output.Type == "exit" {
				return
			}
		}
	}()

	if h.noAttach {
		fmt.Println("Run this command to attach Flutter debugger:")
		fmt.Println()
		fmt.Printf("  MOBAI_DEVICE_ID=%s flutter attach -d mobai-ios --debug-url=%s\n", deviceID, debugURL)
		fmt.Println()
		return nil
	}

	fmt.Println("Attaching Flutter debugger...")
	fmt.Println()

	cmd := exec.CommandContext(ctx, "flutter", "attach", "-d", "mobai-ios", "--debug-url="+debugURL)
	cmd.Env = append(os.Environ(), "MOBAI_DEVICE_ID="+deviceID)

	h.stdinMux = NewStdinMux()
	cmd.Stdin = h.stdinMux.Reader()
	go h.stdinMux.Start(ctx)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("create stdout pipe: %w", err)
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start flutter attach: %w", err)
	}

	go h.handleFlutterOutput(ctx, stdoutPipe)

	err = cmd.Wait()
	if err != nil {
		return &RuntimeError{Err: err}
	}
	return nil
}

func (h *FlutterHandler) handleFlutterOutput(ctx context.Context, stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	readyDetected := false

	for scanner.Scan() {
		line := scanner.Text()
		fmt.Println(line)

		if !readyDetected && strings.Contains(line, "Flutter run key commands") {
			readyDetected = true
			h.onFlutterReady(ctx)
		}
	}
}

func (h *FlutterHandler) onFlutterReady(ctx context.Context) {
	if h.stdinMux != nil {
		fmt.Println("\n[builder] Sending initial hot restart...")
		h.stdinMux.SendCommand('R')
	}

	if !h.noWatch && h.watchConfig != nil && h.stdinMux != nil {
		h.startFileWatcher(ctx)
	}
}

func (h *FlutterHandler) startFileWatcher(ctx context.Context) {
	watchCtx, cancel := context.WithCancel(ctx)
	h.cancelWatch = cancel

	cfg := FileWatcherConfig{
		Dirs:     h.watchConfig.Dirs,
		Patterns: h.watchConfig.Patterns,
		Ignore:   h.watchConfig.Ignore,
		Debounce: time.Duration(h.watchConfig.Debounce) * time.Millisecond,
		OnChange: func() {
			fmt.Println("\n[builder] File change detected, triggering hot reload...")
			h.stdinMux.SendCommand('r')
		},
	}

	if needsPolling(cfg.Dirs) {
		fmt.Println("\n[builder] WSL2 detected, using polling watcher for Windows filesystem")
		h.watcher = NewPollingWatcher(&cfg)
	} else {
		w, err := NewFileWatcher(&cfg)
		if err != nil {
			fmt.Printf("\n[builder] Warning: failed to start file watcher: %v\n", err)
			return
		}
		h.watcher = w
	}

	fmt.Printf("[builder] Watching for changes in: %v\n", cfg.Dirs)

	go func() {
		if err := h.watcher.Start(watchCtx); err != nil && err != context.Canceled {
			fmt.Printf("\n[builder] File watcher error: %v\n", err)
		}
	}()
}

// EnsureCustomDevice ensures Flutter custom_devices.json has mobai-ios configured.
func EnsureCustomDevice(mobaiURL string) error {
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

	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("get executable path: %w", err)
	}

	pingCmd := []string{execPath, "mobai", "--url", mobaiURL, "ping"}
	installCmd := []string{execPath, "mobai", "--url", mobaiURL, "install", "${localPath}"}
	runDebugCmd := []string{execPath, "mobai", "--url", mobaiURL, "run-debug", "${appName}"}
	forwardCmd := []string{execPath, "mobai", "--url", mobaiURL, "forward", "${devicePort}", "${hostPort}"}

	config := map[string]any{
		"custom-devices": []any{
			map[string]any{
				"id":                      "mobai-ios",
				"label":                   "MobAI iOS Device",
				"sdkNameAndVersion":       "iOS (via MobAI)",
				"platform":                nil,
				"enabled":                 true,
				"ping":                    pingCmd,
				"pingSuccessRegex":        "success",
				"install":                 installCmd,
				"uninstall":               []string{"echo", "uninstall not supported"},
				"runDebug":                runDebugCmd,
				"forwardPort":             forwardCmd,
				"forwardPortSuccessRegex": "forwarded",
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
