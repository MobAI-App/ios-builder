// Package workflow provides embedded GitHub Actions workflow templates
// for building iOS applications remotely.
package workflow

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"

	"github.com/MobAI-App/ios-builder/internal/config"
)

//go:embed templates/*
var templatesFS embed.FS

// GetTemplate returns the content of a template file
func GetTemplate(name string) ([]byte, error) {
	content, err := templatesFS.ReadFile("templates/" + name)
	if err != nil {
		return nil, fmt.Errorf("failed to read template %s: %w", name, err)
	}
	return content, nil
}

// GetWorkflowTemplate returns the iOS build workflow content
func GetWorkflowTemplate() ([]byte, error) {
	return GetTemplate("ios-build.yml")
}

// GetShareWorkflowTemplate returns the workflow that builds for the simulator
// and then makes that simulator usable from the MobAI app.
func GetShareWorkflowTemplate() ([]byte, error) {
	return GetTemplate("ios-share.yml")
}

// The runs-on lines of the templates, as embedded (default runner).
const (
	buildRunsOn = "    runs-on: ${{ fromJSON(inputs.profile || '{}').runner || 'macos-latest' }}\n"
	shareRunsOn = "    runs-on: macos-latest\n"
)

// RenderWorkflow returns ios-build.yml with runner as the default runs-on.
// A dispatch still takes the runner from the profile input when it carries
// one; a tag push, which has no inputs, always runs on this default.
func RenderWorkflow(runner config.Runner) ([]byte, error) {
	if err := runner.Validate(); err != nil {
		return nil, err
	}
	content, err := GetWorkflowTemplate()
	if err != nil {
		return nil, err
	}
	def := "'" + config.DefaultRunner + "'"
	switch len(runner) {
	case 0:
	case 1:
		def = "'" + runner[0] + "'"
	default:
		data, _ := json.Marshal([]string(runner))
		def = "fromJSON('" + string(data) + "')"
	}
	line := "    runs-on: ${{ fromJSON(inputs.profile || '{}').runner || " + def + " }}\n"
	return replaceOnce(content, buildRunsOn, line, "ios-build.yml")
}

// RenderShareWorkflow returns ios-share.yml running on runner. The share
// dispatch carries no profile, so the rendered runner is the only one.
func RenderShareWorkflow(runner config.Runner) ([]byte, error) {
	if err := runner.Validate(); err != nil {
		return nil, err
	}
	content, err := GetShareWorkflowTemplate()
	if err != nil {
		return nil, err
	}
	if len(runner) == 0 {
		return content, nil
	}
	var data []byte
	if len(runner) == 1 {
		data, _ = json.Marshal(runner[0])
	} else {
		data, _ = json.Marshal([]string(runner))
	}
	return replaceOnce(content, shareRunsOn, "    runs-on: "+string(data)+"\n", "ios-share.yml")
}

func replaceOnce(content []byte, old, new, name string) ([]byte, error) {
	if n := bytes.Count(content, []byte(old)); n != 1 {
		return nil, fmt.Errorf("%s: expected one runs-on line to render, found %d", name, n)
	}
	return bytes.Replace(content, []byte(old), []byte(new), 1), nil
}
