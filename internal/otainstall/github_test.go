package otainstall

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MobAI-App/ios-builder/internal/github"
)

// fakeGitHub is enough of api.github.com for a distribute session: releases,
// assets, signed downloads and gists, remembering what exists.
type fakeGitHub struct {
	t   *testing.T
	srv *httptest.Server
	mu  sync.Mutex

	releases map[int64]map[string]any // id → body as created
	assets   map[int64][]byte
	gists    map[string]map[string]any
	nextID   int64
	log      []string
	// gistScope false answers POST /gists as GitHub does for a token without it.
	gistScope bool
	// gistDeleteFails answers every DELETE /gists/{id} with 500.
	gistDeleteFails bool
	// beforeUpload runs when the asset upload arrives; set, the upload then fails with 500.
	beforeUpload func()
	now          time.Time
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	f := &fakeGitHub{t: t, releases: map[int64]map[string]any{}, assets: map[int64][]byte{}, gists: map[string]map[string]any{}, nextID: 100, gistScope: true, now: time.Now()}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGitHub) client() *github.Client {
	return github.NewClientWithBaseURL("tok", f.srv.URL)
}

func (f *fakeGitHub) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer tok" && !strings.HasPrefix(r.URL.Path, "/signed/") {
		f.t.Errorf("no token on %s %s", r.Method, r.URL.Path)
	}
	f.log = append(f.log, r.Method+" "+r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	path := r.URL.Path
	switch {
	case r.Method == "POST" && path == "/repos/o/r/releases":
		var body struct {
			TagName string `json:"tag_name"`
			Draft   bool   `json:"draft"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !body.Draft || !strings.HasPrefix(body.TagName, TagPrefix) {
			f.t.Errorf("release request %+v", body)
		}
		id := f.id()
		f.releases[id] = map[string]any{"tag_name": body.TagName, "draft": body.Draft}
		w.WriteHeader(201)
		fmt.Fprintf(w, `{"id":%d,"tag_name":%q,"draft":true,"html_url":"https://github.com/o/r/releases/%d","upload_url":"%s/upload/%d/assets{?name,label}"}`, id, body.TagName, id, f.srv.URL, id)
	case r.Method == "GET" && path == "/repos/o/r/releases":
		var list []map[string]any
		for id, body := range f.releases {
			list = append(list, map[string]any{"id": id, "tag_name": body["tag_name"], "draft": body["draft"]})
		}
		_ = json.NewEncoder(w).Encode(list)
	case r.Method == "DELETE" && strings.HasPrefix(path, "/repos/o/r/releases/") && !strings.Contains(path, "assets"):
		var id int64
		_, _ = fmt.Sscanf(path, "/repos/o/r/releases/%d", &id)
		if _, ok := f.releases[id]; !ok {
			w.WriteHeader(404)
			fmt.Fprint(w, `{"message":"Not Found"}`)
			return
		}
		delete(f.releases, id)
		delete(f.assets, id)
		w.WriteHeader(204)
	case r.Method == "POST" && strings.HasPrefix(path, "/upload/"):
		var rel int64
		_, _ = fmt.Sscanf(path, "/upload/%d/assets", &rel)
		if f.beforeUpload != nil {
			f.beforeUpload()
			w.WriteHeader(500)
			fmt.Fprint(w, `{"message":"boom"}`)
			return
		}
		if r.Header.Get("Content-Type") != "application/octet-stream" || r.URL.Query().Get("name") == "" {
			f.t.Errorf("upload headers: %v %v", r.Header, r.URL.Query())
		}
		data, _ := io.ReadAll(r.Body)
		if int64(len(data)) != r.ContentLength {
			f.t.Errorf("upload Content-Length %d for %d bytes", r.ContentLength, len(data))
		}
		f.assets[rel] = data
		w.WriteHeader(201)
		fmt.Fprintf(w, `{"id":%d,"name":%q,"size":%d}`, rel*10, r.URL.Query().Get("name"), len(data))
	case r.Method == "GET" && strings.HasPrefix(path, "/repos/o/r/releases/assets/"):
		if r.Header.Get("Accept") != "application/octet-stream" {
			f.t.Errorf("mint Accept = %q", r.Header.Get("Accept"))
		}
		var asset int64
		_, _ = fmt.Sscanf(path, "/repos/o/r/releases/assets/%d", &asset)
		w.Header().Set("Location", fmt.Sprintf("%s/signed/%d?jwt=%s&mint=%d", f.srv.URL, asset, jwtWithExp(f.now.Add(300*time.Second).Unix()), len(f.log)))
		w.WriteHeader(302)
	case r.Method == "POST" && path == "/gists":
		if !f.gistScope {
			w.WriteHeader(404)
			fmt.Fprint(w, `{"message":"Not Found","status":"404"}`)
			return
		}
		var body struct {
			Description string                       `json:"description"`
			Public      bool                         `json:"public"`
			Files       map[string]map[string]string `json:"files"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Public || body.Description != GistMarker {
			f.t.Errorf("gist request %+v", body)
		}
		content := body.Files["manifest.plist"]["content"]
		id := fmt.Sprintf("gist%d", f.id())
		f.gists[id] = map[string]any{"description": GistMarker, "content": content}
		w.WriteHeader(201)
		fmt.Fprintf(w, `{"id":%q,"description":%q,"public":false,"html_url":"https://gist.github.com/o/%s","files":{"manifest.plist":{"raw_url":"https://gist.githubusercontent.com/o/%s/raw/sha/manifest.plist"}}}`, id, GistMarker, id, id)
	case r.Method == "GET" && path == "/gists":
		var list []map[string]any
		for id, g := range f.gists {
			list = append(list, map[string]any{"id": id, "description": g["description"], "files": map[string]any{"manifest.plist": map[string]any{"raw_url": "x"}}})
		}
		_ = json.NewEncoder(w).Encode(list)
	case r.Method == "DELETE" && strings.HasPrefix(path, "/gists/"):
		if f.gistDeleteFails {
			w.WriteHeader(500)
			fmt.Fprint(w, `{"message":"boom"}`)
			return
		}
		delete(f.gists, strings.TrimPrefix(path, "/gists/"))
		w.WriteHeader(204)
	default:
		f.t.Errorf("unexpected request %s %s", r.Method, path)
		w.WriteHeader(500)
	}
}

func (f *fakeGitHub) id() int64 {
	f.nextID++
	return f.nextID
}

func (f *fakeGitHub) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, l := range f.log {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

func testApp(t *testing.T) *App {
	t.Helper()
	path := writeIPA(t, map[string]string{
		"Payload/App.app/Info.plist":               appPlist,
		"Payload/App.app/embedded.mobileprovision": mobileprovision(twoDevices + taskAllow),
	})
	app, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func seedLeftovers(gh *fakeGitHub) {
	gh.releases[1] = map[string]any{"tag_name": TagPrefix + "old", "draft": true}
	gh.releases[2] = map[string]any{"tag_name": "v1.0.0", "draft": false}
	gh.gists["stale"] = map[string]any{"description": GistMarker}
	gh.gists["mine"] = map[string]any{"description": "my notes"}
}

func TestCleanupRemovesOnlyBuilderLeftovers(t *testing.T) {
	gh := newFakeGitHub(t)
	seedLeftovers(gh)
	n, err := NewGitHub(gh.client(), "o", "r").Cleanup(context.Background())
	if err != nil || n != 2 {
		t.Fatalf("Cleanup = %d, %v", n, err)
	}
	if _, ok := gh.releases[2]; !ok {
		t.Error("the real release was deleted")
	}
	if _, ok := gh.releases[1]; ok {
		t.Error("the stale draft survived")
	}
	if _, ok := gh.gists["mine"]; !ok {
		t.Error("the user's own gist was deleted")
	}
	if _, ok := gh.gists["stale"]; ok {
		t.Error("the stale manifest gist survived")
	}
}

// syncBuffer is a bytes.Buffer the session writes while the test reads.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// links counts the install links the session has printed so far. The tests
// wait on this rather than on the fake server's request log: the server
// records a request before it answers, so a session interrupted right then
// sees a canceled request and has nothing to leave behind.
func (s *syncBuffer) links() int {
	text := s.String()
	return strings.Count(text, "Install link (valid until") + strings.Count(text, `{"link":`)
}

// waitFor polls until cond holds or the test times out.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func decodeLinks(t *testing.T, ndjson string) []Links {
	t.Helper()
	var out []Links
	for _, line := range strings.Split(strings.TrimSpace(ndjson), "\n") {
		var l Links
		if err := json.Unmarshal([]byte(line), &l); err != nil {
			t.Fatalf("%q: %v", line, err)
		}
		out = append(out, l)
	}
	return out
}

func TestSessionJSONRefreshesOnExpiryAndCleansUp(t *testing.T) {
	gh := newFakeGitHub(t)
	var log, out syncBuffer
	var progressCalls int
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opts := &Options{App: testApp(t), Backend: NewGitHub(gh.client(), "o", "r"), Log: &log, JSON: &out, Timeout: time.Minute,
		Progress: func(done, total int64) { progressCalls++ }, refreshLead: 299 * time.Second} // refresh one second after minting
	done := make(chan error, 1)
	go func() {
		_, err := Run(ctx, opts)
		done <- err
	}()
	waitFor(t, "the automatic refresh", func() bool { return out.links() >= 2 })
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}

	links := decodeLinks(t, out.String())
	if len(links) < 2 {
		t.Fatalf("want two NDJSON links, got %q", out.String())
	}
	first, second := links[0], links[1]
	if !strings.HasPrefix(first.Link, "itms-services://?action=download-manifest&url=https://gist.githubusercontent.com/o/") || !strings.HasSuffix(first.ManifestURL, "/raw") {
		t.Errorf("link = %q", first.Link)
	}
	var line struct {
		App *App `json:"app"`
	}
	if err := json.Unmarshal([]byte(strings.SplitN(out.String(), "\n", 2)[0]), &line); err != nil || line.App == nil || line.App.Profile.Devices != 2 || line.App.BundleID != "run.mobai.tapdash" {
		t.Errorf("JSON line lacks the app: %+v, %v", line.App, err)
	}
	if first.GistID == second.GistID || first.IPAURL == second.IPAURL {
		t.Error("refresh did not mint a new gist and IPA URL")
	}
	if first.ReleaseID == 0 || first.ExpiresAt.Before(time.Now()) || first.ManifestURL == "" {
		t.Errorf("first = %+v", first)
	}
	if progressCalls == 0 {
		t.Error("no upload progress")
	}
	gh.mu.Lock()
	defer gh.mu.Unlock()
	if len(gh.releases) != 0 || len(gh.gists) != 0 {
		t.Errorf("leftovers: releases %v gists %v", gh.releases, gh.gists)
	}
	if !strings.Contains(log.String(), "Removed the upload and the manifest.") || strings.Contains(log.String(), "Press Enter") {
		t.Errorf("log: %s", log.String())
	}
}

func TestSessionKeysRefreshAndQuit(t *testing.T) {
	gh := newFakeGitHub(t)
	var log syncBuffer
	stdin, keys := io.Pipe()
	opts := &Options{App: testApp(t), Backend: NewGitHub(gh.client(), "o", "r"), Log: &log, QR: true, Stdin: stdin, Timeout: time.Minute}
	done := make(chan error, 1)
	go func() {
		_, err := Run(context.Background(), opts)
		done <- err
	}()
	waitFor(t, "the first link", func() bool { return log.links() == 1 })
	if _, err := io.WriteString(keys, "\n"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the refresh", func() bool { return log.links() == 2 })
	if _, err := io.WriteString(keys, "q\n"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	text := log.String()
	if strings.Count(text, "Install link (valid until") != 2 || !strings.Contains(text, "Press Enter to refresh") {
		t.Errorf("log: %s", text)
	}
	if !strings.Contains(text, "█▀▀▀▀▀▀▀█") && !strings.Contains(text, "▄▄▄▄▄▄▄") {
		t.Errorf("no QR code in the log:\n%s", text)
	}
	if !strings.Contains(text, "profile: development, 2 device(s)") {
		t.Errorf("profile line missing: %s", text)
	}
	if gh.count("DELETE /gists/") != 2 || gh.count("DELETE /repos/o/r/releases/") != 1 {
		t.Errorf("cleanup calls: %v", gh.log)
	}
}

func TestSessionOnceLeavesUploadInPlace(t *testing.T) {
	gh := newFakeGitHub(t)
	var log bytes.Buffer
	opts := &Options{App: testApp(t), Backend: NewGitHub(gh.client(), "o", "r"), Log: &log, Once: true}
	res, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Leftovers) != 2 || !strings.Contains(res.Leftovers[0], "draft release") || !strings.HasPrefix(res.Leftovers[1], "gist ") {
		t.Errorf("leftovers = %v", res.Leftovers)
	}
	if len(gh.releases) != 1 || len(gh.gists) != 1 {
		t.Errorf("upload was removed: releases %v gists %v", gh.releases, gh.gists)
	}
	if !strings.Contains(log.String(), "--cleanup") {
		t.Errorf("log: %s", log.String())
	}
	// The gist holds the manifest that names the signed IPA URL.
	for _, g := range gh.gists {
		if content, _ := g["content"].(string); !strings.Contains(content, "software-package") || !strings.Contains(content, html.EscapeString(res.IPAURL)) {
			t.Errorf("manifest: %s", content)
		}
	}
}

func TestSessionWithoutGistScopeRemovesTheRelease(t *testing.T) {
	for _, once := range []bool{false, true} {
		gh := newFakeGitHub(t)
		gh.gistScope = false
		_, err := Run(context.Background(), &Options{App: testApp(t), Backend: NewGitHub(gh.client(), "o", "r"), Once: once})
		if err == nil || !strings.Contains(err.Error(), "builder auth github") {
			t.Fatalf("once %v: err = %v", once, err)
		}
		if len(gh.releases) != 0 {
			t.Errorf("once %v: draft release left behind: %v", once, gh.releases)
		}
	}
}

func TestSessionCanceledDuringUploadRemovesTheRelease(t *testing.T) {
	gh := newFakeGitHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gh.beforeUpload = cancel
	res, err := Run(ctx, &Options{App: testApp(t), Backend: NewGitHub(gh.client(), "o", "r")})
	if err != nil || res == nil || res.Link != "" {
		t.Fatalf("Run = %+v, %v", res, err)
	}
	if len(gh.releases) != 0 {
		t.Errorf("draft release left behind: %v", gh.releases)
	}
}

func TestSessionReportsWhatCleanupCouldNotRemove(t *testing.T) {
	gh := newFakeGitHub(t)
	gh.gistDeleteFails = true
	var log syncBuffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type outcome struct {
		res *Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := Run(ctx, &Options{App: testApp(t), Backend: NewGitHub(gh.client(), "o", "r"), Log: &log, Timeout: time.Minute})
		done <- outcome{res, err}
	}()
	waitFor(t, "the first link", func() bool { return log.links() == 1 })
	cancel()
	o := <-done
	if o.err != nil {
		t.Fatalf("Run: %v", o.err)
	}
	if res := o.res; len(res.Leftovers) != 1 || !strings.HasPrefix(res.Leftovers[0], "gist ") {
		t.Errorf("leftovers = %v", res.Leftovers)
	}
	if len(gh.releases) != 0 || len(gh.gists) != 1 {
		t.Errorf("releases %v gists %v", gh.releases, gh.gists)
	}
	if !strings.Contains(log.String(), "Cleanup failed") {
		t.Errorf("log: %s", log.String())
	}
}
