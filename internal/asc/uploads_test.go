package asc

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

// fakeASC is a minimal buildUploads backend: it hands out two chunk
// operations pointing back at itself, records the PUT bodies, and walks the
// upload state PROCESSING -> COMPLETE, after which the build appears.
type fakeASC struct {
	t        *testing.T
	mu       sync.Mutex
	srv      *httptest.Server
	fileSize int64
	chunks   map[int64][]byte
	headers  map[int64]http.Header
	created  map[string]any
	fileReq  map[string]any
	commit   map[string]any
	polls    int
	buildGet int
	patched  map[string]any
	failing  bool
	chunk500 int
}

func newFakeASC(t *testing.T, fileSize int64) *fakeASC {
	f := &fakeASC{t: t, fileSize: fileSize, chunks: map[int64][]byte{}, headers: map[int64]http.Header{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeASC) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == "POST" && r.URL.Path == "/v1/buildUploads":
		_ = json.NewDecoder(r.Body).Decode(&f.created)
		writeJSON(w, 201, map[string]any{"data": map[string]any{"type": "buildUploads", "id": "up-1", "attributes": map[string]any{
			"cfBundleShortVersionString": "1.2.3", "cfBundleVersion": "42", "platform": "IOS", "state": map[string]any{"state": "AWAITING_UPLOAD"},
		}}})
	case r.Method == "POST" && r.URL.Path == "/v1/buildUploadFiles":
		_ = json.NewDecoder(r.Body).Decode(&f.fileReq)
		half := f.fileSize / 2
		ops := []map[string]any{
			{"method": "PUT", "url": f.srv.URL + "/chunk?offset=0", "offset": 0, "length": half, "requestHeaders": []map[string]string{{"name": "Content-Type", "value": "application/octet-stream"}, {"name": "X-Chunk", "value": "first"}}},
			{"method": "PUT", "url": f.srv.URL + "/chunk?offset=" + strconv.FormatInt(half, 10), "offset": half, "length": f.fileSize - half, "requestHeaders": []map[string]string{{"name": "X-Chunk", "value": "second"}}},
		}
		writeJSON(w, 201, map[string]any{"data": map[string]any{"type": "buildUploadFiles", "id": "file-1", "attributes": map[string]any{"fileName": "App.ipa", "fileSize": f.fileSize, "uploadOperations": ops}}})
	case r.Method == "PUT" && r.URL.Path == "/chunk":
		if r.Header.Get("Authorization") != "" {
			f.t.Error("bearer token leaked to storage URL")
		}
		if f.chunk500 > 0 {
			f.chunk500--
			w.WriteHeader(503)
			return
		}
		offset, _ := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
		data, _ := io.ReadAll(r.Body)
		if r.ContentLength != int64(len(data)) {
			f.t.Errorf("Content-Length %d for %d bytes", r.ContentLength, len(data))
		}
		f.chunks[offset] = data
		f.headers[offset] = r.Header.Clone()
		w.WriteHeader(200)
	case r.Method == "PATCH" && r.URL.Path == "/v1/buildUploadFiles/file-1":
		_ = json.NewDecoder(r.Body).Decode(&f.commit)
		writeJSON(w, 200, map[string]any{"data": map[string]any{"type": "buildUploadFiles", "id": "file-1"}})
	case r.Method == "GET" && r.URL.Path == "/v1/buildUploads/up-1":
		f.polls++
		state := map[string]any{"state": "PROCESSING"}
		if f.polls >= 3 {
			state["state"] = "COMPLETE"
			if f.failing {
				state = map[string]any{"state": "FAILED", "errors": []map[string]string{{"code": "ITMS-90189", "description": "Redundant Binary Upload."}}}
			}
		}
		writeJSON(w, 200, map[string]any{"data": map[string]any{"type": "buildUploads", "id": "up-1", "attributes": map[string]any{"cfBundleShortVersionString": "1.2.3", "cfBundleVersion": "42", "platform": "IOS", "state": state}}})
	case r.Method == "GET" && r.URL.Path == "/v1/builds":
		q := r.URL.Query()
		if q.Get("filter[preReleaseVersion.version]") != "1.2.3" || q.Get("filter[version]") != "42" || q.Get("filter[app]") != "app-1" || q.Get("limit") != "1" {
			f.t.Errorf("builds query = %v", q)
		}
		f.buildGet++
		if f.buildGet == 1 {
			writeJSON(w, 200, map[string]any{"data": []any{}})
			return
		}
		state := "PROCESSING"
		if f.buildGet >= 3 {
			state = "VALID"
		}
		writeJSON(w, 200, map[string]any{"data": []map[string]any{{"type": "builds", "id": "build-9", "attributes": map[string]any{"version": "42", "processingState": state, "uploadedDate": "2026-09-16T10:00:00Z"}}}})
	case r.Method == "PATCH" && r.URL.Path == "/v1/builds/build-9":
		_ = json.NewDecoder(r.Body).Decode(&f.patched)
		writeJSON(w, 200, map[string]any{"data": map[string]any{"type": "builds", "id": "build-9", "attributes": map[string]any{"version": "42", "processingState": "VALID", "usesNonExemptEncryption": false}}})
	default:
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL)
		w.WriteHeader(404)
	}
}

func writeRandomFile(t *testing.T, name string, size int) (string, []byte) {
	t.Helper()
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, data
}

func TestUploadBuildFlow(t *testing.T) {
	path, data := writeRandomFile(t, "App.ipa", 10_001)
	fake := newFakeASC(t, int64(len(data)))
	c := newTestClient(t, fake.srv)
	ctx := context.Background()

	var progress []int64
	upload, err := c.UploadBuild(ctx, UploadBuildOptions{AppID: "app-1", Version: "1.2.3", BuildNumber: "42", Path: path, Progress: func(sent, total int64) {
		progress = append(progress, sent)
		if total != int64(len(data)) {
			t.Errorf("total = %d", total)
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	if upload.ID != "up-1" || upload.State != UploadStateProcessing {
		t.Errorf("upload = %+v", upload)
	}

	fake.mu.Lock()
	created := fake.created["data"].(map[string]any)
	attrs := created["attributes"].(map[string]any)
	if attrs["cfBundleShortVersionString"] != "1.2.3" || attrs["cfBundleVersion"] != "42" || attrs["platform"] != "IOS" {
		t.Errorf("buildUploads attributes = %v", attrs)
	}
	if created["relationships"].(map[string]any)["app"].(map[string]any)["data"].(map[string]any)["id"] != "app-1" {
		t.Errorf("buildUploads relationships = %v", created["relationships"])
	}
	fileAttrs := fake.fileReq["data"].(map[string]any)["attributes"].(map[string]any)
	if fileAttrs["assetType"] != "ASSET" || fileAttrs["fileName"] != "App.ipa" || fileAttrs["fileSize"] != float64(len(data)) || fileAttrs["uti"] != "com.apple.ipa" {
		t.Errorf("buildUploadFiles attributes = %v", fileAttrs)
	}
	if fake.fileReq["data"].(map[string]any)["relationships"].(map[string]any)["buildUpload"].(map[string]any)["data"].(map[string]any)["id"] != "up-1" {
		t.Errorf("buildUploadFiles relationships = %v", fake.fileReq)
	}
	got := append(append([]byte{}, fake.chunks[0]...), fake.chunks[int64(len(data))/2]...)
	if !bytes.Equal(got, data) {
		t.Errorf("reassembled %d bytes differ from the %d-byte file", len(got), len(data))
	}
	if fake.headers[0].Get("X-Chunk") != "first" || fake.headers[0].Get("Content-Type") != "application/octet-stream" || fake.headers[int64(len(data))/2].Get("X-Chunk") != "second" {
		t.Errorf("request headers not honored: %v %v", fake.headers[0], fake.headers[int64(len(data))/2])
	}
	commit := fake.commit["data"].(map[string]any)
	if commit["id"] != "file-1" || commit["attributes"].(map[string]any)["uploaded"] != true {
		t.Errorf("commit body = %v", fake.commit)
	}
	if _, has := commit["attributes"].(map[string]any)["sourceFileChecksums"]; has {
		t.Error("checksum must not be sent")
	}
	fake.mu.Unlock()
	if len(progress) != 2 || progress[1] != int64(len(data)) {
		t.Errorf("progress = %v", progress)
	}

	var states []string
	done, err := c.WaitForBuildUpload(ctx, upload.ID, time.Millisecond, func(u *BuildUpload) { states = append(states, u.State) })
	// UploadBuild already fetched the delivery once, so the wait sees the
	// remaining PROCESSING poll and then COMPLETE.
	if err != nil || done.State != UploadStateComplete || len(states) != 2 {
		t.Errorf("wait: %+v %v %v", done, err, states)
	}

	var seen int
	build, err := c.WaitForBuild(ctx, "app-1", "1.2.3", "42", time.Millisecond, func(*Build) { seen++ })
	if err != nil || build.ID != "build-9" || build.ProcessingState != ProcessingStateValid || seen != 3 {
		t.Errorf("build = %+v, err = %v, polls = %d", build, err, seen)
	}
}

func TestUploadBuildRejected(t *testing.T) {
	path, data := writeRandomFile(t, "App.ipa", 64)
	fake := newFakeASC(t, int64(len(data)))
	fake.failing = true
	c := newTestClient(t, fake.srv)
	ctx := context.Background()
	upload, err := c.UploadBuild(ctx, UploadBuildOptions{AppID: "app-1", Version: "1.2.3", BuildNumber: "42", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.WaitForBuildUpload(ctx, upload.ID, time.Millisecond, nil)
	var failed *UploadFailedError
	if !errors.As(err, &failed) || failed.Upload.Errors[0].Code != "ITMS-90189" {
		t.Fatalf("err = %v", err)
	}
	if want := "ITMS-90189: Redundant Binary Upload."; !bytes.Contains([]byte(err.Error()), []byte(want)) {
		t.Errorf("message %q lacks %q", err.Error(), want)
	}
}

func TestUploadChunkRetriesOn5xx(t *testing.T) {
	path, data := writeRandomFile(t, "App.pkg", 100)
	fake := newFakeASC(t, int64(len(data)))
	fake.chunk500 = 2
	c := newTestClient(t, fake.srv)
	if _, err := c.UploadBuild(context.Background(), UploadBuildOptions{AppID: "app-1", Version: "1.0", BuildNumber: "1", Path: path}); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.fileReq["data"].(map[string]any)["attributes"].(map[string]any)["uti"] != "com.apple.pkg" {
		t.Error("pkg uti not detected")
	}
	if len(fake.chunks[0]) != 50 || len(fake.chunks[50]) != 50 {
		t.Errorf("chunks = %d/%d bytes", len(fake.chunks[0]), len(fake.chunks[50]))
	}
}

func TestWaitForBuildCanceled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"data": []any{}})
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := newTestClient(t, srv).WaitForBuild(ctx, "app-1", "1.0", "1", time.Millisecond, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v", err)
	}
}
