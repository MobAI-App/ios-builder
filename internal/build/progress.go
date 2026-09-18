package build

import (
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/MobAI-App/ios-builder/internal/config"
)

// Phase represents a build phase
type Phase string

const (
	PhaseSnapshot     Phase = "snapshot"
	PhaseTriggering   Phase = "trigger"
	PhaseWaitingStart Phase = "waiting"
	PhaseBuilding     Phase = "building"
	PhaseDownloading  Phase = "download"
)

// PhaseInfo contains information about a phase
type PhaseInfo struct {
	Name   string
	Icon   string
	Status string
}

var phaseInfos = map[Phase]PhaseInfo{
	PhaseSnapshot:     {Name: "Snapshot", Icon: "📦"},
	PhaseTriggering:   {Name: "Trigger", Icon: "🚀"},
	PhaseWaitingStart: {Name: "Start", Icon: "⏳"},
	PhaseBuilding:     {Name: "Build", Icon: "🔨"},
	PhaseDownloading:  {Name: "Download", Icon: "⬇️"},
}

// Progress tracks and displays build progress
type Progress struct {
	writer           io.Writer
	buildID          string
	startTime        time.Time
	currentPhase     Phase
	workflowURL      string
	lastDownloadPct  int
	lastDownloadTime time.Time
	mu               sync.Mutex
}

// NewProgress creates a new progress reporter
func NewProgress(w io.Writer) *Progress {
	return &Progress{
		writer: w,
	}
}

// Start begins progress tracking for a build
func (p *Progress) Start(buildID string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.buildID = buildID
	p.startTime = time.Now()

	fmt.Fprintf(p.writer, "\n")
	fmt.Fprintf(p.writer, "🏗️  Builder - Remote iOS Build\n")
	fmt.Fprintf(p.writer, "   Build ID:      %s\n", buildID)
}

// Settings prints what the job will run with, before anything is dispatched,
// so a wrong profile or flag is visible without opening the provider's logs.
// It completes the header that Start begins.
func (p *Progress) Settings(s *config.BuildSettings, provider string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	orDefault := func(v, d string) string {
		if v == "" {
			return d
		}
		return v
	}
	signing := "unsigned"
	switch {
	case s.Signing && s.Distribution != "":
		signing = fmt.Sprintf("signed (set %s)", s.SigningSet())
	case s.Signing:
		signing = "signed (unsuffixed IOS_* secrets)"
	}
	fmt.Fprintf(p.writer, "   Profile:       %s\n", orDefault(s.Profile, "(none)"))
	fmt.Fprintf(p.writer, "   Configuration: %s\n", orDefault(s.Configuration, "Debug"))
	fmt.Fprintf(p.writer, "   Scheme:        %s\n", orDefault(s.Scheme, "(auto-detected)"))
	fmt.Fprintf(p.writer, "   Signing:       %s\n", signing)
	fmt.Fprintf(p.writer, "   Provider:      %s\n", provider)
	if len(s.Env) > 0 {
		keys := slices.Sorted(maps.Keys(s.Env))
		fmt.Fprintf(p.writer, "   Env:           %s\n", strings.Join(keys, ", "))
	}
	if s.Distribution != "" {
		fmt.Fprintf(p.writer, "   Distribution:  %s\n", s.Distribution)
	}
	if names := s.Hooks.Names(); len(names) > 0 {
		fmt.Fprintf(p.writer, "   Hooks:         %s\n", strings.Join(names, ", "))
	}
	fmt.Fprintf(p.writer, "\n")
}

// Update updates the current phase with a message
func (p *Progress) Update(phase Phase, message string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.currentPhase = phase
	info := phaseInfos[phase]

	// Clear line with ANSI escape and print update
	fmt.Fprintf(p.writer, "\r\033[K%s  %s: %s", info.Icon, info.Name, message)
}

// Complete marks a phase as complete
func (p *Progress) Complete(phase Phase, message string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	info := phaseInfos[phase]
	elapsed := time.Since(p.startTime).Round(time.Second)

	// Clear line and print completion
	fmt.Fprintf(p.writer, "\r\033[K✅ %s: %s (%s)\n", info.Name, message, elapsed)
}

// Error marks a phase as failed
func (p *Progress) Error(phase Phase, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	info := phaseInfos[phase]
	elapsed := time.Since(p.startTime).Round(time.Second)

	fmt.Fprintf(p.writer, "\r\033[K❌ %s: Failed (%s)\n", info.Name, elapsed)
	fmt.Fprintf(p.writer, "   Error: %v\n", err)

	if p.workflowURL != "" {
		fmt.Fprintf(p.writer, "   Logs: %s\n", p.workflowURL)
	}
}

// UpdateStep displays the workflow step currently running on the runner
func (p *Progress) UpdateStep(name string, number, total int, elapsed time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()

	info := phaseInfos[PhaseBuilding]
	fmt.Fprintf(p.writer, "\r\033[K%s  %s: %s (%d/%d) · %s",
		info.Icon, info.Name, name, number, total, elapsed.Round(time.Second))
}

// Warn prints a non-fatal problem without interrupting the build
func (p *Progress) Warn(message string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	fmt.Fprintf(p.writer, "\r\033[K⚠️  %s\n", message)
}

// SetWorkflowURL sets the workflow URL for error messages
func (p *Progress) SetWorkflowURL(url string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.workflowURL = url
}

// UpdateDownloadProgress updates the download progress display
func (p *Progress) UpdateDownloadProgress(downloaded, total int64) {
	p.mu.Lock()
	defer p.mu.Unlock()

	percent := int(float64(downloaded) / float64(total) * 100)
	now := time.Now()

	// Throttle: only update on percentage change or every 500ms
	if percent == p.lastDownloadPct && now.Sub(p.lastDownloadTime) < 500*time.Millisecond {
		return
	}
	p.lastDownloadPct = percent
	p.lastDownloadTime = now

	info := phaseInfos[PhaseDownloading]
	downloadedMB := float64(downloaded) / (1024 * 1024)
	totalMB := float64(total) / (1024 * 1024)

	// Create progress bar
	barWidth := 20
	filled := percent * barWidth / 100
	bar := strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)

	fmt.Fprintf(p.writer, "\r\033[K%s  %s: [%s] %d%% (%.1f/%.1f MB)",
		info.Icon, info.Name, bar, percent, downloadedMB, totalMB)
}

// Finish completes progress tracking
func (p *Progress) Finish() {
	p.mu.Lock()
	defer p.mu.Unlock()

	elapsed := time.Since(p.startTime).Round(time.Second)

	fmt.Fprintf(p.writer, "\n")
	fmt.Fprintf(p.writer, "✨ Build complete! Total time: %s\n", elapsed)
	fmt.Fprintf(p.writer, "\n")
}
