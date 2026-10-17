package builds

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/MobAI-App/ios-builder/internal/build"
	"github.com/MobAI-App/ios-builder/internal/config"
	"github.com/MobAI-App/ios-builder/internal/github"
)

// The two workflow files Builder writes, and what each produces.
var githubWorkflows = []struct{ file, kind string }{
	{build.WorkflowFile, KindIPA},
	{"ios-share.yml", KindSimulator},
}

// GitHub reads Builder runs of ios-build.yml and ios-share.yml.
type GitHub struct {
	Client *github.Client
	Config *config.Config
	// Out receives download progress; nil discards it.
	Out io.Writer
}

func (*GitHub) Provider() string { return "github" }

func (g *GitHub) owner() string { return g.Config.GitHub.Owner }
func (g *GitHub) repo() string  { return g.Config.GitHub.Repo }

// runTitleIDRe takes the build ID off the end of a run-name: "iOS Build
// abcd1234" (dispatch) or "iOS Build ios-build/abcd1234" (tag push).
var runTitleIDRe = regexp.MustCompile(`(?:^|[\s/])([0-9a-f]{8})$`)

func (g *GitHub) build(run *github.WorkflowRun, kind string) Build {
	title := run.DisplayTitle
	if title == "" {
		title = run.Name
	}
	b := Build{Kind: kind, Provider: "github", RunID: strconv.FormatInt(run.ID, 10), URL: run.HTMLURL,
		CreatedAt: run.CreatedAt, StartedAt: timePtr(run.RunStartedAt)}
	if m := runTitleIDRe.FindStringSubmatch(strings.TrimSpace(title)); m != nil {
		b.ID = m[1]
	}
	switch run.Status {
	case "completed":
		b.State = run.Conclusion
		b.FinishedAt = timePtr(run.UpdatedAt)
		switch run.Conclusion {
		case "success":
			b.Status = StatusSucceeded
		case "cancelled", "skipped":
			b.Status = StatusCancelled
		default:
			b.Status = StatusFailed
		}
	case "queued", "waiting", "requested", "pending":
		b.State, b.Status = run.Status, StatusQueued
	default:
		b.State, b.Status = run.Status, StatusRunning
	}
	return b
}

// maxPages bounds a listing: 1000 runs per workflow file.
const maxPages = 10

func (g *GitHub) List(ctx context.Context, opts ListOptions) ([]Build, error) {
	limit := limitOf(opts)
	var all []Build
	for _, wf := range githubWorkflows {
		found := 0
		for page := 1; page <= maxPages && found < limit; page++ {
			runs, err := g.Client.ListWorkflowRunsPage(ctx, g.owner(), g.repo(), wf.file, min(100, max(limit, 20)), page)
			if err != nil {
				var apiErr *github.APIError
				// A repository without the share workflow has no simulator builds.
				if wf.kind == KindSimulator && errors.As(err, &apiErr) && apiErr.Status == "404" {
					break
				}
				return nil, err
			}
			for i := range runs {
				b := g.build(&runs[i], wf.kind)
				if matches(b.Status, opts.Status) {
					all = append(all, b)
					found++
				}
			}
			if len(runs) < min(100, max(limit, 20)) {
				break
			}
		}
	}
	sortBuilds(all)
	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// runURLRe matches https://github.com/<owner>/<repo>/actions/runs/<id>[/...].
var runURLRe = regexp.MustCompile(`^/([^/]+)/([^/]+)/actions/runs/(\d+)`)

func (g *GitHub) Find(ctx context.Context, ref string) (Build, error) {
	ref = strings.TrimSpace(ref)
	if strings.Contains(ref, "://") {
		u, err := url.Parse(ref)
		if err != nil {
			return Build{}, fmt.Errorf("not a run URL: %s", ref)
		}
		m := runURLRe.FindStringSubmatch(u.Path)
		if m == nil {
			return Build{}, fmt.Errorf("not a GitHub Actions run URL: %s", ref)
		}
		if !strings.EqualFold(m[1], g.owner()) || !strings.EqualFold(m[2], g.repo()) {
			return Build{}, fmt.Errorf("run %s belongs to %s/%s, not %s/%s from builder.json", m[3], m[1], m[2], g.owner(), g.repo())
		}
		return g.byRunID(ctx, m[3])
	}
	if IsBuildID(ref) {
		b, err := g.byBuildID(ctx, ref)
		if err == nil {
			return b, nil
		}
		if _, numErr := strconv.ParseInt(ref, 10, 64); numErr != nil {
			return Build{}, err
		}
	}
	if _, err := strconv.ParseInt(ref, 10, 64); err != nil {
		return Build{}, notFound("GitHub", ref)
	}
	return g.byRunID(ctx, ref)
}

// byBuildID searches the newest runs of both workflows for the build ID.
func (g *GitHub) byBuildID(ctx context.Context, id string) (Build, error) {
	for _, wf := range githubWorkflows {
		for page := 1; page <= 3; page++ {
			runs, err := g.Client.ListWorkflowRunsPage(ctx, g.owner(), g.repo(), wf.file, 100, page)
			if err != nil {
				var apiErr *github.APIError
				if wf.kind == KindSimulator && errors.As(err, &apiErr) && apiErr.Status == "404" {
					break
				}
				return Build{}, err
			}
			for i := range runs {
				if b := g.build(&runs[i], wf.kind); b.ID == id {
					return b, nil
				}
			}
			if len(runs) < 100 {
				break
			}
		}
	}
	return Build{}, notFound("GitHub", id)
}

// byRunID reads one run. The run object does not name its workflow file, so
// the kind comes from the run name.
func (g *GitHub) byRunID(ctx context.Context, id string) (Build, error) {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return Build{}, notFound("GitHub", id)
	}
	run, err := g.Client.GetWorkflowRun(ctx, g.owner(), g.repo(), n)
	if err != nil {
		return Build{}, err
	}
	kind := KindIPA
	if strings.HasPrefix(run.Name, "iOS Simulator") || strings.HasPrefix(run.DisplayTitle, "iOS Simulator") {
		kind = KindSimulator
	}
	return g.build(run, kind), nil
}

func (g *GitHub) runID(b *Build) (int64, error) {
	n, err := strconv.ParseInt(b.RunID, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid GitHub run ID %q", b.RunID)
	}
	return n, nil
}

func stepStatus(status, conclusion string) string {
	if status == "completed" && conclusion != "" {
		return conclusion
	}
	return status
}

func (g *GitHub) Show(ctx context.Context, b *Build) (Detail, error) {
	d := Detail{Build: *b}
	id, err := g.runID(b)
	if err != nil {
		return d, err
	}
	jobs, err := g.Client.ListRunJobs(ctx, g.owner(), g.repo(), id)
	if err != nil {
		return d, err
	}
	for i := range jobs {
		j := &jobs[i]
		job := Job{Name: j.Name, Status: stepStatus(j.Status, j.Conclusion)}
		for _, s := range j.Steps {
			job.Steps = append(job.Steps, Step{Number: s.Number, Name: s.Name, Status: stepStatus(s.Status, s.Conclusion)})
		}
		d.Jobs = append(d.Jobs, job)
	}
	if b.Status == StatusFailed {
		// Best-effort, as for `ios build`: the conclusion is shown either way.
		if f, _ := g.Client.RunFailure(ctx, g.owner(), g.repo(), id); f != nil {
			d.Failure = &Failure{Job: f.Job, Step: f.Step, Messages: f.Messages}
		}
	}
	artifacts, err := g.Client.ListRunArtifacts(ctx, g.owner(), g.repo(), id)
	if err != nil {
		return d, err
	}
	for _, a := range artifacts {
		d.Artifacts = append(d.Artifacts, Artifact{Name: a.Name, Size: a.SizeInBytes, Expired: a.Expired, ExpiresAt: timePtr(a.ExpiresAt)})
	}
	return d, nil
}

func (g *GitHub) Download(ctx context.Context, b *Build, dir string) (string, int64, error) {
	if b.Kind == KindSimulator {
		return "", 0, fmt.Errorf("build %s is a simulator session (ios share); it has no IPA", b.label())
	}
	id, err := g.runID(b)
	if err != nil {
		return "", 0, err
	}
	artifacts, err := g.Client.ListRunArtifacts(ctx, g.owner(), g.repo(), id)
	if err != nil {
		return "", 0, err
	}
	var ipa *github.Artifact
	for i := range artifacts {
		if artifacts[i].Name == build.IPAArtifactName {
			ipa = &artifacts[i]
		}
	}
	switch {
	case ipa == nil && !b.Done():
		return "", 0, fmt.Errorf("build %s is still %s and has not uploaded its IPA yet", b.label(), b.Status)
	case ipa == nil:
		return "", 0, fmt.Errorf("build %s (%s) uploaded no IPA", b.label(), b.State)
	case ipa.Expired:
		return "", 0, fmt.Errorf("the IPA of build %s expired on %s; GitHub keeps artifacts for 7 days", b.label(), ipa.ExpiresAt.Format("2006-01-02"))
	}
	out := g.Out
	if out == nil {
		out = io.Discard
	}
	return build.NewCoordinatorWithOutput(g.Config, g.Client, out).DownloadArtifact(ctx, ipa.ID, b.fileName(), dir)
}

func (g *GitHub) Cancel(ctx context.Context, b *Build) error {
	id, err := g.runID(b)
	if err != nil {
		return err
	}
	return g.Client.CancelWorkflowRun(ctx, g.owner(), g.repo(), id)
}

func (g *GitHub) Logs(b *Build, failedOnly bool) LogStream {
	return &githubLogs{g: g, b: *b, failedOnly: failedOnly, printed: map[int64]bool{}, stepSeen: map[string]bool{}}
}

// githubLogs prints each job's log once the job completes (GitHub serves job
// logs only then) and, while a job runs, a line per step as it starts.
type githubLogs struct {
	g          *GitHub
	b          Build
	failedOnly bool
	printed    map[int64]bool
	stepSeen   map[string]bool
}

func (l *githubLogs) Read(ctx context.Context, w io.Writer) (bool, error) {
	id, err := l.g.runID(&l.b)
	if err != nil {
		return false, err
	}
	// The run's state is read before its jobs, so a run seen completed here
	// has every job's log available below.
	run, err := l.g.Client.GetWorkflowRun(ctx, l.g.owner(), l.g.repo(), id)
	if err != nil {
		return false, err
	}
	jobs, err := l.g.Client.ListRunJobs(ctx, l.g.owner(), l.g.repo(), id)
	if err != nil {
		return false, err
	}
	for i := range jobs {
		j := &jobs[i]
		if j.Status != "completed" {
			if l.failedOnly {
				continue
			}
			for _, s := range j.Steps {
				key := fmt.Sprintf("%d/%d", j.ID, s.Number)
				if s.Status == "queued" || s.Status == "pending" || l.stepSeen[key] {
					continue
				}
				l.stepSeen[key] = true
				fmt.Fprintf(w, "--- %s: step %d %s\n", j.Name, s.Number, s.Name)
			}
			continue
		}
		if l.printed[j.ID] {
			continue
		}
		l.printed[j.ID] = true
		if l.failedOnly && j.Conclusion != "failure" {
			continue
		}
		data, err := l.g.Client.JobLogs(ctx, l.g.owner(), l.g.repo(), j.ID)
		if err != nil {
			return false, fmt.Errorf("job %s: %w", j.Name, err)
		}
		fmt.Fprintf(w, "==> %s (%s)\n", j.Name, stepStatus(j.Status, j.Conclusion))
		_, _ = w.Write(data)
		if len(data) > 0 && data[len(data)-1] != '\n' {
			fmt.Fprintln(w)
		}
	}
	return run.Status == "completed", nil
}

func (b *Build) label() string {
	if b.ID != "" {
		return b.ID
	}
	return b.RunID
}

// fileName is the name part of the saved IPA, as `ios build` uses it.
func (b *Build) fileName() string {
	if b.ID != "" {
		return b.ID
	}
	return b.Provider + "-" + b.RunID
}
