package github

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// workflowStartPollInterval is the polling interval when waiting for workflow to start.
	workflowStartPollInterval = 2 * time.Second
)

// TriggerWorkflow triggers a workflow_dispatch event
func (c *Client) TriggerWorkflow(ctx context.Context, owner, repo, workflowFile string, inputs map[string]string) error {
	path := fmt.Sprintf("/repos/%s/%s/actions/workflows/%s/dispatches", owner, repo, workflowFile)

	// Get the default branch
	repository, err := c.GetRepository(ctx, owner, repo)
	if err != nil {
		return fmt.Errorf("failed to get repository: %w", err)
	}

	ref := repository.DefaultRef
	if ref == "" {
		ref = "main"
	}

	req := WorkflowDispatchRequest{
		Ref:    ref,
		Inputs: inputs,
	}

	resp, err := c.request(ctx, "POST", path, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to trigger workflow (status %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return nil
}

// CancelWorkflowRun requests cancellation of a run. Best-effort: a run that has
// already finished returns 409, which is treated as success (nothing to cancel).
func (c *Client) CancelWorkflowRun(ctx context.Context, owner, repo string, runID int64) error {
	path := fmt.Sprintf("/repos/%s/%s/actions/runs/%d/cancel", owner, repo, runID)

	resp, err := c.request(ctx, "POST", path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// 202 Accepted on success; 409 Conflict when the run already completed.
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusConflict {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to cancel run (status %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return nil
}

// GetWorkflowRun retrieves a specific workflow run
func (c *Client) GetWorkflowRun(ctx context.Context, owner, repo string, runID int64) (*WorkflowRun, error) {
	path := fmt.Sprintf("/repos/%s/%s/actions/runs/%d", owner, repo, runID)

	var run WorkflowRun
	if err := c.do(ctx, path, &run); err != nil {
		return nil, fmt.Errorf("failed to get workflow run: %w", err)
	}

	return &run, nil
}

// ListWorkflowRuns lists workflow runs for a workflow file
func (c *Client) ListWorkflowRuns(ctx context.Context, owner, repo, workflowFile string) ([]WorkflowRun, error) {
	path := fmt.Sprintf("/repos/%s/%s/actions/workflows/%s/runs?per_page=10", owner, repo, workflowFile)

	var resp WorkflowRunsResponse
	if err := c.do(ctx, path, &resp); err != nil {
		return nil, fmt.Errorf("failed to list workflow runs: %w", err)
	}

	return resp.WorkflowRuns, nil
}

// FindWorkflowRunByBuildID finds the run whose name carries the build ID. The
// workflow puts the ID in run-name so concurrent builds cannot pick up each
// other's runs.
func (c *Client) FindWorkflowRunByBuildID(ctx context.Context, owner, repo, workflowFile, buildID string) (*WorkflowRun, error) {
	runs, err := c.ListWorkflowRuns(ctx, owner, repo, workflowFile)
	if err != nil {
		return nil, err
	}

	for i := range runs {
		if strings.Contains(runs[i].Name, buildID) {
			return &runs[i], nil
		}
	}

	return nil, fmt.Errorf("no workflow run found for build %s", buildID)
}

// PollForWorkflowStart polls until the run for buildID appears
func (c *Client) PollForWorkflowStart(ctx context.Context, owner, repo, workflowFile, buildID string, timeout time.Duration) (*WorkflowRun, error) {
	deadline := time.Now().Add(timeout)

	for {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for workflow to start")
		}

		run, err := c.FindWorkflowRunByBuildID(ctx, owner, repo, workflowFile, buildID)
		if err == nil {
			return run, nil
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(workflowStartPollInterval):
		}
	}
}

// ListRunJobs lists the jobs and their steps for a workflow run
func (c *Client) ListRunJobs(ctx context.Context, owner, repo string, runID int64) ([]Job, error) {
	path := fmt.Sprintf("/repos/%s/%s/actions/runs/%d/jobs", owner, repo, runID)

	var resp JobsResponse
	if err := c.do(ctx, path, &resp); err != nil {
		return nil, fmt.Errorf("failed to list run jobs: %w", err)
	}

	return resp.Jobs, nil
}

// RunningStep returns the step currently executing in a run, and the total
// number of steps in its job. It returns nil when no step is running.
func (c *Client) RunningStep(ctx context.Context, owner, repo string, runID int64) (*JobStep, int, error) {
	jobs, err := c.ListRunJobs(ctx, owner, repo, runID)
	if err != nil {
		return nil, 0, err
	}

	for _, job := range jobs {
		for i := range job.Steps {
			if job.Steps[i].Status == "in_progress" {
				return &job.Steps[i], len(job.Steps), nil
			}
		}
	}

	return nil, 0, nil
}

// ListCheckRunAnnotations lists the annotations of a check run. A job's ID is
// its check run ID.
func (c *Client) ListCheckRunAnnotations(ctx context.Context, owner, repo string, checkRunID int64) ([]Annotation, error) {
	path := fmt.Sprintf("/repos/%s/%s/check-runs/%d/annotations?per_page=100", owner, repo, checkRunID)

	var annotations []Annotation
	if err := c.do(ctx, path, &annotations); err != nil {
		return nil, fmt.Errorf("failed to list annotations: %w", err)
	}

	return annotations, nil
}

// RunFailure is why a run failed: its first failed job and step, and the
// failure-level annotations of that job (the runner's ::error:: lines).
type RunFailure struct {
	Job      string
	Step     string
	Messages []string
}

// RunFailure reads the failed job, its failed step and its error annotations.
// It returns nil when no job failed.
func (c *Client) RunFailure(ctx context.Context, owner, repo string, runID int64) (*RunFailure, error) {
	jobs, err := c.ListRunJobs(ctx, owner, repo, runID)
	if err != nil {
		return nil, err
	}
	for _, job := range jobs {
		var step string
		for _, s := range job.Steps {
			if s.Conclusion == "failure" {
				step = s.Name
				break
			}
		}
		if step == "" && job.Conclusion != "failure" {
			continue
		}
		failure := &RunFailure{Job: job.Name, Step: step}
		annotations, err := c.ListCheckRunAnnotations(ctx, owner, repo, job.ID)
		if err != nil {
			return failure, err
		}
		for _, a := range annotations {
			if a.Level == "failure" && strings.TrimSpace(a.Message) != "" {
				failure.Messages = append(failure.Messages, strings.TrimSpace(a.Message))
			}
		}
		return failure, nil
	}
	return nil, nil
}

// RunFailedError is a run that completed without success, with what the
// failed job reported when it could be read.
type RunFailedError struct {
	Conclusion string
	Failure    *RunFailure
}

func (e *RunFailedError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "workflow failed with conclusion: %s", e.Conclusion)
	if e.Failure == nil {
		return b.String()
	}
	if e.Failure.Step != "" {
		fmt.Fprintf(&b, "\n   Failed step: %s (job %s)", e.Failure.Step, e.Failure.Job)
	} else {
		fmt.Fprintf(&b, "\n   Failed job: %s", e.Failure.Job)
	}
	for _, m := range e.Failure.Messages {
		fmt.Fprintf(&b, "\n   %s", strings.ReplaceAll(m, "\n", "\n   "))
	}
	return b.String()
}

// runFailed builds the error for a run that ended without success. Reading
// the failure details is best-effort: the conclusion is reported either way.
func (c *Client) runFailed(ctx context.Context, owner, repo string, runID int64, conclusion string) error {
	failure, _ := c.RunFailure(ctx, owner, repo, runID)
	return &RunFailedError{Conclusion: conclusion, Failure: failure}
}

// ListRunArtifacts lists all artifacts for a workflow run
func (c *Client) ListRunArtifacts(ctx context.Context, owner, repo string, runID int64) ([]Artifact, error) {
	path := fmt.Sprintf("/repos/%s/%s/actions/runs/%d/artifacts", owner, repo, runID)

	var resp ArtifactsResponse
	if err := c.do(ctx, path, &resp); err != nil {
		return nil, fmt.Errorf("failed to list artifacts: %w", err)
	}

	return resp.Artifacts, nil
}

// ProgressFunc is called during download with bytes downloaded and total size
type ProgressFunc func(downloaded, total int64)

// DownloadArtifactWithProgress downloads an artifact with progress reporting
func (c *Client) DownloadArtifactWithProgress(ctx context.Context, owner, repo string, artifactID int64, progress ProgressFunc) ([]byte, error) {
	path := fmt.Sprintf("/repos/%s/%s/actions/artifacts/%d/zip", owner, repo, artifactID)

	resp, err := c.request(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusFound {
		// Close initial response before following redirect
		resp.Body.Close()

		redirectURL := resp.Header.Get("Location")
		if redirectURL == "" {
			return nil, fmt.Errorf("artifact redirect missing Location header")
		}

		req, err := http.NewRequestWithContext(ctx, "GET", redirectURL, nil)
		if err != nil {
			return nil, err
		}

		resp, err = c.httpClient.Do(req)
		if err != nil {
			return nil, err
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to download artifact: status %d", resp.StatusCode)
	}

	total := resp.ContentLength

	// Read with progress tracking
	var data []byte
	if progress != nil && total > 0 {
		data = make([]byte, 0, total)
		buf := make([]byte, 32*1024) // 32KB buffer
		var downloaded int64
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				data = append(data, buf[:n]...)
				downloaded += int64(n)
				progress(downloaded, total)
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("failed to read artifact data: %w", err)
			}
		}
	} else {
		data, err = io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to read artifact data: %w", err)
		}
	}

	return data, nil
}

// FindArtifactByName finds an artifact by name in a workflow run
func (c *Client) FindArtifactByName(ctx context.Context, owner, repo string, runID int64, name string) (*Artifact, error) {
	artifacts, err := c.ListRunArtifacts(ctx, owner, repo, runID)
	if err != nil {
		return nil, err
	}

	for _, a := range artifacts {
		if a.Name == name {
			return &a, nil
		}
	}

	return nil, fmt.Errorf("artifact %q not found", name)
}

// artifactPollInterval is fixed (no backoff) to catch the artifact quickly
// after upload. A variable so tests can shorten it.
var artifactPollInterval = 5 * time.Second

// maxConsecutiveStatusErrors bounds how long a failing run-status check is
// tolerated while polling: GitHub's API returns the odd 5xx during a long
// wait, and a build that is still running must not be abandoned for one.
// With the 5s interval this is a minute of failed polls.
const maxConsecutiveStatusErrors = 12

// PollForArtifact polls until an artifact with the given name appears in a workflow run.
// This allows downloading the artifact as soon as it's uploaded, without waiting for the
// entire workflow to complete. onPoll, if non-nil, runs once per attempt.
func (c *Client) PollForArtifact(ctx context.Context, owner, repo string, runID int64, artifactName string, timeout time.Duration, onPoll func()) (*Artifact, error) {
	deadline := time.Now().Add(timeout)
	statusErrors := 0

	for {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for artifact %q", artifactName)
		}

		// Check if artifact is available
		artifact, err := c.FindArtifactByName(ctx, owner, repo, runID, artifactName)
		if err == nil {
			return artifact, nil
		}

		// Check if workflow failed (no point waiting for artifact)
		run, err := c.GetWorkflowRun(ctx, owner, repo, runID)
		if err != nil {
			return nil, fmt.Errorf("failed to check workflow status: %w", err)
		}
		if run.Status == "completed" && run.Conclusion != "success" {
			return nil, c.runFailed(ctx, owner, repo, runID, run.Conclusion)
		}

		if onPoll != nil {
			onPoll()
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(artifactPollInterval):
			// Fixed interval - no backoff to catch artifact quickly
		}
	}
}
