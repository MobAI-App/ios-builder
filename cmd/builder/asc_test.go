package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MobAI-App/ios-builder/internal/asc"
	"github.com/MobAI-App/ios-builder/internal/distribute"
)

// ascFake is an in-memory App Store Connect covering what the command tests
// touch: one app, one processed build, two groups, one existing tester.
type ascFake struct {
	t      *testing.T
	mu     sync.Mutex
	calls  []string
	bodies map[string]map[string]any
}

func newASCFake(t *testing.T) *ascFake {
	t.Helper()
	f := &ascFake{t: t, bodies: map[string]map[string]any{}}
	res := func(typ, id string, attrs map[string]any) map[string]any {
		return map[string]any{"type": typ, "id": id, "attributes": attrs}
	}
	one := func(w http.ResponseWriter, status int, r any) { writeJSON(w, status, map[string]any{"data": r}) }
	many := func(w http.ResponseWriter, rs ...any) {
		if rs == nil {
			rs = []any{}
		}
		writeJSON(w, 200, map[string]any{"data": rs})
	}
	mux := http.NewServeMux()
	handle := func(pattern string, h func(w http.ResponseWriter, r *http.Request, body map[string]any)) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			data, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(data, &body)
			f.mu.Lock()
			defer f.mu.Unlock()
			key := r.Method + " " + r.URL.Path
			f.calls = append(f.calls, key)
			if body != nil {
				f.bodies[key] = body
			}
			h(w, r, body)
		})
	}
	handle("GET /v1/apps", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if r.URL.Query().Get("filter[bundleId]") != "com.example.app" {
			many(w)
			return
		}
		many(w, res("apps", "app-1", map[string]any{"bundleId": "com.example.app", "name": "Example", "primaryLocale": "en-US"}))
	})
	handle("GET /v1/builds", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		many(w, res("builds", "build-9", map[string]any{"version": "7", "processingState": "VALID", "uploadedDate": "2026-09-16T10:00:00Z", "expired": false, "usesNonExemptEncryption": false}))
	})
	handle("GET /v1/betaGroups", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		many(w, res("betaGroups", "g-int", map[string]any{"name": "Team", "isInternalGroup": true}), res("betaGroups", "g-ext", map[string]any{"name": "Beta Testers", "isInternalGroup": false}))
	})
	handle("POST /v1/betaGroups", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		one(w, 201, res("betaGroups", "g-new", obj(t, body, "data", "attributes")))
	})
	handle("POST /v1/builds/{id}/relationships/betaGroups", func(w http.ResponseWriter, r *http.Request, _ map[string]any) { w.WriteHeader(204) })
	handle("POST /v1/betaGroups/{id}/relationships/betaTesters", func(w http.ResponseWriter, r *http.Request, _ map[string]any) { w.WriteHeader(204) })
	handle("GET /v1/betaTesters", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if r.URL.Query().Get("filter[email]") == "old@example.com" {
			many(w, res("betaTesters", "t-old", map[string]any{"email": "old@example.com", "state": "ACCEPTED"}))
			return
		}
		many(w)
	})
	handle("POST /v1/betaTesters", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		attrs := obj(t, body, "data", "attributes")
		if attrs["email"] == "old@example.com" {
			writeJSON(w, 409, map[string]any{"errors": []map[string]any{{"status": "409", "code": "ENTITY_ERROR.ATTRIBUTE.INVALID.DUPLICATE", "title": "duplicate"}}})
			return
		}
		one(w, 201, res("betaTesters", "t-new", map[string]any{"email": attrs["email"], "state": "INVITED"}))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s", r.Method, r.URL)
		w.WriteHeader(404)
	})
	srv := httptest.NewServer(mux)
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

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// obj walks decoded JSON down the given object keys; a missing or non-object
// step fails the test and yields nil, which later lookups tolerate.
func obj(t *testing.T, v any, keys ...string) map[string]any {
	t.Helper()
	for i := 0; ; i++ {
		m, ok := v.(map[string]any)
		if !ok {
			t.Errorf("JSON path %v: %T is not an object", keys[:i], v)
			return nil
		}
		if i == len(keys) {
			return m
		}
		v = m[keys[i]]
	}
}

// arr is obj for a final array value.
func arr(t *testing.T, v any, keys ...string) []any {
	t.Helper()
	a, ok := obj(t, v, keys[:len(keys)-1]...)[keys[len(keys)-1]].([]any)
	if !ok {
		t.Errorf("JSON path %v is not an array", keys)
	}
	return a
}

func (f *ascFake) body(key string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bodies[key]
}

func (f *ascFake) called(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == key {
			return true
		}
	}
	return false
}

// run executes a builder command line and returns stdout and stderr.
func run(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs(args)
	err = rootCmd.Execute()
	return out.String(), errOut.String(), err
}

func TestSubmitCreatesMissingGroup(t *testing.T) {
	f := newASCFake(t)
	stdout, stderr, err := run(t, "ios", "submit", "--testflight", "--group", "Nightly", "--bundle-id", "com.example.app", "--json")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	var res distribute.TestFlightResult
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("stdout is not the JSON result: %v\n%s", err, stdout)
	}
	if len(res.Groups) != 1 || res.Groups[0].Name != "Nightly" || !res.Groups[0].Created || !res.Groups[0].Internal || res.Groups[0].ID != "g-new" {
		t.Errorf("groups = %+v", res.Groups)
	}
	attrs := obj(t, f.body("POST /v1/betaGroups"), "data", "attributes")
	if attrs["name"] != "Nightly" || attrs["isInternalGroup"] != true {
		t.Errorf("create body = %v", attrs)
	}
	if !f.called("POST /v1/builds/build-9/relationships/betaGroups") {
		t.Errorf("build not added: %v", f.calls)
	}
	if !strings.Contains(stderr, "Created TestFlight group Nightly (internal)") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestASCTestersAddExistingTester(t *testing.T) {
	f := newASCFake(t)
	stdout, stderr, err := run(t, "asc", "testers", "add", "old@example.com", "new@example.com", "--group", "beta testers", "--bundle-id", "com.example.app", "--json")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	var rows []distribute.TesterResult
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatalf("stdout is not a JSON array: %v\n%s", err, stdout)
	}
	if len(rows) != 2 || rows[0].Status != distribute.TesterAdded || rows[0].ID != "t-old" || rows[1].Status != distribute.TesterInvited || rows[1].ID != "t-new" || rows[0].Group != "Beta Testers" {
		t.Errorf("rows = %+v", rows)
	}
	links := arr(t, f.body("POST /v1/betaGroups/g-ext/relationships/betaTesters"), "data")
	if len(links) != 1 || obj(t, links[0])["id"] != "t-old" {
		t.Errorf("existing tester linkage = %v", links)
	}
	if !strings.Contains(stderr, "Added existing tester old@example.com to Beta Testers") || !strings.Contains(stderr, "Invited new@example.com to Beta Testers") {
		t.Errorf("stderr = %q", stderr)
	}
}
