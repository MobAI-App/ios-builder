package builds

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/MobAI-App/ios-builder/internal/build"
	"github.com/MobAI-App/ios-builder/internal/ci"
	"github.com/MobAI-App/ios-builder/internal/config"
)

// RemoteProvider is what a Codemagic or Bitrise source needs from its
// provider: the run API Builder builds with, plus the history reads.
type RemoteProvider interface {
	ci.Provider
	ci.History
}

// Remote reads Builder builds on Codemagic or Bitrise: builds of the
// configured build and share workflows, identified by the BUILD_ID variable
// Builder sent with them.
type Remote struct {
	P       RemoteProvider
	CI      config.CIConfig
	Project string
}

func (r *Remote) Provider() string { return r.P.Name() }

func (r *Remote) workflows() []struct{ name, kind string } {
	return []struct{ name, kind string }{{r.CI.BuildWorkflow, KindIPA}, {r.CI.ShareWorkflow, KindSimulator}}
}

func (r *Remote) kindOf(workflow string) string {
	if workflow != "" && workflow == r.CI.ShareWorkflow {
		return KindSimulator
	}
	return KindIPA
}

// ipaNameRe is the artifact name the runners give an IPA: <build-id>.ipa.
var ipaNameRe = regexp.MustCompile(`^([0-9a-f]{8})\.ipa(?:\.zip)?$`)

func (r *Remote) build(rec *ci.Record) Build {
	b := Build{Kind: r.kindOf(rec.Workflow), Provider: r.P.Name(), RunID: rec.ID, URL: rec.URL, State: rec.State,
		Profile: rec.Variables["BUILDER_PROFILE"], CreatedAt: rec.CreatedAt,
		StartedAt: timePtr(rec.StartedAt), FinishedAt: timePtr(rec.FinishedAt)}
	if id := rec.Variables["BUILD_ID"]; IsBuildID(id) {
		b.ID = id
	} else {
		// Without the variables in the response, the IPA's name still carries it.
		for _, a := range rec.Artifacts {
			if m := ipaNameRe.FindStringSubmatch(path.Base(a.Name)); m != nil {
				b.ID = m[1]
				break
			}
		}
	}
	switch {
	case rec.Success:
		b.Status = StatusSucceeded
	case rec.Cancelled:
		b.Status = StatusCancelled
	case rec.Done:
		b.Status = StatusFailed
	case rec.StartedAt.IsZero() && (rec.State == "queued" || rec.State == "initializing" || rec.State == "on-hold"):
		b.Status = StatusQueued
	default:
		b.Status = StatusRunning
	}
	if b.CreatedAt.IsZero() && b.StartedAt != nil {
		b.CreatedAt = *b.StartedAt
	}
	return b
}

func (r *Remote) records(ctx context.Context, limit int) ([]ci.Record, error) {
	var all []ci.Record
	for _, wf := range r.workflows() {
		if wf.name == "" {
			continue
		}
		recs, err := r.P.Builds(ctx, wf.name, limit)
		if err != nil {
			return nil, fmt.Errorf("%s builds of %s: %w", r.P.Name(), wf.name, err)
		}
		all = append(all, recs...)
	}
	return all, nil
}

func (r *Remote) List(ctx context.Context, opts ListOptions) ([]Build, error) {
	limit := limitOf(opts)
	fetch := limit
	if opts.Status != "" {
		// Filtering happens here, so read further back to fill the page.
		fetch = max(limit*3, 50)
	}
	recs, err := r.records(ctx, fetch)
	if err != nil {
		return nil, err
	}
	var out []Build
	for i := range recs {
		if b := r.build(&recs[i]); matches(b.Status, opts.Status) {
			out = append(out, b)
		}
	}
	sortBuilds(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// runIDFromURL takes the run ID off a Codemagic (…/build/<id>) or Bitrise
// (…/build/<slug>) URL.
func runIDFromURL(ref string) (string, bool) {
	u, err := url.Parse(ref)
	if err != nil || u.Host == "" {
		return "", false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i := len(parts) - 2; i >= 0; i-- {
		if parts[i] == "build" || parts[i] == "builds" {
			return parts[i+1], parts[i+1] != ""
		}
	}
	return "", false
}

func (r *Remote) Find(ctx context.Context, ref string) (Build, error) {
	ref = strings.TrimSpace(ref)
	if strings.Contains(ref, "://") {
		id, ok := runIDFromURL(ref)
		if !ok {
			return Build{}, fmt.Errorf("not a %s build URL: %s", r.P.Name(), ref)
		}
		ref = id
	} else if IsBuildID(ref) {
		recs, err := r.records(ctx, 50)
		if err != nil {
			return Build{}, err
		}
		var found []Build
		for i := range recs {
			if b := r.build(&recs[i]); b.ID == ref {
				found = append(found, b)
			}
		}
		if len(found) > 0 {
			sortBuilds(found)
			return found[0], nil
		}
		return Build{}, notFound(r.P.Name(), ref)
	}
	if ref == "" {
		return Build{}, notFound(r.P.Name(), ref)
	}
	rec, err := r.P.Build(ctx, ref)
	if err != nil {
		return Build{}, err
	}
	return r.build(&rec), nil
}

func (r *Remote) Show(ctx context.Context, b *Build) (Detail, error) {
	d := Detail{Build: *b}
	rec, err := r.P.Build(ctx, b.RunID)
	if err != nil {
		return d, err
	}
	d.Build = r.build(&rec)
	if d.ID == "" {
		d.ID = b.ID
	}
	if len(rec.Steps) > 0 {
		job := Job{Name: rec.Workflow, Status: rec.State}
		for i, s := range rec.Steps {
			job.Steps = append(job.Steps, Step{Number: i + 1, Name: s.Name, Status: s.State})
			if s.Failed && d.Failure == nil {
				d.Failure = &Failure{Job: rec.Workflow, Step: s.Name}
			}
		}
		d.Jobs = append(d.Jobs, job)
	}
	if d.Status == StatusFailed && d.Failure == nil {
		d.Failure = &Failure{Job: rec.Workflow}
	}
	if d.Failure != nil && rec.Reason != "" {
		d.Failure.Messages = append(d.Failure.Messages, rec.Reason)
	}
	artifacts := rec.Artifacts
	if lister, ok := r.P.(ci.ArtifactLister); ok && rec.Done {
		if artifacts, err = lister.Artifacts(ctx, ci.Run{ID: rec.ID, URL: rec.URL}); err != nil {
			return d, err
		}
	}
	for _, a := range artifacts {
		d.Artifacts = append(d.Artifacts, Artifact{Name: a.Name, Size: a.Size})
	}
	return d, nil
}

func (r *Remote) Download(ctx context.Context, b *Build, dir string) (string, int64, error) {
	if b.Kind == KindSimulator {
		return "", 0, fmt.Errorf("build %s is a simulator session (ios share); it has no IPA", b.label())
	}
	name := b.ID
	if name == "" {
		name = b.fileName()
	}
	return build.DownloadRemote(ctx, r.P, b.RunID, name, dir, r.Project)
}

func (r *Remote) Cancel(ctx context.Context, b *Build) error {
	return build.CancelRemote(ctx, r.P, b.RunID)
}

func (r *Remote) Logs(b *Build, failedOnly bool) LogStream {
	switch p := r.P.(type) {
	case *ci.Codemagic:
		return &codemagicLogs{p: p, b: *b, failedOnly: failedOnly, printed: map[int]bool{}}
	case *ci.Bitrise:
		return &bitriseLogs{p: p, b: *b, failedOnly: failedOnly, last: -1}
	}
	return unsupportedLogs{r.P.Name()}
}

type unsupportedLogs struct{ name string }

func (u unsupportedLogs) Read(context.Context, io.Writer) (bool, error) {
	return false, fmt.Errorf("%s does not expose build logs to Builder", u.name)
}

// codemagicLogs prints each step's log once the step has ended; Codemagic
// serves logs per step.
type codemagicLogs struct {
	p          *ci.Codemagic
	b          Build
	failedOnly bool
	printed    map[int]bool
}

func (l *codemagicLogs) Read(ctx context.Context, w io.Writer) (bool, error) {
	rec, err := l.p.Build(ctx, l.b.RunID)
	if err != nil {
		return false, err
	}
	for i := range rec.Steps {
		s := &rec.Steps[i]
		if l.printed[i] || (!s.Done && !rec.Done) {
			continue
		}
		l.printed[i] = true
		if (l.failedOnly && !s.Failed) || s.LogURL == "" {
			continue
		}
		fmt.Fprintf(w, "==> %s (%s)\n", s.Name, s.State)
		if err := l.p.StepLog(ctx, s, w); err != nil {
			return false, fmt.Errorf("step %s: %w", s.Name, err)
		}
		fmt.Fprintln(w)
	}
	return rec.Done, nil
}

// bitriseLogs prints new log chunks as Bitrise appends them. Once the log is
// archived and nothing was printed, the whole text comes from its raw URL.
// Bitrise has no per-step logs, so --failed prints the whole log of a failed
// build.
type bitriseLogs struct {
	p          *ci.Bitrise
	b          Build
	failedOnly bool
	last       int
}

func (l *bitriseLogs) Read(ctx context.Context, w io.Writer) (bool, error) {
	rec, err := l.p.Build(ctx, l.b.RunID)
	if err != nil {
		return false, err
	}
	if l.failedOnly && (!rec.Done || rec.Success) {
		return rec.Done, nil
	}
	log, err := l.p.Log(ctx, l.b.RunID)
	if err != nil {
		return false, err
	}
	for _, c := range log.Chunks {
		if c.Position <= l.last {
			continue
		}
		l.last = c.Position
		_, _ = io.WriteString(w, c.Text)
	}
	if rec.Done && log.Archived && l.last < 0 && log.RawURL != "" {
		l.last = 0
		if err := l.p.RawLog(ctx, log.RawURL, w); err != nil {
			return false, err
		}
	}
	// A finished build keeps writing chunks until its log is archived.
	return rec.Done && (log.Archived || len(log.Chunks) > 0), nil
}
