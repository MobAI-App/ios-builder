package build

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/MobAI-App/ios-builder/internal/snapshot"
)

// ShareWorkflowFile is the workflow that builds for the simulator and then
// makes it usable from the MobAI app.
const ShareWorkflowFile = "ios-share.yml"

// ShareOptions configures a simulator session.
type ShareOptions struct {
	Provider string // Override the configured CI provider
	// Duration is how long the simulator stays available while unused. Using
	// it keeps it open past this.
	Duration time.Duration
	Remote   string // git remote to push the working-tree snapshot to
	Timeout  time.Duration
}

// ShareResult describes a started session.
type ShareResult struct {
	WorkflowURL   string
	RunID         int64
	ProviderRunID string // String ID used by Codemagic and Bitrise
	BuildID       string
	Submitted     bool // True when the provider accepted a session, before readiness is known
}

// shareStepName is the workflow step that publishes the simulator. Reaching it
// is what turns "building" into "go and use it".
const shareStepName = "Share the simulator"

// sharePublishGrace covers the gap between the share step starting and the
// device showing up in the app, which polls rather than being pushed to.
const sharePublishGrace = 30 * time.Second

// Share builds the working tree for the simulator and publishes it to the
// account's MobAI app, then returns while the job outlives the command.
func (c *Coordinator) Share(ctx context.Context, opts ShareOptions) (*ShareResult, error) {
	name, err := c.config.ProviderName(opts.Provider)
	if err != nil {
		return nil, err
	}
	if name != "github" || c.provider != nil {
		return c.shareRemote(ctx, opts)
	}
	if c.github == nil {
		return nil, fmt.Errorf("GitHub client is required")
	}
	if opts.Timeout == 0 {
		opts.Timeout = DefaultTimeout // building can take a while before sharing starts
	}
	if opts.Duration == 0 {
		opts.Duration = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	buildID := uuid.New().String()[:8]
	c.progress.Start(buildID)

	c.progress.Update(PhaseSnapshot, "Snapshotting working tree...")
	sha, err := snapshot.Create(ctx, fmt.Sprintf("ios-builder snapshot %s", buildID))
	if err != nil {
		c.progress.Error(PhaseSnapshot, err)
		return nil, fmt.Errorf("failed to snapshot working tree: %w", err)
	}
	ref := snapshot.Ref(buildID)
	if err := snapshot.Push(ctx, opts.Remote, sha, ref); err != nil {
		c.progress.Error(PhaseSnapshot, err)
		return nil, fmt.Errorf("failed to push snapshot: %w", err)
	}
	// The snapshot ref is left in place: the job checks it out and runs long
	// after this returns. It can be deleted once the job has finished.
	c.progress.Complete(PhaseSnapshot, fmt.Sprintf("Pushed %s", sha[:7]))

	c.progress.Update(PhaseTriggering, "Starting the simulator session...")
	inputs := map[string]string{
		"build_id":     buildID,
		"snapshot_ref": ref,
		"duration":     opts.Duration.String(),
	}
	if c.config.IOS.Path != "" {
		inputs["ios_path"] = c.config.IOS.Path
	}
	if c.config.IOS.Scheme != "" {
		inputs["scheme"] = c.config.IOS.Scheme
	}
	if c.config.Flutter.Version != "" {
		inputs["flutter_version"] = c.config.Flutter.Version
	}
	if c.config.KMP.JDKVersion != "" {
		inputs["jdk_version"] = c.config.KMP.JDKVersion
	}
	if err := c.github.TriggerWorkflow(ctx, c.config.GitHub.Owner, c.config.GitHub.Repo, ShareWorkflowFile, inputs); err != nil {
		c.progress.Error(PhaseTriggering, err)
		return nil, fmt.Errorf("failed to trigger workflow: %w", err)
	}
	c.progress.Complete(PhaseTriggering, "Session starting")

	c.progress.Update(PhaseWaitingStart, "Waiting for the runner...")
	run, err := c.github.PollForWorkflowStart(ctx, c.config.GitHub.Owner, c.config.GitHub.Repo, ShareWorkflowFile, buildID, 2*time.Minute)
	if err != nil {
		c.progress.Error(PhaseWaitingStart, err)
		return nil, fmt.Errorf("workflow failed to start: %w", err)
	}
	c.progress.Complete(PhaseWaitingStart, fmt.Sprintf("Running (run #%d)", run.ID))
	c.progress.SetWorkflowURL(run.HTMLURL)

	c.progress.Update(PhaseBuilding, "Building for the simulator...")
	if err := c.waitForShareStep(ctx, run.ID); err != nil {
		// Aborted before the sim was shared (Ctrl+C, timeout, or failed build):
		// cancel the run so no runner keeps building. A finished run 409s
		// harmlessly. Fresh context since the caller's was likely just cancelled.
		cancelCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if cerr := c.github.CancelWorkflowRun(cancelCtx, c.config.GitHub.Owner, c.config.GitHub.Repo, run.ID); cerr == nil {
			c.progress.Update(PhaseBuilding, "Cancelled the run")
		}
		c.progress.Error(PhaseBuilding, err)
		return nil, err
	}
	c.progress.Complete(PhaseBuilding, "Built")

	return &ShareResult{WorkflowURL: run.HTMLURL, RunID: run.ID}, nil
}

// waitForShareStep follows the run until the publish step starts. A run that
// ends before then failed to build; the workflow page says why.
func (c *Coordinator) waitForShareStep(ctx context.Context, runID int64) error {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for the simulator: %w", ctx.Err())
		case <-ticker.C:
		}

		step, total, err := c.github.RunningStep(ctx, c.config.GitHub.Owner, c.config.GitHub.Repo, runID)
		if err == nil && step != nil {
			c.progress.UpdateStep(step.Name, step.Number, total, time.Since(step.StartedAt))
			if step.Name == shareStepName {
				// Sim is publishing now; wait for it to appear before returning.
				// An interrupt here is "done", not a reason to cancel a live sim.
				select {
				case <-ctx.Done():
				case <-time.After(sharePublishGrace):
				}
				return nil
			}
			continue
		}

		// No step running: either between steps, or the run is over.
		run, rerr := c.github.GetWorkflowRun(ctx, c.config.GitHub.Owner, c.config.GitHub.Repo, runID)
		if rerr == nil && run.Status == "completed" {
			return fmt.Errorf("the build finished without sharing a simulator (%s)", run.Conclusion)
		}
	}
}
