package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MobAI-App/ios-builder/internal/asc"
	"github.com/MobAI-App/ios-builder/internal/metadata"
)

// metadataFake serves one app with an editable version 2.0 in en-US and
// records the writes.
type metadataFake struct {
	mu     sync.Mutex
	writes []string
	bodies map[string]map[string]any
}

func newMetadataFake(t *testing.T) *metadataFake {
	t.Helper()
	f := &metadataFake{bodies: map[string]map[string]any{}}
	list := func(w http.ResponseWriter, rs ...map[string]any) {
		writeJSON(w, 200, map[string]any{"data": rs})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.Path
		if r.Method != http.MethodGet {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.writes = append(f.writes, key)
			f.bodies[key] = body
			f.mu.Unlock()
		}
		switch key {
		case "GET /v1/apps":
			list(w, map[string]any{"type": "apps", "id": "app-1", "attributes": map[string]any{"bundleId": "com.example.app", "name": "Example"}})
		case "GET /v1/apps/app-1/appStoreVersions":
			list(w,
				map[string]any{"type": "appStoreVersions", "id": "v-1", "attributes": map[string]any{"versionString": "1.0", "appVersionState": "READY_FOR_DISTRIBUTION", "createdDate": "2026-01-01T00:00:00Z"}},
				map[string]any{"type": "appStoreVersions", "id": "v-2", "attributes": map[string]any{"versionString": "2.0", "appVersionState": "PREPARE_FOR_SUBMISSION", "createdDate": "2026-09-01T00:00:00Z"}})
		case "GET /v1/apps/app-1/appInfos":
			list(w, map[string]any{"type": "appInfos", "id": "ai-1", "attributes": map[string]any{"state": "PREPARE_FOR_SUBMISSION"},
				"relationships": map[string]any{"primaryCategory": map[string]any{"data": map[string]any{"type": "appCategories", "id": "UTILITIES"}}}})
		case "GET /v1/appInfos/ai-1/appInfoLocalizations":
			list(w, map[string]any{"type": "appInfoLocalizations", "id": "il-1", "attributes": map[string]any{"locale": "en-US", "name": "Example", "subtitle": "Does things"}})
		case "GET /v1/appStoreVersions/v-2/appStoreVersionLocalizations":
			list(w, map[string]any{"type": "appStoreVersionLocalizations", "id": "vl-1", "attributes": map[string]any{"locale": "en-US", "description": "An app.", "keywords": "one,two"}})
		case "PATCH /v1/appStoreVersionLocalizations/vl-1":
			writeJSON(w, 200, map[string]any{"data": map[string]any{"type": "appStoreVersionLocalizations", "id": "vl-1"}})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	creds := asc.Credentials{IssuerID: "iss", KeyID: "kid", PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))}
	previous := getASCClient
	getASCClient = func() (*asc.Client, error) {
		return asc.NewClient(creds, asc.WithBaseURL(srv.URL), asc.WithRetryDelay(time.Millisecond))
	}
	t.Cleanup(func() { getASCClient = previous })
	return f
}

func (f *metadataFake) written() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.writes...)
}

func TestMetadataPullThenPush(t *testing.T) {
	f := newMetadataFake(t)
	t.Chdir(t.TempDir())

	stdout, stderr, err := run(t, "ios", "metadata", "pull", "--bundle-id", "com.example.app", "--json")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	var pulled metadata.PullResult
	if err := json.Unmarshal([]byte(stdout), &pulled); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
	}
	if pulled.Version.VersionString != "2.0" || len(pulled.Written) != 5 {
		t.Errorf("pull = %+v", pulled)
	}
	data, err := os.ReadFile(filepath.Join("metadata", "en-US", "keywords.txt"))
	if err != nil || string(data) != "one,two\n" {
		t.Errorf("keywords.txt = %q, %v", data, err)
	}

	if err := os.WriteFile(filepath.Join("metadata", "en-US", "keywords.txt"), []byte("one,two,three\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err = run(t, "ios", "metadata", "push", "--bundle-id", "com.example.app")
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("push without --yes: %v", err)
	}
	if !strings.Contains(stdout+stderr, `Will update en-US keywords: "one,two" → "one,two,three"`) {
		t.Errorf("plan not printed:\n%s%s", stdout, stderr)
	}
	stdout, _, err = run(t, "ios", "metadata", "push", "--bundle-id", "com.example.app", "--dry-run", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var plan metadata.Plan
	if err := json.Unmarshal([]byte(stdout), &plan); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
	}
	if len(plan.Changes) != 1 || plan.Changes[0].Field != "keywords" || plan.Applied {
		t.Errorf("plan = %+v", plan)
	}
	if w := f.written(); len(w) != 0 {
		t.Fatalf("dry run wrote: %v", w)
	}

	if _, stderr, err = run(t, "ios", "metadata", "push", "--bundle-id", "com.example.app", "--yes"); err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if w := f.written(); len(w) != 1 || w[0] != "PATCH /v1/appStoreVersionLocalizations/vl-1" {
		t.Errorf("writes = %v", w)
	}
	f.mu.Lock()
	body := f.bodies["PATCH /v1/appStoreVersionLocalizations/vl-1"]
	f.mu.Unlock()
	attrs := obj(t, body, "data", "attributes")
	if len(attrs) != 1 || attrs["keywords"] != "one,two,three" {
		t.Errorf("patch = %v", attrs)
	}
}

func TestMetadataPushValidatesLocally(t *testing.T) {
	f := newMetadataFake(t)
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(filepath.Join("meta", "en-US"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("meta", "en-US", "subtitle.txt"), []byte(strings.Repeat("s", 31)), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := run(t, "ios", "metadata", "push", "--bundle-id", "com.example.app", "--metadata-dir", "meta", "--yes")
	if err == nil || !strings.Contains(err.Error(), "en-US/subtitle.txt is 31 characters, the limit is 30") {
		t.Errorf("err = %v", err)
	}
	if w := f.written(); len(w) != 0 {
		t.Errorf("writes = %v", w)
	}
}
