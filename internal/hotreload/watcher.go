// Package hotreload holds the pieces of hot reload through MobAI that need no
// terminal: the MobAI iOS custom device Flutter attaches through, the Dart VM
// service URL a debug build prints, the file watcher that triggers reloads,
// and pointing a React Native debug build at Metro. The dev commands build on
// it, and pkg/dev exposes it to other programs.
package hotreload

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// isWSL2 detects if running in WSL2 environment.
func isWSL2() bool {
	data, err := os.ReadFile("/proc/version")
	if err != nil {
		return false
	}
	version := strings.ToLower(string(data))
	return strings.Contains(version, "microsoft") || strings.Contains(version, "wsl")
}

// isWindowsMountedPath checks if path is on a Windows-mounted filesystem in WSL.
func isWindowsMountedPath(path string) bool {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	return strings.HasPrefix(absPath, "/mnt/")
}

// NeedsPolling determines if polling should be used instead of inotify.
func NeedsPolling(dirs []string) bool {
	if !isWSL2() {
		return false
	}
	for _, dir := range dirs {
		if isWindowsMountedPath(dir) {
			return true
		}
	}
	return false
}

// Watcher watches files and calls OnChange, until Stop.
type Watcher interface {
	Start(ctx context.Context) error
	Stop()
}

// NewWatcher returns the watcher that works for cfg's directories: a polling
// one for Windows files under WSL2, where change events do not arrive.
func NewWatcher(cfg *FileWatcherConfig) (Watcher, error) {
	if NeedsPolling(cfg.Dirs) {
		return NewPollingWatcher(cfg), nil
	}
	return NewFileWatcher(cfg)
}

// FileWatcherConfig holds configuration for the file watcher.
type FileWatcherConfig struct {
	Dirs     []string
	Patterns []string
	Ignore   []string
	Debounce time.Duration
	OnChange func()
}

// FileWatcher watches for file changes and triggers callbacks.
type FileWatcher struct {
	watcher *fsnotify.Watcher
	cfg     FileWatcherConfig
	done    chan struct{}
	mu      sync.Mutex
	timer   *time.Timer
}

// NewFileWatcher creates a new file watcher with the given configuration.
func NewFileWatcher(cfg *FileWatcherConfig) (*FileWatcher, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	return &FileWatcher{
		watcher: watcher,
		cfg:     *cfg,
		done:    make(chan struct{}),
	}, nil
}

// Start begins watching for file changes. It blocks until the context is cancelled.
func (w *FileWatcher) Start(ctx context.Context) error {
	for _, dir := range w.cfg.Dirs {
		if err := w.addRecursive(dir); err != nil {
			return err
		}
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-w.done:
			return nil
		case event, ok := <-w.watcher.Events:
			if !ok {
				return nil
			}
			w.handleEvent(event)
		case err, ok := <-w.watcher.Errors:
			if !ok {
				return nil
			}
			fmt.Printf("[builder] File watcher error: %v\n", err)
		}
	}
}

// Stop stops the file watcher.
func (w *FileWatcher) Stop() {
	w.mu.Lock()
	if w.timer != nil {
		w.timer.Stop()
	}
	w.mu.Unlock()

	select {
	case <-w.done:
		// Already closed
	default:
		close(w.done)
	}
	w.watcher.Close() //nolint:errcheck // Cleanup on shutdown
}

func (w *FileWatcher) addRecursive(root string) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			// Skip hidden directories
			if strings.HasPrefix(d.Name(), ".") && path != root {
				return filepath.SkipDir
			}
			if err := w.watcher.Add(path); err != nil {
				return nil
			}
		}
		return nil
	})
}

func (w *FileWatcher) handleEvent(event fsnotify.Event) {
	if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) == 0 {
		return
	}

	if event.Op&fsnotify.Rename != 0 {
		if _, err := os.Stat(event.Name); err != nil {
			return
		}
	}

	if !w.matchesPattern(event.Name) {
		return
	}

	if w.shouldIgnore(event.Name) {
		return
	}

	if event.Op&fsnotify.Create != 0 {
		if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
			if err := w.addRecursive(event.Name); err != nil {
				fmt.Printf("[builder] Warning: failed to watch new directory %s: %v\n", event.Name, err)
			}
			return
		}
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.timer != nil {
		w.timer.Stop()
	}

	w.timer = time.AfterFunc(w.cfg.Debounce, func() {
		if w.cfg.OnChange != nil {
			w.cfg.OnChange()
		}
	})
}

// matchesPattern checks if the file matches any of the configured patterns.
func (w *FileWatcher) matchesPattern(path string) bool {
	for _, pattern := range w.cfg.Patterns {
		if strings.HasSuffix(path, pattern) {
			return true
		}
	}
	return false
}

// shouldIgnore checks if the file matches any ignore patterns.
func (w *FileWatcher) shouldIgnore(path string) bool {
	for _, pattern := range w.cfg.Ignore {
		if strings.HasSuffix(path, pattern) {
			return true
		}
	}
	return false
}
