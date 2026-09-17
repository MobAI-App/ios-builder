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
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// ascFake is an in-memory App Store Connect covering what the command tests
// touch: one app, one processed build, two groups, an accepted tester and a
// NOT_INVITED one.
type ascFake struct {
	t      *testing.T
	mu     sync.Mutex
	calls  []string
	bodies map[string]map[string]any
	// dupGroup adds a second group whose name folds to "beta testers".
	dupGroup bool
	// quietState is the NOT_INVITED tester's state; an invitation flips it.
	quietState string
}

func newASCFake(t *testing.T) *ascFake {
	t.Helper()
	f := &ascFake{t: t, bodies: map[string]map[string]any{}, quietState: "NOT_INVITED"}
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
		// The internal group mirrors a real one made in the UI: automatic distribution, null public link.
		groups := []any{
			res("betaGroups", "g-int", map[string]any{"name": "Team", "isInternalGroup": true, "hasAccessToAllBuilds": true, "publicLinkEnabled": nil}),
			res("betaGroups", "g-ext", map[string]any{"name": "Beta Testers", "isInternalGroup": false}),
		}
		if f.dupGroup {
			groups = append(groups, res("betaGroups", "g-dup", map[string]any{"name": "beta testers", "isInternalGroup": false}))
		}
		many(w, groups...)
	})
	handle("POST /v1/betaGroups", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		one(w, 201, res("betaGroups", "g-new", obj(t, body, "data", "attributes")))
	})
	handle("DELETE /v1/betaGroups/{id}", func(w http.ResponseWriter, r *http.Request, _ map[string]any) { w.WriteHeader(204) })
	handle("POST /v1/builds/{id}/relationships/betaGroups", func(w http.ResponseWriter, r *http.Request, _ map[string]any) { w.WriteHeader(204) })
	handle("POST /v1/betaGroups/{id}/relationships/betaTesters", func(w http.ResponseWriter, r *http.Request, _ map[string]any) { w.WriteHeader(204) })
	handle("DELETE /v1/betaGroups/{id}/relationships/betaTesters", func(w http.ResponseWriter, r *http.Request, _ map[string]any) { w.WriteHeader(204) })
	old := func() map[string]any {
		return res("betaTesters", "t-old", map[string]any{"email": "old@example.com", "firstName": "Old", "inviteType": "EMAIL", "state": "ACCEPTED"})
	}
	quiet := func() map[string]any {
		return res("betaTesters", "t-quiet", map[string]any{"email": "quiet@example.com", "inviteType": "EMAIL", "state": f.quietState})
	}
	handle("GET /v1/betaTesters", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		switch r.URL.Query().Get("filter[email]") {
		case "old@example.com":
			many(w, old())
		case "quiet@example.com":
			many(w, quiet())
		case "":
			many(w, old(), quiet())
		default:
			many(w)
		}
	})
	handle("GET /v1/betaTesters/{id}", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		switch r.PathValue("id") {
		case "t-old":
			one(w, 200, old())
		case "t-quiet":
			one(w, 200, quiet())
		default:
			w.WriteHeader(404)
		}
	})
	handle("DELETE /v1/betaTesters/{id}", func(w http.ResponseWriter, r *http.Request, _ map[string]any) { w.WriteHeader(204) })
	handle("POST /v1/betaTesterInvitations", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if obj(t, body, "data", "relationships", "betaTester", "data")["id"] == "t-quiet" {
			f.quietState = "INVITED"
		}
		one(w, 201, map[string]any{"type": "betaTesterInvitations", "id": "bti-1"})
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

// run executes a builder command line and returns stdout and stderr. Flag
// values survive Execute on the shared command tree, so they are reset first.
func run(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	resetFlags(rootCmd)
	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs(args)
	err = rootCmd.Execute()
	return out.String(), errOut.String(), err
}

func resetFlags(c *cobra.Command) {
	c.Flags().VisitAll(func(f *pflag.Flag) {
		if sv, ok := f.Value.(pflag.SliceValue); ok {
			_ = sv.Replace(nil)
		} else {
			_ = f.Value.Set(f.DefValue)
		}
		f.Changed = false
	})
	for _, sub := range c.Commands() {
		resetFlags(sub)
	}
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
	if stdout != "" && strings.Contains(stdout, "Added existing") {
		t.Errorf("progress leaked into stdout: %q", stdout)
	}
}

func TestASCTestersInvite(t *testing.T) {
	f := newASCFake(t)
	stdout, stderr, err := run(t, "asc", "testers", "invite", "quiet@example.com", "old@example.com", "--bundle-id", "com.example.app", "--json")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	var rows []inviteRow
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatalf("stdout is not a JSON array: %v\n%s", err, stdout)
	}
	if len(rows) != 2 || rows[0].ID != "t-quiet" || rows[0].State != "INVITED" || !rows[0].Invited || rows[1].ID != "t-old" || rows[1].State != "ACCEPTED" || rows[1].Invited {
		t.Errorf("rows = %+v", rows)
	}
	invite := obj(t, f.body("POST /v1/betaTesterInvitations"), "data")
	if obj(t, invite, "relationships", "app", "data")["id"] != "app-1" || obj(t, invite, "relationships", "betaTester", "data")["id"] != "t-quiet" {
		t.Errorf("invitation = %v", invite)
	}
	if !strings.Contains(stderr, "Sent TestFlight invitation to quiet@example.com (INVITED)") || !strings.Contains(stderr, "old@example.com is ACCEPTED; no invitation sent") {
		t.Errorf("stderr = %q", stderr)
	}
	if !strings.Contains(stdout, `"invited": true`) || strings.Contains(stdout, "Sent TestFlight") {
		t.Errorf("stdout = %q", stdout)
	}

	// An address the app does not have is an error before anything is sent.
	f = newASCFake(t)
	if _, _, err := run(t, "asc", "testers", "invite", "nobody@example.com", "--bundle-id", "com.example.app"); err == nil || !strings.Contains(err.Error(), "no TestFlight tester nobody@example.com") || f.called("POST /v1/betaTesterInvitations") {
		t.Errorf("err = %v, calls = %v", err, f.calls)
	}
}

func TestASCTestersListHintsNotInvited(t *testing.T) {
	newASCFake(t)
	stdout, _, err := run(t, "asc", "testers", "--bundle-id", "com.example.app")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "EMAIL") || !strings.Contains(stdout, "NOT_INVITED") || !strings.Contains(stdout, "builder asc testers invite") {
		t.Errorf("stdout = %q", stdout)
	}
	stdout, _, err = run(t, "asc", "testers", "--bundle-id", "com.example.app", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []testerRow
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil || len(rows) != 2 || rows[1].State != "NOT_INVITED" {
		t.Errorf("rows = %+v, err = %v\n%s", rows, err, stdout)
	}
}

func TestASCGroupsDeleteNeedsYes(t *testing.T) {
	f := newASCFake(t)
	stdout, _, err := run(t, "asc", "groups", "delete", "beta testers", "--bundle-id", "com.example.app")
	if err == nil || !strings.Contains(err.Error(), "--yes") || f.called("DELETE /v1/betaGroups/g-ext") {
		t.Errorf("err = %v, calls = %v", err, f.calls)
	}
	if !strings.Contains(stdout, "Will delete TestFlight group Beta Testers (external, 2 testers, g-ext)") {
		t.Errorf("the preview must name the group: %q", stdout)
	}
	stdout, stderr, err := run(t, "asc", "groups", "delete", "beta testers", "--yes", "--bundle-id", "com.example.app", "--json")
	if err != nil || !f.called("DELETE /v1/betaGroups/g-ext") {
		t.Fatalf("err = %v, calls = %v", err, f.calls)
	}
	var row groupRow
	if err := json.Unmarshal([]byte(stdout), &row); err != nil || row.ID != "g-ext" || row.Testers != 2 {
		t.Errorf("row = %+v, err = %v\n%s", row, err, stdout)
	}
	if !strings.Contains(stderr, "Deleted TestFlight group Beta Testers") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestASCTestersRemoveTeamWideNeedsYes(t *testing.T) {
	f := newASCFake(t)
	_, _, err := run(t, "asc", "testers", "remove", "old@example.com", "--bundle-id", "com.example.app")
	if err == nil || !strings.Contains(err.Error(), "--yes") || len(f.calls) != 0 {
		t.Errorf("err = %v, calls = %v", err, f.calls)
	}
	// Every address is resolved before the first deletion.
	_, _, err = run(t, "asc", "testers", "remove", "old@example.com", "nobody@example.com", "--yes", "--bundle-id", "com.example.app")
	if err == nil || !strings.Contains(err.Error(), "nobody@example.com") || f.called("DELETE /v1/betaTesters/t-old") {
		t.Errorf("err = %v, calls = %v", err, f.calls)
	}
	stdout, _, err := run(t, "asc", "testers", "remove", "old@example.com", "--yes", "--bundle-id", "com.example.app")
	if err != nil || !f.called("DELETE /v1/betaTesters/t-old") {
		t.Fatalf("err = %v, calls = %v", err, f.calls)
	}
	if !strings.Contains(stdout, "Will remove old@example.com (t-old) from TestFlight for the whole team") {
		t.Errorf("stdout = %q", stdout)
	}

	// With --group only the linkage goes.
	f = newASCFake(t)
	stdout, _, err = run(t, "asc", "testers", "remove", "old@example.com", "--group", "Beta Testers", "--bundle-id", "com.example.app", "--json")
	if err != nil || f.called("DELETE /v1/betaTesters/t-old") {
		t.Fatalf("err = %v, calls = %v", err, f.calls)
	}
	if links := arr(t, f.body("DELETE /v1/betaGroups/g-ext/relationships/betaTesters"), "data"); len(links) != 1 || obj(t, links[0])["id"] != "t-old" {
		t.Errorf("linkage = %v", links)
	}
	var rows []testerRemoveRow
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil || len(rows) != 1 || rows[0].Group != "Beta Testers" {
		t.Errorf("rows = %+v, err = %v\n%s", rows, err, stdout)
	}
}

func TestASCAmbiguousGroupIsRefused(t *testing.T) {
	f := newASCFake(t)
	f.dupGroup = true
	for _, args := range [][]string{
		{"asc", "groups", "delete", "BETA TESTERS", "--yes"},
		{"asc", "testers", "remove", "old@example.com", "--group", "beta testers"},
		{"asc", "testers", "add", "new@example.com", "--group", "beta testers"},
		{"asc", "groups", "add-build", "beta testers"},
	} {
		_, _, err := run(t, append(args, "--bundle-id", "com.example.app")...)
		if err == nil || !strings.Contains(err.Error(), "Beta Testers (g-ext)") || !strings.Contains(err.Error(), "beta testers (g-dup)") {
			t.Errorf("%v: err = %v", args, err)
		}
	}
	for _, c := range f.calls {
		if strings.HasPrefix(c, "DELETE") || c == "POST /v1/betaTesters" || strings.HasSuffix(c, "/relationships/betaGroups") {
			t.Errorf("ambiguous name must change nothing: %v", f.calls)
		}
	}
	// A unique case-insensitive match still works.
	if _, _, err := run(t, "asc", "testers", "--group", "TEAM", "--bundle-id", "com.example.app"); err != nil {
		t.Error(err)
	}
}

func TestASCResolvesAppOrExplains(t *testing.T) {
	newASCFake(t)
	_, _, err := run(t, "asc", "groups")
	if err == nil || !strings.Contains(err.Error(), "--bundle-id") || !strings.Contains(err.Error(), "builder.json") {
		t.Errorf("err = %v", err)
	}
}
