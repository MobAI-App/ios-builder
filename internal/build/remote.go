package build

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/MobAI-App/ios-builder/internal/auth"
	"github.com/MobAI-App/ios-builder/internal/ci"
	"github.com/MobAI-App/ios-builder/internal/config"
	"github.com/MobAI-App/ios-builder/internal/snapshot"
	"github.com/google/uuid"
)

// NewCoordinatorWithProvider allows callers to supply their own provider/token.
func NewCoordinatorWithProvider(cfg *config.Config, provider ci.Provider, w io.Writer) *Coordinator {
	return &Coordinator{config: cfg, provider: provider, progress: NewProgress(w)}
}

// RemoteProvider validates configuration and credentials before any snapshot is pushed.
func RemoteProvider(cfg *config.Config, override string) (ci.Provider, config.CIConfig, error) {
	name, err := cfg.ProviderName(override)
	if err != nil {
		return nil, config.CIConfig{}, err
	}
	if name == "github" {
		return nil, config.CIConfig{}, fmt.Errorf("choose --provider codemagic or bitrise for this command; GitHub runs can be cancelled from their Actions page")
	}
	cfgCI, err := cfg.ProviderConfig(name)
	if err != nil {
		return nil, cfgCI, err
	}
	key := strings.ToUpper(name) + "_API_TOKEN"
	token, err := auth.GetProviderToken(name)
	if err != nil {
		return nil, cfgCI, fmt.Errorf("not authenticated with %s; run builder auth %s or set %s", name, name, key)
	}
	switch name {
	case "codemagic":
		return ci.NewCodemagic(cfgCI, token), cfgCI, nil
	case "bitrise":
		return ci.NewBitrise(cfgCI, token), cfgCI, nil
	default:
		return nil, cfgCI, fmt.Errorf("%s is not an external CI provider", name)
	}
}

func (c *Coordinator) remote(override string) (ci.Provider, config.CIConfig, error) {
	if c.provider != nil {
		if override != "" && override != c.provider.Name() {
			return nil, config.CIConfig{}, fmt.Errorf("provider override does not match supplied provider")
		}
		cfg, err := c.config.ProviderConfig(c.provider.Name())
		return c.provider, cfg, err
	}
	return RemoteProvider(c.config, override)
}

func (c *Coordinator) inputs(buildID, ref, sha string) map[string]string {
	v := map[string]string{"BUILD_ID": buildID, "SNAPSHOT_REF": ref, "SNAPSHOT_SHA": sha,
		"IOS_PATH": c.config.IOS.Path, "SCHEME": c.config.IOS.Scheme,
		"CONFIGURATION": c.config.IOS.Configuration, "FLUTTER_VERSION": c.config.Flutter.Version,
		"JDK_VERSION": c.config.KMP.JDKVersion, "USE_SIGNING": "false",
		"BUILDER_REPOSITORY": c.config.GitHub.Owner + "/" + c.config.GitHub.Repo}
	if v["IOS_PATH"] == "" {
		v["IOS_PATH"] = "."
	}
	if v["CONFIGURATION"] == "" {
		v["CONFIGURATION"] = "Debug"
	}
	if v["JDK_VERSION"] == "" {
		v["JDK_VERSION"] = "17"
	}
	return v
}

func (c *Coordinator) pushSnapshot(ctx context.Context, remote, buildID string) (string, string, error) {
	c.progress.Start(buildID)
	c.progress.Update(PhaseSnapshot, "Snapshotting working tree...")
	sha, err := snapshot.Create(ctx, fmt.Sprintf("ios-builder snapshot %s", buildID))
	if err != nil {
		return "", "", err
	}
	ref := snapshot.Ref(buildID)
	if err := snapshot.Push(ctx, remote, sha, ref); err != nil {
		return "", "", err
	}
	c.progress.Complete(PhaseSnapshot, "Pushed "+sha[:7])
	return ref, sha, nil
}

func (c *Coordinator) buildRemote(ctx context.Context, opts BuildOptions) (*BuildResult, error) {
	p, cfgCI, err := c.remote(opts.Provider)
	if err != nil {
		return nil, err
	}
	if opts.Timeout < 0 {
		return nil, fmt.Errorf("timeout must be positive")
	}
	if opts.Timeout == 0 {
		opts.Timeout = DefaultTimeout
	}
	if opts.Remote == "" {
		opts.Remote = "origin"
	}
	if opts.OutputDir == "" {
		opts.OutputDir = "dist"
	}
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	started := time.Now()
	buildID := uuid.New().String()[:8]
	ref, sha, err := c.pushSnapshot(ctx, opts.Remote, buildID)
	if err != nil {
		return nil, err
	}
	v := c.inputs(buildID, ref, sha)
	if c.config.IOS.Signing && !opts.Unsigned {
		v["USE_SIGNING"] = "true"
	}
	c.progress.Update(PhaseTriggering, "Triggering "+p.Name()+" build...")
	run, err := p.Start(ctx, ci.Request{Workflow: cfgCI.BuildWorkflow, Variables: v})
	if err != nil {
		// A lost response can still mean the job was accepted. Retain its source
		// ref and never dispatch a second build behind the user's back.
		return nil, fmt.Errorf("%s dispatch: %w; snapshot %s retained; check the provider dashboard before retrying", p.Name(), err, ref)
	}
	c.progress.Complete(PhaseTriggering, "Run "+run.ID)
	c.progress.SetWorkflowURL(run.URL)
	terminal := false
	defer func() {
		if !terminal {
			cancelCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
			defer stop()
			if err := cancelAndWait(cancelCtx, p, run); err != nil {
				c.progress.Warn(fmt.Sprintf("could not confirm cancellation; run: %s; snapshot %s retained", run.URL, ref))
				return
			}
		}
		c.deleteSnapshot(ctx, opts.Remote, ref)
	}()
	status, err := waitForBuild(ctx, p, run, 10*time.Second, func(s ci.Status) { c.progress.Update(PhaseBuilding, s.State) })
	if err != nil {
		return nil, fmt.Errorf("%s build: %w (logs: %s)", p.Name(), err, run.URL)
	}
	terminal = true
	if !status.Success {
		return nil, fmt.Errorf("%s build ended: %s (logs: %s)", p.Name(), status.State, run.URL)
	}
	artifact, err := findIPA(status.Artifacts, buildID)
	if err != nil {
		return nil, fmt.Errorf("%w (logs: %s)", err, run.URL)
	}
	c.progress.Complete(PhaseBuilding, "Build completed")
	c.progress.Update(PhaseDownloading, "Downloading IPA...")
	path, size, err := saveRemoteIPA(ctx, p, run, artifact, opts.OutputDir, c.config.Project, buildID)
	if err != nil {
		return nil, err
	}
	c.progress.Complete(PhaseDownloading, fmt.Sprintf("IPA downloaded (%.2f MB)", float64(size)/(1024*1024)))
	c.progress.Finish()
	return &BuildResult{BuildID: buildID, IPAPath: path, IPASize: size, WorkflowURL: run.URL, Duration: time.Since(started)}, nil
}

func waitForBuild(ctx context.Context, p ci.Provider, run ci.Run, interval time.Duration, report func(ci.Status)) (ci.Status, error) {
	for {
		s, err := p.Status(ctx, run)
		if err != nil {
			return ci.Status{}, err
		}
		if report != nil {
			report(s)
		}
		if s.Done {
			return s, nil
		}
		select {
		case <-ctx.Done():
			return ci.Status{}, ctx.Err()
		case <-time.After(interval):
		}
	}
}

func cancelAndWait(ctx context.Context, p ci.Provider, run ci.Run) error {
	if err := p.Cancel(ctx, run); err != nil {
		return err
	}
	_, err := waitForBuild(ctx, p, run, 2*time.Second, nil)
	return err
}

func findIPA(artifacts []ci.Artifact, buildID string) (ci.Artifact, error) {
	var matches []ci.Artifact
	for _, a := range artifacts {
		if filepath.Ext(a.Name) != ".ipa" {
			continue
		}
		if filepath.Base(a.Name) == buildID+".ipa" {
			return a, nil
		}
		matches = append(matches, a)
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	return ci.Artifact{}, fmt.Errorf("successful build produced no unambiguous IPA artifact (found %d)", len(matches))
}

func saveRemoteIPA(ctx context.Context, p ci.Provider, run ci.Run, a ci.Artifact, dir, project, buildID string) (string, int64, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", 0, err
	}
	f, err := os.CreateTemp(dir, ".builder-*.ipa.part")
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = f.Close(); _ = os.Remove(f.Name()) }()
	n, err := p.Download(ctx, run, a, f)
	if err != nil {
		return "", 0, err
	}
	if a.Size > 0 && a.Size != n {
		return "", 0, fmt.Errorf("artifact size mismatch")
	}
	z, err := zip.NewReader(f, n)
	if err != nil {
		return "", 0, fmt.Errorf("downloaded artifact is not an IPA archive")
	}
	apps := 0
	for _, item := range z.File {
		parts := strings.Split(item.Name, "/")
		if len(parts) == 3 && parts[0] == "Payload" && strings.HasSuffix(parts[1], ".app") && parts[2] == "Info.plist" && !item.FileInfo().IsDir() {
			apps++
		}
	}
	if apps != 1 {
		return "", 0, fmt.Errorf("downloaded IPA must contain exactly one top-level application Info.plist")
	}
	if err := f.Close(); err != nil {
		return "", 0, err
	}
	project = strings.NewReplacer("/", "_", "\\", "_").Replace(project)
	dest := filepath.Join(dir, project+"-"+buildID+".ipa")
	if err := os.Rename(f.Name(), dest); err != nil {
		return "", 0, err
	}
	return dest, n, nil
}

func (c *Coordinator) shareRemote(ctx context.Context, opts ShareOptions) (*ShareResult, error) {
	p, cfgCI, err := c.remote(opts.Provider)
	if err != nil {
		return nil, err
	}
	if opts.Duration < 0 || opts.Timeout < 0 {
		return nil, fmt.Errorf("duration and timeout must be positive")
	}
	if opts.Duration == 0 {
		opts.Duration = 30 * time.Minute
	}
	if opts.Duration > 60*time.Minute {
		return nil, fmt.Errorf("%s idle duration cannot exceed 60m; the workflow has a 90-minute total limit including setup/build", p.Name())
	}
	if opts.Remote == "" {
		opts.Remote = "origin"
	}
	if opts.Timeout == 0 {
		opts.Timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	buildID := uuid.New().String()[:8]
	ref, sha, err := c.pushSnapshot(ctx, opts.Remote, buildID)
	if err != nil {
		return nil, err
	}
	v := c.inputs(buildID, ref, sha)
	v["DURATION"] = opts.Duration.String()
	run, err := p.Start(ctx, ci.Request{Workflow: cfgCI.ShareWorkflow, Variables: v})
	if err != nil {
		return nil, fmt.Errorf("%s dispatch: %w; snapshot %s retained; check the dashboard before retrying", p.Name(), err, ref)
	}
	if ctx.Err() != nil {
		cancelCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if err := cancelAndWait(cancelCtx, p, run); err == nil {
			c.deleteSnapshot(ctx, opts.Remote, ref)
		} else {
			c.progress.Warn(fmt.Sprintf("cancellation unconfirmed; run %s; snapshot %s retained", run.URL, ref))
		}
		return nil, ctx.Err()
	}
	// The APIs do not expose a common bridge-readiness signal. Return a submitted
	// session explicitly, leaving its ref available while queued. Never report
	// "ready" based only on a provider accepting a build.
	return &ShareResult{WorkflowURL: run.URL, ProviderRunID: run.ID, BuildID: buildID, Submitted: true}, nil
}

// CancelRemote cancels a submitted external-provider run and confirms termination.
func CancelRemote(ctx context.Context, p ci.Provider, runID string) error {
	if runID == "" {
		return fmt.Errorf("run ID is required")
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	return cancelAndWait(ctx, p, ci.Run{ID: runID})
}
