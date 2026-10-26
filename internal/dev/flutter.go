package dev

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/MobAI-App/ios-builder/internal/config"
	"github.com/MobAI-App/ios-builder/internal/hotreload"
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
}

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
			if u := hotreload.VMServiceURL(output.Data); u != "" {
				fmt.Printf("Found debug service: %s\n", u)
				return u, debugOutput, nil
			}
		}
	}
}

func (h *FlutterHandler) runFlutterAttach(ctx context.Context, deviceID, debugURL string, outputChan <-chan mobai.DebugOutput) error {
	fmt.Println()
	fmt.Printf("VM Service URL (on device): %s\n", debugURL)
	fmt.Println()

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

	cfg := hotreload.FileWatcherConfig{
		Dirs:     h.watchConfig.Dirs,
		Patterns: h.watchConfig.Patterns,
		Ignore:   h.watchConfig.Ignore,
		Debounce: time.Duration(h.watchConfig.Debounce) * time.Millisecond,
		OnChange: func() {
			fmt.Println("\n[builder] File change detected, triggering hot reload...")
			h.stdinMux.SendCommand('r')
		},
	}

	if hotreload.NeedsPolling(cfg.Dirs) {
		fmt.Println("\n[builder] WSL2 detected, using polling watcher for Windows filesystem")
		h.watcher = hotreload.NewPollingWatcher(&cfg)
	} else {
		w, err := hotreload.NewFileWatcher(&cfg)
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
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("get executable path: %w", err)
	}

	return hotreload.WriteCustomDevice(&hotreload.CustomDevice{
		Ping:                    []string{execPath, "mobai", "--url", mobaiURL, "ping"},
		PingSuccessRegex:        "success",
		Install:                 []string{execPath, "mobai", "--url", mobaiURL, "install", "${localPath}"},
		Uninstall:               []string{"echo", "uninstall not supported"},
		RunDebug:                []string{execPath, "mobai", "--url", mobaiURL, "run-debug", "${appName}"},
		ForwardPort:             []string{execPath, "mobai", "--url", mobaiURL, "forward", "${devicePort}", "${hostPort}"},
		ForwardPortSuccessRegex: "forwarded",
	})
}
