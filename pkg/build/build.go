// Package build exposes remote iOS build coordination to code outside this module.
//
// Coordinator.Build runs the whole flow: snapshot the working tree, trigger the
// workflow, poll for the run, download the IPA. Every step after the snapshot
// uses the Actions API; callers wanting finer control can drive the snapshot
// and github packages directly.
package build

import (
	"io"

	"github.com/MobAI-App/ios-builder/internal/build"
	"github.com/MobAI-App/ios-builder/pkg/ci"
	"github.com/MobAI-App/ios-builder/pkg/config"
	"github.com/MobAI-App/ios-builder/pkg/github"
)

const (
	// DefaultTimeout is the default build timeout.
	DefaultTimeout = build.DefaultTimeout

	// WorkflowFile is the workflow that builds an IPA.
	WorkflowFile = build.WorkflowFile

	// ShareWorkflowFile is the workflow that publishes a simulator to MobAI.
	ShareWorkflowFile = build.ShareWorkflowFile

	// IPAArtifactName is the name of the IPA artifact uploaded by the workflow.
	IPAArtifactName = build.IPAArtifactName
)

type (
	Coordinator  = build.Coordinator
	BuildOptions = build.BuildOptions
	BuildResult  = build.BuildResult
	ShareOptions = build.ShareOptions
	ShareResult  = build.ShareResult
)

// NewCoordinator creates a build coordinator that reports progress on stdout.
func NewCoordinator(cfg *config.Config, gh *github.Client) *Coordinator {
	return build.NewCoordinator(cfg, gh)
}

// NewCoordinatorWithProvider supplies an external CI provider without changing
// existing GitHub constructors. The provider may carry an explicit API token.
func NewCoordinatorWithProvider(cfg *config.Config, provider ci.Provider, w io.Writer) *Coordinator {
	return build.NewCoordinatorWithProvider(cfg, provider, w)
}

// NewCoordinatorWithOutput creates a coordinator that renders progress to w.
// Pass io.Discard to silence it.
func NewCoordinatorWithOutput(cfg *config.Config, gh *github.Client, w io.Writer) *Coordinator {
	return build.NewCoordinatorWithOutput(cfg, gh, w)
}
