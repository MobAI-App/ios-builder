package build

import (
	"context"
	"fmt"

	"github.com/MobAI-App/ios-builder/internal/ci"
)

// DownloadArtifact saves the IPA inside a GitHub artifact as
// <outputDir>/<project>-<name>.ipa, the same file `ios build` writes, so a
// build whose command was interrupted can still be fetched. name is the
// build ID, or the run ID when the build ID is unknown.
func (c *Coordinator) DownloadArtifact(ctx context.Context, artifactID int64, name, outputDir string) (string, int64, error) {
	if c.github == nil {
		return "", 0, fmt.Errorf("GitHub client is required")
	}
	if outputDir == "" {
		outputDir = "dist"
	}
	path, size, err := c.downloadIPAArtifactByID(ctx, outputDir, artifactID, name)
	if err != nil {
		return "", 0, err
	}
	c.progress.Finish()
	return path, size, nil
}

// DownloadRemote saves the IPA of a finished Codemagic or Bitrise run under
// the same name `ios build` uses, after the same checks.
func DownloadRemote(ctx context.Context, p ci.Provider, runID, buildID, outputDir, project string) (string, int64, error) {
	if outputDir == "" {
		outputDir = "dist"
	}
	run := ci.Run{ID: runID}
	status, err := p.Status(ctx, run)
	if err != nil {
		return "", 0, err
	}
	if !status.Done {
		return "", 0, fmt.Errorf("the build is still running (%s); its IPA is not there yet", status.State)
	}
	if !status.Success {
		return "", 0, fmt.Errorf("the build ended without success (%s), so it has no IPA", status.State)
	}
	if lister, ok := p.(ci.ArtifactLister); ok {
		if status.Artifacts, err = lister.Artifacts(ctx, run); err != nil {
			return "", 0, err
		}
	}
	artifact, err := findIPA(status.Artifacts, buildID)
	if err != nil {
		return "", 0, err
	}
	name := buildID
	if name == "" {
		name = runID
	}
	return saveRemoteIPA(ctx, p, run, artifact, outputDir, project, name)
}
