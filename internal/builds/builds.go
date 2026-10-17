// Package builds is the history of Builder builds across providers. GitHub
// Actions, Codemagic and Bitrise stay the source of truth; this package reads
// their runs and normalizes them into one shape keyed by the Builder build ID.
package builds

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Kinds of build.
const (
	KindIPA       = "ipa"       // ios build
	KindSimulator = "simulator" // ios share
)

// Normalized statuses.
const (
	StatusQueued    = "queued"
	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
)

// Build is one Builder build as any provider reports it.
type Build struct {
	ID         string     `json:"id,omitempty"` // Builder build ID (8 hex), empty when not recoverable
	Kind       string     `json:"kind"`
	Profile    string     `json:"profile,omitempty"`
	Status     string     `json:"status"`
	State      string     `json:"state"` // the provider's own status/conclusion
	Provider   string     `json:"provider"`
	RunID      string     `json:"run_id"`
	URL        string     `json:"url,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// Done reports whether the build has stopped.
func (b *Build) Done() bool {
	return b.Status == StatusSucceeded || b.Status == StatusFailed || b.Status == StatusCancelled
}

// Duration is how long the build has run: until it finished, or until now
// while it runs. Zero before it starts.
func (b *Build) Duration(now time.Time) time.Duration {
	start := b.CreatedAt
	if b.StartedAt != nil {
		start = *b.StartedAt
	}
	if start.IsZero() {
		return 0
	}
	end := now
	if b.FinishedAt != nil {
		end = *b.FinishedAt
	} else if b.Done() {
		return 0
	}
	if end.Before(start) {
		return 0
	}
	return end.Sub(start)
}

// MarshalJSON adds the duration in whole seconds. Marshal a *Build (or a
// slice element) so it applies.
func (b *Build) MarshalJSON() ([]byte, error) {
	type plain Build
	return json.Marshal(struct {
		plain
		DurationSeconds int64 `json:"duration_seconds"`
	}{plain(*b), int64(b.Duration(time.Now()).Seconds())})
}

// Job is a job of a run (GitHub) or the run itself (Codemagic, Bitrise).
type Job struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Steps  []Step `json:"steps,omitempty"`
}

// Step is a step of a job.
type Step struct {
	Number int    `json:"number,omitempty"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// Failure is why a build failed: the failed job and step and what the runner
// reported, as far as the provider tells.
type Failure struct {
	Job      string   `json:"job,omitempty"`
	Step     string   `json:"step,omitempty"`
	Messages []string `json:"messages,omitempty"`
}

// Artifact is a file a build uploaded.
type Artifact struct {
	Name      string     `json:"name"`
	Size      int64      `json:"size"`
	Expired   bool       `json:"expired,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// Detail is a build with its jobs, failure and artifacts.
type Detail struct {
	Build
	Jobs      []Job      `json:"jobs,omitempty"`
	Failure   *Failure   `json:"failure,omitempty"`
	Artifacts []Artifact `json:"artifacts"`
}

// MarshalJSON keeps the embedded Build's fields and duration at the top
// level. Marshal a *Detail so it applies.
func (d *Detail) MarshalJSON() ([]byte, error) {
	base, err := d.Build.MarshalJSON()
	if err != nil {
		return nil, err
	}
	var fields map[string]any
	if err := json.Unmarshal(base, &fields); err != nil {
		return nil, err
	}
	if len(d.Jobs) > 0 {
		fields["jobs"] = d.Jobs
	}
	if d.Failure != nil {
		fields["failure"] = d.Failure
	}
	artifacts := d.Artifacts
	if artifacts == nil {
		artifacts = []Artifact{}
	}
	fields["artifacts"] = artifacts
	return json.Marshal(fields)
}

// ListOptions narrow a listing.
type ListOptions struct {
	Limit  int    // newest first; 0 means 20
	Status string // "", running (includes queued), failed (includes cancelled), succeeded
}

// LogStream prints a build's log in pieces. Read writes what is new since the
// last call and reports whether the build has finished and everything was
// printed.
type LogStream interface {
	Read(ctx context.Context, w io.Writer) (done bool, err error)
}

// Source is one provider's view of Builder builds.
type Source interface {
	Provider() string
	List(ctx context.Context, opts ListOptions) ([]Build, error)
	// Find resolves a Builder build ID, a provider run ID or a run URL.
	Find(ctx context.Context, ref string) (Build, error)
	Show(ctx context.Context, b *Build) (Detail, error)
	Logs(b *Build, failedOnly bool) LogStream
	// Download saves the build's IPA into dir and returns its path and size.
	Download(ctx context.Context, b *Build, dir string) (string, int64, error)
	Cancel(ctx context.Context, b *Build) error
}

var buildIDRe = regexp.MustCompile(`^[0-9a-f]{8}$`)

// IsBuildID reports whether ref has the shape of a Builder build ID.
func IsBuildID(ref string) bool { return buildIDRe.MatchString(ref) }

// ValidStatus checks a --status value.
func ValidStatus(s string) error {
	switch s {
	case "", StatusRunning, StatusFailed, StatusSucceeded:
		return nil
	}
	return fmt.Errorf("unknown status %q: use running, failed or succeeded", s)
}

// matches applies a --status filter.
func matches(status, filter string) bool {
	switch filter {
	case "":
		return true
	case StatusRunning:
		return status == StatusRunning || status == StatusQueued
	case StatusFailed:
		return status == StatusFailed || status == StatusCancelled
	}
	return status == filter
}

// sortBuilds orders builds newest first.
func sortBuilds(bs []Build) {
	sort.SliceStable(bs, func(i, j int) bool { return bs[i].CreatedAt.After(bs[j].CreatedAt) })
}

func limitOf(opts ListOptions) int {
	if opts.Limit <= 0 {
		return 20
	}
	return opts.Limit
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// ProviderFromURL names the provider a run URL belongs to, or "" when ref is
// not one.
func ProviderFromURL(ref string) string {
	switch {
	case strings.Contains(ref, "github.com/") && strings.Contains(ref, "/actions/runs/"):
		return "github"
	case strings.Contains(ref, "codemagic.io/"):
		return "codemagic"
	case strings.Contains(ref, "bitrise.io/"):
		return "bitrise"
	}
	return ""
}

// Follow reads a log stream until the build ends, waiting interval between
// reads.
func Follow(ctx context.Context, s LogStream, w io.Writer, interval time.Duration) error {
	for {
		done, err := s.Read(ctx, w)
		if err != nil || done {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

func notFound(provider, ref string) error {
	return fmt.Errorf("no %s build matches %q; pass a Builder build ID from `builder builds`, a run ID or a run URL", provider, ref)
}
