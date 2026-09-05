// Package ci implements remote build services independently of GitHub source hosting.
package ci

import (
	"context"
	"io"
)

// Request contains non-secret build inputs. Workflows come from Branch; sources
// come from SNAPSHOT_REF and must match SNAPSHOT_SHA.
type Request struct {
	Workflow  string
	Variables map[string]string
}

type Run struct{ ID, URL string }
type Artifact struct {
	ID, Name, URL string
	Size          int64
}
type Status struct {
	State         string
	Done, Success bool
	Artifacts     []Artifact
}

// Provider never retries a dispatch: a lost response may have started a job.
type Provider interface {
	Name() string
	Start(context.Context, Request) (Run, error)
	Status(context.Context, Run) (Status, error)
	Download(context.Context, Run, Artifact, io.Writer) (int64, error)
	Cancel(context.Context, Run) error
}

// ArtifactLister separates artifact API availability from the run's terminal state.
// Providers whose status response includes artifacts need not implement it.
type ArtifactLister interface {
	Artifacts(context.Context, Run) ([]Artifact, error)
}
