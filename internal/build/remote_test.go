package build

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MobAI-App/ios-builder/internal/ci"
	"github.com/MobAI-App/ios-builder/internal/config"
)

type fakeProvider struct {
	statuses     []ci.Status
	cancelCalled bool
	payload      []byte
	downloadErr  error
	startErr     error
	cancelErr    error
	request      ci.Request
	failPolling  bool
}

func (*fakeProvider) Name() string { return "codemagic" }
func (p *fakeProvider) Start(_ context.Context, req ci.Request) (ci.Run, error) {
	p.request = req
	return ci.Run{ID: "run", URL: "https://provider.example/run"}, p.startErr
}
func (p *fakeProvider) Status(ctx context.Context, _ ci.Run) (ci.Status, error) {
	if err := ctx.Err(); err != nil {
		return ci.Status{}, err
	}
	if p.failPolling && !p.cancelCalled {
		return ci.Status{}, errors.New("polling failed")
	}
	if len(p.statuses) == 0 {
		return ci.Status{State: "building"}, nil
	}
	s := p.statuses[0]
	if len(p.statuses) > 1 {
		p.statuses = p.statuses[1:]
	}
	return s, nil
}
func (p *fakeProvider) Cancel(ctx context.Context, _ ci.Run) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.cancelCalled = true
	return p.cancelErr
}

func TestRemoteSnapshotLifecycle(t *testing.T) {
	for _, tt := range []struct {
		name                  string
		startErr, cancelErr   error
		failPolling, retained bool
	}{
		{"success", nil, nil, false, false},
		{"transient poll recovers", nil, nil, false, false},
		{"artifact failure after completion", nil, nil, false, false},
		{"ambiguous dispatch", errors.New("lost response"), nil, false, true},
		{"rejected dispatch", &ci.APIError{StatusCode: 404}, nil, false, false},
		{"ambiguous server failure", &ci.APIError{StatusCode: 503}, nil, false, true},
		{"rejected share", &ci.APIError{StatusCode: 403}, nil, false, false},
		{"ambiguous share", &ci.APIError{StatusCode: 408}, nil, false, true},
		{"poll failure cancelled", nil, nil, true, false},
		{"unconfirmed cancellation", nil, errors.New("cancel failed"), true, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			repo := filepath.Join(dir, "repo")
			remote := filepath.Join(dir, "remote.git")
			git := func(wd string, args ...string) string {
				t.Helper()
				cmd := exec.Command("git", args...)
				cmd.Dir = wd
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %s %v", args, out, err)
				}
				return strings.TrimSpace(string(out))
			}
			git(dir, "init", "--bare", remote)
			git(dir, "init", repo)
			git(repo, "config", "user.name", "Builder Test")
			git(repo, "config", "user.email", "builder@example.com")
			if err := os.WriteFile(filepath.Join(repo, "app.txt"), []byte("source"), 0644); err != nil {
				t.Fatal(err)
			}
			git(repo, "add", ".")
			git(repo, "commit", "-m", "base")
			git(repo, "remote", "add", "origin", remote)
			cwd, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chdir(repo); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = os.Chdir(cwd) }()
			var buf bytes.Buffer
			z := zip.NewWriter(&buf)
			w, err := z.Create("Payload/App.app/Info.plist")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write([]byte("plist")); err != nil {
				t.Fatal(err)
			}
			if err := z.Close(); err != nil {
				t.Fatal(err)
			}
			p := &fakeProvider{startErr: tt.startErr, cancelErr: tt.cancelErr, failPolling: tt.failPolling, payload: buf.Bytes(), statuses: []ci.Status{{Done: true, Success: true, State: "finished", Artifacts: []ci.Artifact{{Name: "app.ipa"}}}}}
			cfg := &config.Config{Project: "App", GitHub: config.GitHubConfig{Owner: "owner", Repo: "repo"}, Codemagic: config.CIConfig{AppID: "app", Branch: "main"}}
			c := NewCoordinatorWithProvider(cfg, p, io.Discard)
			if tt.name == "artifact failure after completion" {
				c.provider = &failedArtifactProvider{p}
			}
			if tt.name == "transient poll recovers" {
				original := http.DefaultTransport
				defer func() { http.DefaultTransport = original }()
				polls := 0
				http.DefaultTransport = buildTransportFunc(func(r *http.Request) (*http.Response, error) {
					code, body := 200, ""
					switch {
					case r.Method == "POST" && r.URL.Path == "/builds":
						var req struct {
							Environment struct{ Variables map[string]string }
						}
						if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
							t.Fatal(err)
						}
						p.request.Variables = req.Environment.Variables
						body = `{"buildId":"run"}`
					case r.Method == "GET" && r.URL.Path == "/api/v3/builds/run":
						polls++
						if polls == 1 {
							code = 503
						} else {
							body = `{"data":{"status":"finished","artifacts":[{"name":"app.ipa","short_lived_download_url":"https://storage.example/app.ipa"}]}}`
						}
					case r.Method == "GET" && r.URL.Host == "storage.example":
						body = string(p.payload)
					default:
						t.Fatalf("unexpected request/cancellation: %s %s", r.Method, r.URL)
					}
					return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
				})
				c.provider = ci.NewCodemagic(cfg.Codemagic, "test-token")
			}

			var result *BuildResult
			if strings.HasSuffix(tt.name, "share") {
				_, err = c.Share(context.Background(), ShareOptions{})
			} else {
				result, err = c.Build(context.Background(), BuildOptions{OutputDir: filepath.Join(dir, "dist")})
			}
			if tt.name == "success" || tt.name == "transient poll recovers" {
				if err != nil || result == nil {
					t.Fatalf("build: %+v %v", result, err)
				}
			} else if err == nil {
				t.Fatal("expected failure")
			}
			ref := p.request.Variables["SNAPSHOT_REF"]
			sha := p.request.Variables["SNAPSHOT_SHA"]
			if ref == "" || len(sha) != 40 {
				t.Fatalf("missing snapshot identity: %v", p.request)
			}
			refs := git(dir, "--git-dir="+remote, "for-each-ref", "--format=%(refname)")
			if strings.Contains(refs, ref) != tt.retained {
				t.Fatalf("retained=%v refs=%s", tt.retained, refs)
			}
			if tt.name == "artifact failure after completion" && p.cancelCalled {
				t.Fatal("completed build was cancelled due to artifact error")
			}
			if tt.failPolling && !p.cancelCalled {
				t.Fatal("remote run was not cancelled")
			}
		})
	}
}
func (p *fakeProvider) Download(_ context.Context, _ ci.Run, _ ci.Artifact, w io.Writer) (int64, error) {
	n, err := w.Write(p.payload)
	if p.downloadErr != nil {
		return int64(n), p.downloadErr
	}
	return int64(n), err
}

func TestWaitForBuildAndCancellation(t *testing.T) {
	p := &fakeProvider{statuses: []ci.Status{{State: "queued"}, {State: "finished", Done: true, Success: true}}}
	s, err := waitForBuild(context.Background(), p, ci.Run{}, time.Millisecond, nil)
	if err != nil || !s.Success {
		t.Fatalf("%+v %v", s, err)
	}
	if err := CancelRemote(context.Background(), p, "run"); err != nil || !p.cancelCalled {
		t.Fatalf("cancel: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := waitForBuild(ctx, &fakeProvider{}, ci.Run{}, time.Millisecond, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("ignored cancellation: %v", err)
	}
}

func TestFindIPA(t *testing.T) {
	if _, err := findIPA(nil, "build"); err == nil {
		t.Fatal("accepted success without IPA")
	}
	if _, err := findIPA([]ci.Artifact{{Name: "a.ipa"}, {Name: "b.ipa"}}, "build"); err == nil {
		t.Fatal("selected ambiguous IPA")
	}
	got, err := findIPA([]ci.Artifact{{Name: "other.ipa"}, {Name: "build.ipa"}}, "build")
	if err != nil || got.Name != "build.ipa" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestSaveRemoteIPAAtomicAndValidated(t *testing.T) {
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	w, err := z.Create("Payload/MyApp.app/Info.plist")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("plist")); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name        string
		payload     []byte
		size        int64
		downloadErr error
		bad         bool
	}{
		{"valid", buf.Bytes(), int64(buf.Len()), nil, false},
		{"truncated", []byte("not an IPA"), 0, nil, true},
		{"size mismatch", buf.Bytes(), 10, nil, true},
		{"network failure", buf.Bytes(), 0, io.ErrUnexpectedEOF, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			p := &fakeProvider{payload: tt.payload, downloadErr: tt.downloadErr}
			path, _, err := saveRemoteIPA(context.Background(), p, ci.Run{}, ci.Artifact{Size: tt.size}, dir, "app", "id")
			if (err != nil) != tt.bad {
				t.Fatalf("%q %v", path, err)
			}
			files, _ := os.ReadDir(dir)
			if tt.bad && len(files) != 0 {
				t.Fatal("failed download left output files")
			}
			if !tt.bad && path != filepath.Join(dir, "app-id.ipa") {
				t.Fatal("wrong output path")
			}
		})
	}
}

type buildTransportFunc func(*http.Request) (*http.Response, error)

func (f buildTransportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type failedArtifactProvider struct{ *fakeProvider }

func (*failedArtifactProvider) Artifacts(context.Context, ci.Run) ([]ci.Artifact, error) {
	return nil, errors.New("artifact service unavailable")
}
