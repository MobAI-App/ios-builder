// Package workflow exposes the embedded GitHub Actions templates to code
// outside this module.
package workflow

import (
	"github.com/MobAI-App/ios-builder/internal/config"
	"github.com/MobAI-App/ios-builder/internal/workflow"
)

// GetTemplate returns the named embedded template.
func GetTemplate(name string) ([]byte, error) {
	return workflow.GetTemplate(name)
}

// GetWorkflowTemplate returns the iOS build workflow (ios-build.yml).
func GetWorkflowTemplate() ([]byte, error) {
	return workflow.GetWorkflowTemplate()
}

// GetShareWorkflowTemplate returns the simulator share workflow (ios-share.yml).
func GetShareWorkflowTemplate() ([]byte, error) {
	return workflow.GetShareWorkflowTemplate()
}

// RenderWorkflow returns ios-build.yml with runner as its default runs-on.
func RenderWorkflow(runner config.Runner) ([]byte, error) {
	return workflow.RenderWorkflow(runner)
}

// RenderShareWorkflow returns ios-share.yml running on runner.
func RenderShareWorkflow(runner config.Runner) ([]byte, error) {
	return workflow.RenderShareWorkflow(runner)
}
