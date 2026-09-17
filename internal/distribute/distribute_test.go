package distribute

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MobAI-App/ios-builder/internal/asc"
)

func writeIPA(t *testing.T, plistBody string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "App.ipa")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.Create("Payload/App.app/Info.plist")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict>` + plistBody + `</dict></plist>`))
	bin, _ := zw.Create("Payload/App.app/App")
	payload := make([]byte, 3000)
	_, _ = rand.Read(payload)
	_, _ = bin.Write(payload)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

const plistExempt = `<key>CFBundleIdentifier</key><string>com.example.app</string><key>CFBundleShortVersionString</key><string>2.0.0</string><key>CFBundleVersion</key><string>7</string><key>ITSAppUsesNonExemptEncryption</key><false/>`
const plistUndeclared = `<key>CFBundleIdentifier</key><string>com.example.app</string><key>CFBundleShortVersionString</key><string>2.0.0</string><key>CFBundleVersion</key><string>7</string>`

// fake is an in-memory App Store Connect covering the routes the flows use.
type fake struct {
	t   *testing.T
	srv *httptest.Server
	mu  sync.Mutex
	// calls lists "METHOD /path" in order; bodies keeps the last body per call.
	calls  []string
	bodies map[string]map[string]any
	// state knobs
	buildState       string
	buildEncryption  *bool
	versionExists    bool
	versionState     string
	openSubmission   bool
	submitStatus     int
	betaReviewExists bool
	// noGroups empties the group list; autoGroup adds an internal group with
	// automatic distribution; created collects groups made through the API.
	noGroups  bool
	autoGroup bool
	created   []map[string]any
	// users are team members by email; testers maps tester emails to IDs;
	// pendingInvite makes every invitation lookup find one.
	users         map[string]bool
	testers       map[string]string
	pendingInvite bool
}

func newFake(t *testing.T) *fake {
	f := &fake{t: t, bodies: map[string]map[string]any{}, buildState: "VALID", versionState: "PREPARE_FOR_SUBMISSION", submitStatus: 200, users: map[string]bool{}, testers: map[string]string{}}
	mux := http.NewServeMux()
	res := func(typ, id string, attrs map[string]any, rels map[string]any) map[string]any {
		r := map[string]any{"type": typ, "id": id, "attributes": attrs}
		if rels != nil {
			r["relationships"] = rels
		}
		return r
	}
	one := func(w http.ResponseWriter, status int, r any) { writeJSON(w, status, map[string]any{"data": r}) }
	many := func(w http.ResponseWriter, rs ...any) {
		if rs == nil {
			rs = []any{}
		}
		writeJSON(w, 200, map[string]any{"data": rs})
	}
	record := func(r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		key := r.Method + " " + r.URL.Path
		f.calls = append(f.calls, key)
		if r.Body != nil {
			var body map[string]any
			data, _ := io.ReadAll(r.Body)
			if json.Unmarshal(data, &body) == nil {
				f.bodies[key] = body
			}
		}
	}
	wrap := func(h func(w http.ResponseWriter, r *http.Request)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/chunk" && !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				f.t.Errorf("%s %s without bearer token", r.Method, r.URL.Path)
			}
			record(r)
			f.mu.Lock()
			defer f.mu.Unlock()
			h(w, r)
		}
	}
	build := func() map[string]any {
		attrs := map[string]any{"version": "7", "processingState": f.buildState, "uploadedDate": "2026-09-16T10:00:00Z", "expired": false}
		if f.buildEncryption != nil {
			attrs["usesNonExemptEncryption"] = *f.buildEncryption
		}
		return res("builds", "build-9", attrs, nil)
	}
	mux.HandleFunc("GET /v1/apps", wrap(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("filter[bundleId]") != "com.example.app" {
			many(w)
			return
		}
		many(w, res("apps", "app-1", map[string]any{"bundleId": "com.example.app", "name": "Example", "primaryLocale": "de-DE"}, nil))
	}))
	mux.HandleFunc("POST /v1/buildUploads", wrap(func(w http.ResponseWriter, r *http.Request) {
		one(w, 201, res("buildUploads", "up-1", map[string]any{"state": map[string]any{"state": "AWAITING_UPLOAD"}}, nil))
	}))
	mux.HandleFunc("POST /v1/buildUploadFiles", wrap(func(w http.ResponseWriter, r *http.Request) {
		size, _ := obj(f.t, f.bodies["POST /v1/buildUploadFiles"], "data", "attributes")["fileSize"].(float64)
		one(w, 201, res("buildUploadFiles", "file-1", map[string]any{"uploadOperations": []map[string]any{{"method": "PUT", "url": f.srv.URL + "/chunk", "offset": 0, "length": int64(size), "requestHeaders": []map[string]string{{"name": "X-Test", "value": "1"}}}}}, nil))
	}))
	mux.HandleFunc("PUT /chunk", wrap(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test") != "1" {
			f.t.Error("chunk request header missing")
		}
		w.WriteHeader(200)
	}))
	mux.HandleFunc("PATCH /v1/buildUploadFiles/{id}", wrap(func(w http.ResponseWriter, r *http.Request) { one(w, 200, res("buildUploadFiles", "file-1", nil, nil)) }))
	mux.HandleFunc("GET /v1/buildUploads/{id}", wrap(func(w http.ResponseWriter, r *http.Request) {
		one(w, 200, res("buildUploads", "up-1", map[string]any{"cfBundleShortVersionString": "2.0.0", "cfBundleVersion": "7", "state": map[string]any{"state": "COMPLETE", "warnings": []map[string]string{{"code": "ITMS-90000", "description": "Some warning"}}}}, nil))
	}))
	mux.HandleFunc("GET /v1/builds", wrap(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("filter[app]") != "app-1" || q.Get("filter[preReleaseVersion.platform]") != "IOS" || q.Get("sort") != "-uploadedDate" {
			f.t.Errorf("builds query = %v", q)
		}
		if q.Get("filter[processingState]") == "VALID" && f.buildState != "VALID" {
			many(w)
			return
		}
		many(w, build())
	}))
	mux.HandleFunc("PATCH /v1/builds/{id}", wrap(func(w http.ResponseWriter, r *http.Request) {
		v, _ := obj(f.t, f.bodies["PATCH /v1/builds/build-9"], "data", "attributes")["usesNonExemptEncryption"].(bool)
		f.buildEncryption = &v
		one(w, 200, build())
	}))
	mux.HandleFunc("GET /v1/betaGroups", wrap(func(w http.ResponseWriter, r *http.Request) {
		if f.noGroups {
			many(w)
			return
		}
		groups := []any{
			res("betaGroups", "g-int", map[string]any{"name": "Team", "isInternalGroup": true, "hasAccessToAllBuilds": false}, nil),
			res("betaGroups", "g-ext", map[string]any{"name": "Beta Testers", "isInternalGroup": false, "publicLinkEnabled": true}, nil),
		}
		if f.autoGroup {
			groups = append(groups, res("betaGroups", "g-auto", map[string]any{"name": "Everyone", "isInternalGroup": true, "hasAccessToAllBuilds": true}, nil))
		}
		for _, g := range f.created {
			groups = append(groups, g)
		}
		many(w, groups...)
	}))
	mux.HandleFunc("POST /v1/betaGroups", wrap(func(w http.ResponseWriter, r *http.Request) {
		attrs := obj(f.t, f.bodies["POST /v1/betaGroups"], "data", "attributes")
		g := res("betaGroups", fmt.Sprintf("g-new-%d", len(f.created)+1), attrs, nil)
		f.created = append(f.created, g)
		one(w, 201, g)
	}))
	mux.HandleFunc("POST /v1/betaGroups/{id}/relationships/betaTesters", wrap(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	mux.HandleFunc("GET /v1/betaTesters", wrap(func(w http.ResponseWriter, r *http.Request) {
		email := r.URL.Query().Get("filter[email]")
		var testers []any
		for e, id := range f.testers {
			if email == "" || strings.EqualFold(e, email) {
				testers = append(testers, res("betaTesters", id, map[string]any{"email": e, "state": "ACCEPTED"}, nil))
			}
		}
		many(w, testers...)
	}))
	mux.HandleFunc("POST /v1/betaTesters", wrap(func(w http.ResponseWriter, r *http.Request) {
		email, _ := obj(f.t, f.bodies["POST /v1/betaTesters"], "data", "attributes")["email"].(string)
		if _, exists := f.testers[email]; exists {
			writeJSON(w, 409, map[string]any{"errors": []map[string]any{{"status": "409", "code": "ENTITY_ERROR.ATTRIBUTE.INVALID.DUPLICATE", "title": "duplicate", "detail": "A beta tester with the email '" + email + "' already exists."}}})
			return
		}
		id := fmt.Sprintf("t-new-%d", len(f.testers)+1)
		f.testers[email] = id
		one(w, 201, res("betaTesters", id, map[string]any{"email": email, "state": "INVITED"}, nil))
	}))
	mux.HandleFunc("GET /v1/users", wrap(func(w http.ResponseWriter, r *http.Request) {
		email := r.URL.Query().Get("filter[username]")
		if f.users[email] {
			many(w, res("users", "u-"+email, map[string]any{"username": email, "firstName": "Team", "lastName": "Member", "roles": []string{"DEVELOPER"}}, nil))
			return
		}
		many(w)
	}))
	mux.HandleFunc("GET /v1/userInvitations", wrap(func(w http.ResponseWriter, r *http.Request) {
		email := r.URL.Query().Get("filter[email]")
		if f.pendingInvite {
			many(w, res("userInvitations", "inv-0", map[string]any{"email": email, "roles": []string{"CUSTOMER_SUPPORT"}}, nil))
			return
		}
		many(w)
	}))
	mux.HandleFunc("POST /v1/userInvitations", wrap(func(w http.ResponseWriter, r *http.Request) {
		attrs := obj(f.t, f.bodies["POST /v1/userInvitations"], "data", "attributes")
		one(w, 201, res("userInvitations", "inv-1", attrs, nil))
	}))
	mux.HandleFunc("GET /v1/builds/{id}/betaBuildLocalizations", wrap(func(w http.ResponseWriter, r *http.Request) {
		many(w, res("betaBuildLocalizations", "loc-en", map[string]any{"locale": "en-US", "whatsNew": "old"}, nil))
	}))
	mux.HandleFunc("POST /v1/betaBuildLocalizations", wrap(func(w http.ResponseWriter, r *http.Request) {
		one(w, 201, res("betaBuildLocalizations", "loc-de", map[string]any{"locale": "de-DE"}, nil))
	}))
	mux.HandleFunc("PATCH /v1/betaBuildLocalizations/{id}", wrap(func(w http.ResponseWriter, r *http.Request) {
		one(w, 200, res("betaBuildLocalizations", "loc-en", map[string]any{"locale": "en-US"}, nil))
	}))
	mux.HandleFunc("GET /v1/builds/{id}/betaAppReviewSubmission", wrap(func(w http.ResponseWriter, r *http.Request) {
		if f.betaReviewExists {
			one(w, 200, res("betaAppReviewSubmissions", "bar-0", map[string]any{"betaReviewState": "APPROVED"}, nil))
			return
		}
		one(w, 200, nil)
	}))
	mux.HandleFunc("POST /v1/betaAppReviewSubmissions", wrap(func(w http.ResponseWriter, r *http.Request) {
		one(w, 201, res("betaAppReviewSubmissions", "bar-1", map[string]any{"betaReviewState": "WAITING_FOR_REVIEW"}, nil))
	}))
	mux.HandleFunc("GET /v1/betaAppReviewSubmissions/{id}", wrap(func(w http.ResponseWriter, r *http.Request) {
		one(w, 200, res("betaAppReviewSubmissions", "bar-1", map[string]any{"betaReviewState": "APPROVED"}, nil))
	}))
	mux.HandleFunc("POST /v1/builds/{id}/relationships/betaGroups", wrap(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	version := func() map[string]any {
		return res("appStoreVersions", "ver-1", map[string]any{"platform": "IOS", "versionString": "2.0.0", "appVersionState": f.versionState, "releaseType": "MANUAL"}, map[string]any{"build": map[string]any{"data": nil}})
	}
	mux.HandleFunc("GET /v1/apps/{id}/appStoreVersions", wrap(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("filter[versionString]") != "2.0.0" || r.URL.Query().Get("filter[platform]") != "IOS" {
			f.t.Errorf("versions query = %v", r.URL.Query())
		}
		if f.versionExists {
			many(w, version())
			return
		}
		many(w)
	}))
	mux.HandleFunc("POST /v1/appStoreVersions", wrap(func(w http.ResponseWriter, r *http.Request) { f.versionExists = true; one(w, 201, version()) }))
	mux.HandleFunc("PATCH /v1/appStoreVersions/{id}", wrap(func(w http.ResponseWriter, r *http.Request) {
		v := version()
		obj(f.t, v, "attributes")["releaseType"] = "AFTER_APPROVAL"
		v["relationships"] = map[string]any{"build": map[string]any{"data": map[string]string{"type": "builds", "id": "build-9"}}}
		one(w, 200, v)
	}))
	mux.HandleFunc("GET /v1/reviewSubmissions", wrap(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("filter[state]") != "READY_FOR_REVIEW,UNRESOLVED_ISSUES" {
			f.t.Errorf("submissions query = %v", r.URL.Query())
		}
		if f.openSubmission {
			many(w, res("reviewSubmissions", "rs-0", map[string]any{"platform": "IOS", "state": "READY_FOR_REVIEW"}, nil))
			return
		}
		many(w)
	}))
	mux.HandleFunc("POST /v1/reviewSubmissions", wrap(func(w http.ResponseWriter, r *http.Request) {
		one(w, 201, res("reviewSubmissions", "rs-1", map[string]any{"platform": "IOS", "state": "READY_FOR_REVIEW"}, nil))
	}))
	mux.HandleFunc("GET /v1/reviewSubmissions/{id}/items", wrap(func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") == "rs-0" {
			many(w, res("reviewSubmissionItems", "item-0", map[string]any{"state": "READY_FOR_REVIEW"}, map[string]any{"appStoreVersion": map[string]any{"data": map[string]string{"type": "appStoreVersions", "id": "ver-1"}}}))
			return
		}
		many(w)
	}))
	mux.HandleFunc("POST /v1/reviewSubmissionItems", wrap(func(w http.ResponseWriter, r *http.Request) {
		one(w, 201, res("reviewSubmissionItems", "item-1", map[string]any{"state": "READY_FOR_REVIEW"}, nil))
	}))
	mux.HandleFunc("PATCH /v1/reviewSubmissions/{id}", wrap(func(w http.ResponseWriter, r *http.Request) {
		if f.submitStatus != 200 {
			writeJSON(w, f.submitStatus, map[string]any{"errors": []map[string]any{{"status": "409", "code": "STATE_ERROR.ENTITY_STATE_INVALID", "title": "The request cannot be fulfilled because of the state of another resource.", "detail": "You must provide a screenshot for iPhone 6.5\" displays."}}})
			return
		}
		one(w, 200, res("reviewSubmissions", r.PathValue("id"), map[string]any{"platform": "IOS", "state": "WAITING_FOR_REVIEW", "submittedDate": "2026-09-16T11:00:00Z"}, nil))
	}))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL)
		w.WriteHeader(404)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
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

func (f *fake) client(t *testing.T) *asc.Client {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	creds := asc.Credentials{IssuerID: "iss", KeyID: "kid", PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))}
	c, err := asc.NewClient(creds, asc.WithBaseURL(f.srv.URL), asc.WithRetryDelay(time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (f *fake) called(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == key {
			return true
		}
	}
	return false
}

func (f *fake) body(key string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bodies[key]
}

func TestUploadWithWaitSetsCompliance(t *testing.T) {
	f := newFake(t)
	var log bytes.Buffer
	res, err := Upload(context.Background(), f.client(t), &UploadOptions{IPAPath: writeIPA(t, plistExempt), Wait: true, PollInterval: time.Millisecond, Log: &log})
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	if res.App.ID != "app-1" || res.IPA.Version != "2.0.0" || res.IPA.BuildNumber != "7" || res.Upload.ID != "up-1" || res.Upload.State != "COMPLETE" {
		t.Errorf("result = %+v", res)
	}
	if res.Build == nil || res.Build.ID != "build-9" || res.Build.ProcessingState != "VALID" || res.Compliance != "set_exempt" || res.Build.UsesNonExemptEncryption == nil || *res.Build.UsesNonExemptEncryption {
		t.Errorf("build = %+v, compliance = %s", res.Build, res.Compliance)
	}
	if res.Link != "https://appstoreconnect.apple.com/apps/app-1/testflight/ios/build-9" {
		t.Errorf("link = %s", res.Link)
	}
	if len(res.Upload.Warnings) != 1 || !strings.Contains(log.String(), "ITMS-90000") {
		t.Errorf("warnings not surfaced: %+v\n%s", res.Upload.Warnings, log.String())
	}
	for _, key := range []string{"POST /v1/buildUploads", "POST /v1/buildUploadFiles", "PUT /chunk", "PATCH /v1/buildUploadFiles/file-1", "GET /v1/buildUploads/up-1", "GET /v1/builds", "PATCH /v1/builds/build-9"} {
		if !f.called(key) {
			t.Errorf("%s not called; calls = %v", key, f.calls)
		}
	}
	attrs := obj(t, f.body("POST /v1/buildUploads"), "data", "attributes")
	if attrs["cfBundleShortVersionString"] != "2.0.0" || attrs["cfBundleVersion"] != "7" || attrs["platform"] != "IOS" {
		t.Errorf("upload attributes = %v", attrs)
	}
}

func TestUploadWithoutWaitLeavesComplianceForLater(t *testing.T) {
	f := newFake(t)
	res, err := Upload(context.Background(), f.client(t), &UploadOptions{IPAPath: writeIPA(t, plistUndeclared), NoEncryption: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Build != nil || res.Compliance != "pending" || res.Link != "https://appstoreconnect.apple.com/apps/app-1/testflight/ios" {
		t.Errorf("result = %+v", res)
	}
	if f.called("PATCH /v1/builds/build-9") || f.called("GET /v1/builds") {
		t.Errorf("must not touch builds without --wait: %v", f.calls)
	}
}

func TestUploadUndeclaredEncryptionStaysPending(t *testing.T) {
	f := newFake(t)
	res, err := Upload(context.Background(), f.client(t), &UploadOptions{IPAPath: writeIPA(t, plistUndeclared), Wait: true, PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if res.Compliance != "pending" || f.called("PATCH /v1/builds/build-9") {
		t.Errorf("compliance = %s, calls = %v", res.Compliance, f.calls)
	}
}

func TestUploadUnknownApp(t *testing.T) {
	f := newFake(t)
	_, err := Upload(context.Background(), f.client(t), &UploadOptions{IPAPath: writeIPA(t, strings.ReplaceAll(plistExempt, "com.example.app", "com.other"))})
	if err == nil || !strings.Contains(err.Error(), "com.other") {
		t.Errorf("err = %v", err)
	}
}
