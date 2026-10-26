package hotreload

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestFileWatcher_MatchesPattern(t *testing.T) {
	tests := []struct {
		name     string
		file     string
		patterns []string
		ignore   []string
		want     bool
	}{
		{
			name:     "matches .dart",
			file:     "main.dart",
			patterns: []string{".dart"},
			ignore:   nil,
			want:     true,
		},
		{
			name:     "ignores .g.dart",
			file:     "main.g.dart",
			patterns: []string{".dart"},
			ignore:   []string{".g.dart"},
			want:     false,
		},
		{
			name:     "ignores .freezed.dart",
			file:     "model.freezed.dart",
			patterns: []string{".dart"},
			ignore:   []string{".g.dart", ".freezed.dart"},
			want:     false,
		},
		{
			name:     "no match",
			file:     "main.go",
			patterns: []string{".dart"},
			ignore:   nil,
			want:     false,
		},
		{
			name:     "multiple patterns",
			file:     "style.css",
			patterns: []string{".dart", ".css"},
			ignore:   nil,
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := &FileWatcher{
				cfg: FileWatcherConfig{
					Patterns: tt.patterns,
					Ignore:   tt.ignore,
				},
			}

			got := w.matchesPattern(tt.file) && !w.shouldIgnore(tt.file)
			if got != tt.want {
				t.Errorf("matchesPattern(%q) && !shouldIgnore = %v, want %v", tt.file, got, tt.want)
			}
		})
	}
}

func TestFileWatcher_Debounce(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.dart")

	if err := os.WriteFile(testFile, []byte("initial"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	var callCount atomic.Int32

	cfg := FileWatcherConfig{
		Dirs:     []string{tmpDir},
		Patterns: []string{".dart"},
		Ignore:   []string{".g.dart"},
		Debounce: 150 * time.Millisecond,
		OnChange: func() {
			callCount.Add(1)
		},
	}

	watcher, err := NewFileWatcher(&cfg)
	if err != nil {
		t.Fatalf("failed to create watcher: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- watcher.Start(ctx)
	}()

	time.Sleep(100 * time.Millisecond)

	for i := range 3 {
		if err := os.WriteFile(testFile, []byte("change"+string(rune('0'+i))), 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	time.Sleep(250 * time.Millisecond)

	watcher.Stop()

	if err := <-errCh; err != nil && !errors.Is(err, context.Canceled) {
		t.Errorf("watcher.Start() returned unexpected error: %v", err)
	}

	if count := callCount.Load(); count != 1 {
		t.Errorf("onChange called %d times, want 1", count)
	}
}

func TestFileWatcher_RecursiveWatch(t *testing.T) {
	tmpDir := t.TempDir()
	nestedDir := filepath.Join(tmpDir, "lib", "src", "widgets")
	if err := os.MkdirAll(nestedDir, 0755); err != nil {
		t.Fatalf("failed to create nested dir: %v", err)
	}

	var callCount atomic.Int32

	cfg := FileWatcherConfig{
		Dirs:     []string{tmpDir},
		Patterns: []string{".dart"},
		Ignore:   []string{},
		Debounce: 10 * time.Millisecond,
		OnChange: func() {
			callCount.Add(1)
		},
	}

	watcher, err := NewFileWatcher(&cfg)
	if err != nil {
		t.Fatalf("failed to create watcher: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- watcher.Start(ctx)
	}()

	// Wait for watcher to start
	time.Sleep(50 * time.Millisecond)

	// Create file in nested directory
	testFile := filepath.Join(nestedDir, "button.dart")
	if err := os.WriteFile(testFile, []byte("widget"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	// Wait for debounce
	time.Sleep(50 * time.Millisecond)

	watcher.Stop()

	// Check for unexpected errors
	if err := <-errCh; err != nil && !errors.Is(err, context.Canceled) {
		t.Errorf("watcher.Start() returned unexpected error: %v", err)
	}

	if count := callCount.Load(); count != 1 {
		t.Errorf("onChange called %d times, want 1", count)
	}
}

func TestFileWatcher_IgnoreGeneratedFiles(t *testing.T) {
	tmpDir := t.TempDir()

	// Create initial file
	testFile := filepath.Join(tmpDir, "model.g.dart")
	if err := os.WriteFile(testFile, []byte("generated"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	var callCount atomic.Int32

	cfg := FileWatcherConfig{
		Dirs:     []string{tmpDir},
		Patterns: []string{".dart"},
		Ignore:   []string{".g.dart", ".freezed.dart"},
		Debounce: 10 * time.Millisecond,
		OnChange: func() {
			callCount.Add(1)
		},
	}

	watcher, err := NewFileWatcher(&cfg)
	if err != nil {
		t.Fatalf("failed to create watcher: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- watcher.Start(ctx)
	}()

	// Wait for watcher to start
	time.Sleep(50 * time.Millisecond)

	// Modify generated file
	if err := os.WriteFile(testFile, []byte("regenerated"), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	// Wait for potential callback
	time.Sleep(50 * time.Millisecond)

	watcher.Stop()

	// Check for unexpected errors
	if err := <-errCh; err != nil && !errors.Is(err, context.Canceled) {
		t.Errorf("watcher.Start() returned unexpected error: %v", err)
	}

	// Should not be called for ignored file
	if count := callCount.Load(); count != 0 {
		t.Errorf("onChange called %d times for ignored file, want 0", count)
	}
}

func TestFileWatcher_Stop(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := FileWatcherConfig{
		Dirs:     []string{tmpDir},
		Patterns: []string{".dart"},
		Debounce: 10 * time.Millisecond,
	}

	watcher, err := NewFileWatcher(&cfg)
	if err != nil {
		t.Fatalf("failed to create watcher: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- watcher.Start(ctx)
	}()

	// Wait for watcher to start
	time.Sleep(50 * time.Millisecond)

	// Stop via context cancel
	cancel()

	// Should exit cleanly
	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("watcher.Start() returned unexpected error: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Error("watcher did not stop after context cancel")
	}
}
