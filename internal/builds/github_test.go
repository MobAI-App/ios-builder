package builds

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MobAI-App/ios-builder/internal/config"
	"github.com/MobAI-App/ios-builder/internal/github"
)

// ipaBytes is a minimal IPA: one app bundle with an Info.plist.
func ipaBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	w, err := z.Create("Payload/App.app/Info.plist")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte("<plist/>"))
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// githubFake is a repository with three ios-build.yml runs (one succeeded by
// dispatch, one failed from a tag push, one running) and one ios-share.yml run.
type githubFake struct {
	t   *testing.T
	srv *httptest.Server
	mu  sync.Mutex
	// runningDone flips run 30 to completed (and its job's log available).
	runningDone bool
	cancelled   []string
	expired     bool
	calls       []string
}

func newGitHubFake(t *testing.T) *githubFake {
	f := &githubFake{t: t}
	artifactZip := func() []byte {
		var buf bytes.Buffer
		z := zip.NewWriter(&buf)
		w, _ := z.Create("abcd1234.ipa")
		_, _ = w.Write(ipaBytes(t))
		_ = z.Close()
		return buf.Bytes()
	}()
	run := func(id int, title, status, conclusion, created string) string {
		return fmt.Sprintf(`{"id":%d,"name":%q,"display_title":%q,"status":%q,"conclusion":%q,"html_url":"https://github.com/o/r/actions/runs/%d","created_at":%q,"run_started_at":%q,"updated_at":"2026-10-01T10:%s:00Z"}`,
			id, title, title, status, conclusion, id, created, created, "30")
	}
	mux := http.NewServeMux()
	handle := func(pattern string, h http.HandlerFunc) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			f.mu.Lock()
			f.calls = append(f.calls, r.Method+" "+r.URL.Path)
			f.mu.Unlock()
			if r.Header.Get("Authorization") != "Bearer tok" && !strings.HasPrefix(r.URL.Path, "/blob/") {
				t.Errorf("%s without the token", r.URL.Path)
			}
			h(w, r)
		})
	}
	running := func() string {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.runningDone {
			return run(30, "iOS Build 1234abcd", "completed", "success", "2026-10-01T10:20:00Z")
		}
		return run(30, "iOS Build 1234abcd", "in_progress", "", "2026-10-01T10:20:00Z")
	}
	handle("GET /repos/o/r/actions/workflows/ios-build.yml/runs", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") != "1" {
			fmt.Fprint(w, `{"total_count":3,"workflow_runs":[]}`)
			return
		}
		fmt.Fprintf(w, `{"total_count":3,"workflow_runs":[%s,%s,%s]}`, running(),
			run(20, "iOS Build ios-build/deadbeef", "completed", "failure", "2026-10-01T10:10:00Z"),
			run(10, "iOS Build abcd1234", "completed", "success", "2026-10-01T10:00:00Z"))
	})
	handle("GET /repos/o/r/actions/workflows/ios-share.yml/runs", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"total_count":1,"workflow_runs":[%s]}`, run(40, "iOS Simulator cafef00d", "completed", "cancelled", "2026-10-01T10:15:00Z"))
	})
	handle("GET /repos/o/r/actions/runs/{id}", func(w http.ResponseWriter, r *http.Request) {
		switch r.PathValue("id") {
		case "10":
			fmt.Fprint(w, run(10, "iOS Build abcd1234", "completed", "success", "2026-10-01T10:00:00Z"))
		case "20":
			fmt.Fprint(w, run(20, "iOS Build ios-build/deadbeef", "completed", "failure", "2026-10-01T10:10:00Z"))
		case "30":
			fmt.Fprint(w, running())
		case "40":
			fmt.Fprint(w, run(40, "iOS Simulator cafef00d", "completed", "cancelled", "2026-10-01T10:15:00Z"))
		default:
			w.WriteHeader(404)
			fmt.Fprint(w, `{"message":"Not Found"}`)
		}
	})
	handle("GET /repos/o/r/actions/runs/{id}/jobs", func(w http.ResponseWriter, r *http.Request) {
		switch r.PathValue("id") {
		case "20":
			fmt.Fprint(w, `{"total_count":2,"jobs":[
				{"id":201,"name":"prepare","status":"completed","conclusion":"success","steps":[{"name":"Checkout","status":"completed","conclusion":"success","number":1}]},
				{"id":202,"name":"build","status":"completed","conclusion":"failure","steps":[
					{"name":"Checkout","status":"completed","conclusion":"success","number":1},
					{"name":"Build IPA","status":"completed","conclusion":"failure","number":2}]}]}`)
		case "30":
			f.mu.Lock()
			done := f.runningDone
			f.mu.Unlock()
			if done {
				fmt.Fprint(w, `{"total_count":1,"jobs":[{"id":301,"name":"build","status":"completed","conclusion":"success","steps":[
					{"name":"Checkout","status":"completed","conclusion":"success","number":1},
					{"name":"Build IPA","status":"completed","conclusion":"success","number":2}]}]}`)
				return
			}
			fmt.Fprint(w, `{"total_count":1,"jobs":[{"id":301,"name":"build","status":"in_progress","steps":[
				{"name":"Checkout","status":"completed","conclusion":"success","number":1},
				{"name":"Build IPA","status":"in_progress","number":2},
				{"name":"Upload IPA","status":"queued","number":3}]}]}`)
		default:
			fmt.Fprint(w, `{"total_count":1,"jobs":[{"id":101,"name":"build","status":"completed","conclusion":"success","steps":[]}]}`)
		}
	})
	handle("GET /repos/o/r/check-runs/202/annotations", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"annotation_level":"failure","message":"No profile matching 'Builder store com.example.app' found"}]`)
	})
	handle("GET /repos/o/r/actions/runs/{id}/artifacts", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") != "10" {
			fmt.Fprint(w, `{"total_count":0,"artifacts":[]}`)
			return
		}
		f.mu.Lock()
		expired := f.expired
		f.mu.Unlock()
		fmt.Fprintf(w, `{"total_count":1,"artifacts":[{"id":555,"name":"ipa","size_in_bytes":%d,"expired":%t,"expires_at":"2026-10-08T10:00:00Z"}]}`, len(artifactZip), expired)
	})
	handle("GET /repos/o/r/actions/artifacts/555/zip", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(artifactZip)
	})
	handle("GET /repos/o/r/actions/jobs/{id}/logs", func(w http.ResponseWriter, r *http.Request) {
		// GitHub redirects to short-lived storage.
		http.Redirect(w, r, "/blob/log-"+r.PathValue("id"), http.StatusFound)
	})
	handle("GET /blob/{name}", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "log of %s\n", r.PathValue("name"))
	})
	handle("POST /repos/o/r/actions/runs/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.cancelled = append(f.cancelled, r.PathValue("id"))
		f.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *githubFake) source() *GitHub {
	cfg := &config.Config{Project: "App", GitHub: config.GitHubConfig{Owner: "o", Repo: "r"}}
	return &GitHub{Client: github.NewClientWithBaseURL("tok", f.srv.URL), Config: cfg}
}

func ids(bs []Build) string {
	var out []string
	for i := range bs {
		out = append(out, bs[i].ID+":"+bs[i].Kind+":"+bs[i].Status)
	}
	return strings.Join(out, " ")
}

func TestGitHubList(t *testing.T) {
	g := newGitHubFake(t).source()
	ctx := context.Background()
	all, err := g.List(ctx, ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := "1234abcd:ipa:running cafef00d:simulator:cancelled deadbeef:ipa:failed abcd1234:ipa:succeeded"
	if got := ids(all); got != want {
		t.Fatalf("list:\n got %s\nwant %s", got, want)
	}
	if all[3].RunID != "10" || all[3].URL != "https://github.com/o/r/actions/runs/10" || all[3].Duration(time.Now()) != 30*time.Minute {
		t.Fatalf("run fields: %+v", all[3])
	}
	for filter, want := range map[string]string{
		StatusRunning:   "1234abcd:ipa:running",
		StatusFailed:    "cafef00d:simulator:cancelled deadbeef:ipa:failed",
		StatusSucceeded: "abcd1234:ipa:succeeded",
	} {
		got, err := g.List(ctx, ListOptions{Status: filter})
		if err != nil || ids(got) != want {
			t.Errorf("--status %s: got %s (%v), want %s", filter, ids(got), err, want)
		}
	}
	limited, err := g.List(ctx, ListOptions{Limit: 2})
	if err != nil || ids(limited) != "1234abcd:ipa:running cafef00d:simulator:cancelled" {
		t.Fatalf("--limit 2: %s %v", ids(limited), err)
	}
}

func TestGitHubFind(t *testing.T) {
	g := newGitHubFake(t).source()
	ctx := context.Background()
	for ref, want := range map[string]string{
		"deadbeef": "20", // build ID from a tag-pushed run
		"cafef00d": "40", // simulator run
		"10":       "10", // run ID
		"https://github.com/o/r/actions/runs/20/job/202": "20",
	} {
		b, err := g.Find(ctx, ref)
		if err != nil || b.RunID != want {
			t.Errorf("Find(%q) = run %q, %v; want run %s", ref, b.RunID, err, want)
		}
	}
	if b, _ := g.Find(ctx, "40"); b.Kind != KindSimulator || b.ID != "cafef00d" {
		t.Errorf("run 40 by ID: %+v", b)
	}
	if _, err := g.Find(ctx, "0badf00d"); err == nil || !strings.Contains(err.Error(), "no GitHub build matches") {
		t.Errorf("unknown build ID: %v", err)
	}
	if _, err := g.Find(ctx, "https://github.com/other/repo/actions/runs/20"); err == nil || !strings.Contains(err.Error(), "other/repo") {
		t.Errorf("foreign repo URL: %v", err)
	}
}

func TestGitHubShowFailedRun(t *testing.T) {
	g := newGitHubFake(t).source()
	ctx := context.Background()
	b, err := g.Find(ctx, "deadbeef")
	if err != nil {
		t.Fatal(err)
	}
	d, err := g.Show(ctx, &b)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Jobs) != 2 || d.Jobs[1].Steps[1].Status != "failure" {
		t.Fatalf("jobs: %+v", d.Jobs)
	}
	if d.Failure == nil || d.Failure.Step != "Build IPA" || d.Failure.Job != "build" || len(d.Failure.Messages) != 1 {
		t.Fatalf("failure: %+v", d.Failure)
	}

	b, _ = g.Find(ctx, "abcd1234")
	d, err = g.Show(ctx, &b)
	if err != nil || d.Failure != nil || len(d.Artifacts) != 1 || d.Artifacts[0].Name != "ipa" || d.Artifacts[0].ExpiresAt == nil {
		t.Fatalf("succeeded run: %+v %v", d, err)
	}
}

func TestGitHubLogs(t *testing.T) {
	f := newGitHubFake(t)
	g := f.source()
	ctx := context.Background()

	b, _ := g.Find(ctx, "deadbeef")
	var out bytes.Buffer
	done, err := g.Logs(&b, false).Read(ctx, &out)
	if err != nil || !done || !strings.Contains(out.String(), "==> prepare") || !strings.Contains(out.String(), "log of log-202") {
		t.Fatalf("all logs: done=%v %v\n%s", done, err, out.String())
	}
	out.Reset()
	if _, err := g.Logs(&b, true).Read(ctx, &out); err != nil || strings.Contains(out.String(), "prepare") || !strings.Contains(out.String(), "log of log-202") {
		t.Fatalf("--failed must print only the failed job: %v\n%s", err, out.String())
	}

	// --follow on a running build: step lines first, the job log once it ends.
	b, _ = g.Find(ctx, "1234abcd")
	out.Reset()
	reads := 0
	stream := g.Logs(&b, false)
	wrapped := streamFunc(func(ctx context.Context, w *bytes.Buffer) (bool, error) {
		reads++
		if reads == 2 {
			f.mu.Lock()
			f.runningDone = true
			f.mu.Unlock()
		}
		return stream.Read(ctx, w)
	})
	for {
		done, err := wrapped(ctx, &out)
		if err != nil {
			t.Fatal(err)
		}
		if done {
			break
		}
	}
	got := out.String()
	if strings.Count(got, "step 2 Build IPA") != 1 || strings.Contains(got, "Upload IPA") || !strings.HasSuffix(got, "log of log-301\n") {
		t.Fatalf("follow output:\n%s", got)
	}
}

type streamFunc func(context.Context, *bytes.Buffer) (bool, error)

func TestFollowStopsWhenDone(t *testing.T) {
	f := newGitHubFake(t)
	g := f.source()
	b, _ := g.Find(context.Background(), "1234abcd")
	go func() {
		time.Sleep(30 * time.Millisecond)
		f.mu.Lock()
		f.runningDone = true
		f.mu.Unlock()
	}()
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := Follow(ctx, g.Logs(&b, false), &out, 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "log of log-301") {
		t.Fatalf("follow output:\n%s", out.String())
	}
}

func TestGitHubDownload(t *testing.T) {
	f := newGitHubFake(t)
	g := f.source()
	ctx := context.Background()
	dir := t.TempDir()

	b, _ := g.Find(ctx, "abcd1234")
	path, size, err := g.Download(ctx, &b, dir)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(dir, "App-abcd1234.ipa") || size != int64(len(ipaBytes(t))) {
		t.Fatalf("saved %s (%d bytes)", path, size)
	}
	if data, _ := os.ReadFile(path); !bytes.Equal(data, ipaBytes(t)) {
		t.Fatal("the saved IPA is not the artifact's")
	}

	f.mu.Lock()
	f.expired = true
	f.mu.Unlock()
	if _, _, err := g.Download(ctx, &b, dir); err == nil || !strings.Contains(err.Error(), "expired on 2026-10-08") {
		t.Errorf("expired artifact: %v", err)
	}
	b, _ = g.Find(ctx, "1234abcd")
	if _, _, err := g.Download(ctx, &b, dir); err == nil || !strings.Contains(err.Error(), "still running") {
		t.Errorf("running build: %v", err)
	}
	b, _ = g.Find(ctx, "cafef00d")
	if _, _, err := g.Download(ctx, &b, dir); err == nil || !strings.Contains(err.Error(), "simulator") {
		t.Errorf("simulator build: %v", err)
	}
}

func TestGitHubCancel(t *testing.T) {
	f := newGitHubFake(t)
	g := f.source()
	b, _ := g.Find(context.Background(), "1234abcd")
	if err := g.Cancel(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	if len(f.cancelled) != 1 || f.cancelled[0] != "30" {
		t.Fatalf("cancelled: %v", f.cancelled)
	}
}
