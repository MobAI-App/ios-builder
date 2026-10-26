package hotreload

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// PollingWatcher watches for file changes using polling (for WSL2 compatibility).
type PollingWatcher struct {
	cfg      FileWatcherConfig
	done     chan struct{}
	mu       sync.Mutex
	timer    *time.Timer
	mtimes   map[string]time.Time
	interval time.Duration
}

// NewPollingWatcher creates a new polling-based file watcher.
func NewPollingWatcher(cfg *FileWatcherConfig) *PollingWatcher {
	return &PollingWatcher{
		cfg:      *cfg,
		done:     make(chan struct{}),
		mtimes:   make(map[string]time.Time),
		interval: 500 * time.Millisecond,
	}
}

// Start begins polling for file changes. It blocks until the context is cancelled.
func (w *PollingWatcher) Start(ctx context.Context) error {
	if err := w.scan(); err != nil {
		return err
	}

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-w.done:
			return nil
		case <-ticker.C:
			if changed := w.checkChanges(); changed {
				w.triggerDebounced()
			}
		}
	}
}

// Stop stops the polling watcher.
func (w *PollingWatcher) Stop() {
	w.mu.Lock()
	if w.timer != nil {
		w.timer.Stop()
	}
	w.mu.Unlock()

	select {
	case <-w.done:
	default:
		close(w.done)
	}
}

func (w *PollingWatcher) scan() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	for _, dir := range w.cfg.Dirs {
		if err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if strings.HasPrefix(d.Name(), ".") && path != dir {
					return filepath.SkipDir
				}
				return nil
			}
			if !w.matchesPattern(path) || w.shouldIgnore(path) {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			w.mtimes[path] = info.ModTime()
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func (w *PollingWatcher) checkChanges() bool {
	w.mu.Lock()
	defer w.mu.Unlock()

	changed := false
	currentFiles := make(map[string]bool)

	for _, dir := range w.cfg.Dirs {
		_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if strings.HasPrefix(d.Name(), ".") && path != dir {
					return filepath.SkipDir
				}
				return nil
			}
			if !w.matchesPattern(path) || w.shouldIgnore(path) {
				return nil
			}
			currentFiles[path] = true
			info, err := d.Info()
			if err != nil {
				return nil
			}
			mtime := info.ModTime()
			if oldMtime, exists := w.mtimes[path]; !exists || !mtime.Equal(oldMtime) {
				w.mtimes[path] = mtime
				changed = true
			}
			return nil
		})
	}

	for path := range w.mtimes {
		if !currentFiles[path] {
			delete(w.mtimes, path)
		}
	}

	return changed
}

func (w *PollingWatcher) triggerDebounced() {
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

func (w *PollingWatcher) matchesPattern(path string) bool {
	for _, pattern := range w.cfg.Patterns {
		if strings.HasSuffix(path, pattern) {
			return true
		}
	}
	return false
}

func (w *PollingWatcher) shouldIgnore(path string) bool {
	for _, pattern := range w.cfg.Ignore {
		if strings.HasSuffix(path, pattern) {
			return true
		}
	}
	return false
}
