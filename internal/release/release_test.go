package release

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
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MobAI-App/ios-builder/internal/asc"
	"github.com/MobAI-App/ios-builder/internal/build"
	"github.com/MobAI-App/ios-builder/internal/config"
)

const bundleID = "com.example.app"

func writeIPA(t *testing.T, path, bundle, version, buildNumber string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, _ := zw.Create("Payload/App.app/Info.plist")
	_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>` + bundle +
		`</string><key>CFBundleShortVersionString</key><string>` + version + `</string><key>CFBundleVersion</key><string>` + buildNumber +
		`</string><key>ITSAppUsesNonExemptEncryption</key><false/></dict></plist>`))
	bin, _ := zw.Create("Payload/App.app/App")
	_, _ = bin.Write(bytes.Repeat([]byte("x"), 2048))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// fakeBuilder records the options and drops an IPA stamped with the requested
// build number (or `stamp`, to play a runner that ignored it) into OutputDir.
type fakeBuilder struct {
	t     *testing.T
	opts  build.BuildOptions
	stamp string
	err   error
	calls int
}

func (b *fakeBuilder) Build(_ context.Context, opts *build.BuildOptions) (*build.BuildResult, error) {
	b.opts, b.calls = *opts, b.calls+1
	if b.err != nil {
		return nil, b.err
	}
	// Decode "X.Y.Z+N" the way the runner does.
	v, n, hasVersion := strings.Cut(opts.BuildNumber, "+")
	if !hasVersion {
		v, n = "2.0.0", opts.BuildNumber
	}
	if b.stamp != "" {
		n = b.stamp
	}
	path := filepath.Join(opts.OutputDir, "App-abcdef12.ipa")
	writeIPA(b.t, path, bundleID, v, n)
	return &build.BuildResult{BuildID: "abcdef12", IPAPath: path, WorkflowURL: "https://github.com/o/r/actions/runs/1"}, nil
}

// fake is the slice of App Store Connect the release flow touches.
type fake struct {
	t        *testing.T
	srv      *httptest.Server
	mu       sync.Mutex
	calls    []string
	existing []string // build numbers already in App Store Connect
	listed   []string // filter[version] of every GET /v1/builds
	// encryption is the export compliance answer once PATCHed.
	encryption *bool
}

func newFake(t *testing.T, existing ...string) *fake {
	f := &fake{t: t, existing: existing}
	res := func(typ, id string, attrs map[string]any) map[string]any {
		return map[string]any{"type": typ, "id": id, "attributes": attrs}
	}
	writeJSON := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	one := func(w http.ResponseWriter, r any) { writeJSON(w, 200, map[string]any{"data": r}) }
	many := func(w http.ResponseWriter, rs ...any) {
		if rs == nil {
			rs = []any{}
		}
		writeJSON(w, 200, map[string]any{"data": rs})
	}
	mux := http.NewServeMux()
	handle := func(pattern string, h func(w http.ResponseWriter, r *http.Request)) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.calls = append(f.calls, r.Method+" "+r.URL.Path)
			h(w, r)
		})
	}
	handle("GET /v1/apps", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("filter[bundleId]") != bundleID {
			many(w)
			return
		}
		many(w, res("apps", "app-1", map[string]any{"bundleId": bundleID, "name": "Example"}))
	})
	handle("GET /v1/builds", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		f.listed = append(f.listed, q.Get("filter[version]"))
		if n := q.Get("filter[version]"); n != "" {
			attrs := map[string]any{"version": n, "processingState": "VALID", "uploadedDate": "2026-09-16T10:00:00Z", "expired": false}
			if f.encryption != nil {
				attrs["usesNonExemptEncryption"] = *f.encryption
			}
			many(w, res("builds", "build-9", attrs))
			return
		}
		var rs []any
		for i, n := range f.existing {
			rs = append(rs, res("builds", "old-"+n, map[string]any{"version": n, "processingState": "VALID", "uploadedDate": time.Date(2026, 1, 1+i, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)}))
		}
		many(w, rs...)
	})
	handle("POST /v1/buildUploads", func(w http.ResponseWriter, r *http.Request) {
		one(w, res("buildUploads", "up-1", map[string]any{"state": map[string]any{"state": "AWAITING_UPLOAD"}}))
	})
	handle("POST /v1/buildUploadFiles", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Data struct {
				Attributes struct{ FileSize int64 }
			}
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		one(w, res("buildUploadFiles", "file-1", map[string]any{"uploadOperations": []map[string]any{{"method": "PUT", "url": f.srv.URL + "/chunk", "offset": 0, "length": body.Data.Attributes.FileSize}}}))
	})
	handle("PUT /chunk", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	handle("PATCH /v1/buildUploadFiles/{id}", func(w http.ResponseWriter, r *http.Request) { one(w, res("buildUploadFiles", "file-1", nil)) })
	handle("GET /v1/buildUploads/{id}", func(w http.ResponseWriter, r *http.Request) {
		one(w, res("buildUploads", "up-1", map[string]any{"state": map[string]any{"state": "COMPLETE"}}))
	})
	handle("PATCH /v1/builds/{id}", func(w http.ResponseWriter, r *http.Request) {
		no := false
		f.encryption = &no
		one(w, res("builds", r.PathValue("id"), map[string]any{"version": "13", "processingState": "VALID", "usesNonExemptEncryption": false}))
	})
	handle("GET /v1/betaGroups", func(w http.ResponseWriter, r *http.Request) {
		many(w, res("betaGroups", "g-int", map[string]any{"name": "Team", "isInternalGroup": true}))
	})
	handle("POST /v1/builds/{id}/relationships/betaGroups", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	handle("GET /v1/apps/{id}/appStoreVersions", func(w http.ResponseWriter, r *http.Request) { many(w) })
	handle("POST /v1/appStoreVersions", func(w http.ResponseWriter, r *http.Request) {
		one(w, res("appStoreVersions", "ver-1", map[string]any{"platform": "IOS", "versionString": "3.1.0", "appVersionState": "PREPARE_FOR_SUBMISSION"}))
	})
	handle("PATCH /v1/appStoreVersions/{id}", func(w http.ResponseWriter, r *http.Request) {
		one(w, res("appStoreVersions", "ver-1", map[string]any{"platform": "IOS", "versionString": "3.1.0", "appVersionState": "PREPARE_FOR_SUBMISSION", "releaseType": "AFTER_APPROVAL"}))
	})
	handle("GET /v1/reviewSubmissions", func(w http.ResponseWriter, r *http.Request) { many(w) })
	handle("POST /v1/reviewSubmissions", func(w http.ResponseWriter, r *http.Request) {
		one(w, res("reviewSubmissions", "rs-1", map[string]any{"platform": "IOS", "state": "READY_FOR_REVIEW"}))
	})
	handle("GET /v1/reviewSubmissions/{id}/items", func(w http.ResponseWriter, r *http.Request) { many(w) })
	handle("POST /v1/reviewSubmissionItems", func(w http.ResponseWriter, r *http.Request) {
		one(w, res("reviewSubmissionItems", "item-1", map[string]any{"state": "READY_FOR_REVIEW"}))
	})
	handle("PATCH /v1/reviewSubmissions/{id}", func(w http.ResponseWriter, r *http.Request) {
		one(w, res("reviewSubmissions", "rs-1", map[string]any{"platform": "IOS", "state": "WAITING_FOR_REVIEW"}))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL)
		w.WriteHeader(404)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
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

// releaseConfig has the store profile `signing setup --distribution store`
// writes, selected as the default so tests need no --profile.
func releaseConfig() *config.Config {
	return &config.Config{
		Project: "App", IOS: config.IOSConfig{BundleID: bundleID},
		DefaultProfile: "store", Profiles: map[string]config.Profile{"store": {Distribution: "store"}},
	}
}

func TestRunTestFlightPicksNextBuildNumber(t *testing.T) {
	f := newFake(t, "7", "12", "1.0.3", "beta")
	b := &fakeBuilder{t: t}
	var log bytes.Buffer
	res, err := Run(context.Background(), releaseConfig(), b, f.client(t), &Options{
		Build:  build.BuildOptions{OutputDir: filepath.Join(t.TempDir(), "dist"), Provider: "codemagic", Remote: "upstream", Timeout: time.Minute},
		Groups: []string{"Team"}, PollInterval: time.Millisecond, Log: &log,
	})
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	if b.opts.BuildNumber != "13" || b.opts.Unsigned || b.opts.Provider != "codemagic" || b.opts.Remote != "upstream" || b.opts.Profile != "" {
		t.Errorf("build options = %+v", b.opts)
	}
	if res.BuildNumber != "13" || res.Version != "2.0.0" || res.BuildID != "abcdef12" || res.ASCBuildID != "build-9" || res.BundleID != bundleID {
		t.Errorf("result = %+v", res)
	}
	if len(res.Groups) != 1 || res.Groups[0].Name != "Team" || res.Link != "https://appstoreconnect.apple.com/apps/app-1/testflight/ios/build-9" {
		t.Errorf("groups = %+v, link = %s", res.Groups, res.Link)
	}
	if len(f.listed) == 0 || f.listed[0] != "" {
		t.Errorf("the build-number query must list every build, got filters %q", f.listed)
	}
	for _, key := range []string{"POST /v1/buildUploads", "PUT /chunk", "GET /v1/buildUploads/up-1", "PATCH /v1/builds/build-9", "POST /v1/builds/build-9/relationships/betaGroups"} {
		if !f.called(key) {
			t.Errorf("%s not called; calls = %v", key, f.calls)
		}
	}
	if !strings.Contains(log.String(), "Build number 13") {
		t.Errorf("chosen number not logged:\n%s", log.String())
	}
	data, _ := json.Marshal(res)
	for _, key := range []string{`"build_id":"abcdef12"`, `"asc_build_id":"build-9"`, `"version":"2.0.0"`, `"build_number":"13"`, `"groups":[{`} {
		if !strings.Contains(string(data), key) {
			t.Errorf("JSON lacks %s: %s", key, data)
		}
	}
}

func TestRunAppStoreWithOverrides(t *testing.T) {
	f := newFake(t, "40")
	b := &fakeBuilder{t: t}
	// --profile names a store profile other than the default.
	cfg := releaseConfig()
	cfg.DefaultProfile = "development"
	cfg.Profiles["development"] = config.Profile{Distribution: "development"}
	res, err := Run(context.Background(), cfg, b, f.client(t), &Options{
		Build: build.BuildOptions{OutputDir: filepath.Join(t.TempDir(), "dist"), Profile: "store"}, BuildNumber: "100", Version: "3.1.0",
		AppStore: true, ReleaseType: asc.ReleaseTypeAfterApproval, PollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if b.opts.BuildNumber != "3.1.0+100" || b.opts.Profile != "store" {
		t.Errorf("build options = %+v", b.opts)
	}
	if res.Version != "3.1.0" || res.BuildNumber != "100" || res.Link != "https://appstoreconnect.apple.com/apps/app-1/distribution" {
		t.Errorf("result = %+v", res)
	}
	if !f.called("PATCH /v1/reviewSubmissions/rs-1") || !f.called("POST /v1/appStoreVersions") {
		t.Errorf("not submitted: %v", f.calls)
	}
	for _, filter := range f.listed {
		if filter == "" {
			t.Error("--build-number must skip the build listing")
		}
	}
}

func TestRunRefusesIPAWithoutTheBuildNumber(t *testing.T) {
	f := newFake(t)
	b := &fakeBuilder{t: t, stamp: "1"}
	res, err := Run(context.Background(), releaseConfig(), b, f.client(t), &Options{Build: build.BuildOptions{OutputDir: filepath.Join(t.TempDir(), "dist")}, BuildNumber: "5"})
	if err == nil || !strings.Contains(err.Error(), "did not apply build number 5") || !strings.Contains(err.Error(), `"1"`) {
		t.Fatalf("err = %v", err)
	}
	if res == nil || res.BuildID != "abcdef12" || res.ASCBuildID != "" {
		t.Errorf("partial result = %+v", res)
	}
	if f.called("POST /v1/buildUploads") {
		t.Error("uploaded an IPA with the wrong build number")
	}
}

func TestRunPreflight(t *testing.T) {
	profiles := map[string]config.Profile{
		"store":       {Distribution: "store"},
		"store-debug": {Distribution: "store", Configuration: "Debug"},
		"development": {Distribution: "development"},
		"unsigned":    {},
	}
	for name, tc := range map[string]struct {
		cfg     *config.Config
		profile string
		want    []string
	}{
		// The legacy ios.signing path is not an App Store profile either.
		"no profile":         {&config.Config{IOS: config.IOSConfig{Signing: true, Configuration: "Release"}}, "", []string{"signing setup --distribution store", "--profile store"}},
		"unsigned profile":   {&config.Config{Profiles: profiles}, "unsigned", []string{"signing setup --distribution store", "--profile store"}},
		"development":        {&config.Config{Profiles: profiles}, "development", []string{`"development" has distribution development`, "--profile store"}},
		"explicit Debug":     {&config.Config{Profiles: profiles}, "store-debug", []string{`"store-debug" builds Debug`, `"Release"`}},
		"unknown profile":    {&config.Config{Profiles: profiles}, "missing", []string{`"missing" is not defined`}},
		"defaultProfile":     {&config.Config{Profiles: profiles, DefaultProfile: "development"}, "", []string{"distribution development"}},
		"flag beats default": {&config.Config{Profiles: profiles, DefaultProfile: "store"}, "development", []string{"distribution development"}},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t)
			b := &fakeBuilder{t: t}
			_, err := Run(context.Background(), tc.cfg, b, f.client(t), &Options{Build: build.BuildOptions{Profile: tc.profile}})
			if err == nil || b.calls != 0 || len(f.calls) != 0 {
				t.Fatalf("err = %v, builds = %d, calls = %v", err, b.calls, f.calls)
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("err = %v\nwant %q", err, want)
				}
			}
		})
	}
	// Explicit Release, and --profile over a non-store default, both pass.
	for _, tc := range []struct {
		cfg     *config.Config
		profile string
	}{
		{&config.Config{Profiles: map[string]config.Profile{"prod": {Distribution: "store", Configuration: "Release"}}}, "prod"},
		{&config.Config{Profiles: profiles, DefaultProfile: "development"}, "store"},
	} {
		if got, err := Preflight(tc.cfg, tc.profile, nil); err != nil || got != tc.profile {
			t.Errorf("Preflight(%q) = %q, %v", tc.profile, got, err)
		}
	}
	f := newFake(t)
	for _, opts := range []*Options{{BuildNumber: "1.2.3.4"}, {Version: "v1"}, {BuildNumber: "12a"}} {
		if _, err := Run(context.Background(), releaseConfig(), &fakeBuilder{t: t}, f.client(t), opts); err == nil {
			t.Errorf("accepted %+v", opts)
		}
	}
	if len(f.calls) != 0 {
		t.Errorf("validation reached the network: %v", f.calls)
	}
}

// TestPreflightPicksTheOnlyStoreProfile covers what `signing setup
// --distribution store` leaves behind: one store profile, no defaultProfile.
func TestPreflightPicksTheOnlyStoreProfile(t *testing.T) {
	store := config.Profile{Distribution: "store"}
	dev := config.Profile{Distribution: "development"}
	for name, tc := range map[string]struct {
		cfg     *config.Config
		profile string
		want    string   // the profile the build must use
		logged  bool     // the selection is announced
		errWant []string // non-empty when Preflight must refuse
	}{
		"the only store profile": {
			cfg:  &config.Config{Profiles: map[string]config.Profile{"store": store, "dev": dev}},
			want: "store", logged: true,
		},
		"the only store profile, non-store default": {
			cfg:  &config.Config{DefaultProfile: "dev", Profiles: map[string]config.Profile{"beta": store, "dev": dev}},
			want: "beta", logged: true,
		},
		"two store profiles": {
			cfg:     &config.Config{Profiles: map[string]config.Profile{"beta": store, "prod": store, "dev": dev}},
			errWant: []string{"more than one App Store profile: beta, prod", "--profile"},
		},
		"no store profile": {
			cfg:     &config.Config{Profiles: map[string]config.Profile{"dev": dev}},
			errWant: []string{"no App Store build profile selected", "signing setup --distribution store"},
		},
		"--profile is not second-guessed": {
			cfg:     &config.Config{Profiles: map[string]config.Profile{"store": store, "dev": dev}},
			profile: "dev",
			errWant: []string{`profile "dev" has distribution development`},
		},
		"--profile beats the only store profile": {
			cfg:     &config.Config{Profiles: map[string]config.Profile{"beta": store, "prod": store, "dev": dev}},
			profile: "prod",
			want:    "prod",
		},
		"defaultProfile is a store profile": {
			cfg:  &config.Config{DefaultProfile: "prod", Profiles: map[string]config.Profile{"beta": store, "prod": store}},
			want: "",
		},
	} {
		t.Run(name, func(t *testing.T) {
			var log bytes.Buffer
			got, err := Preflight(tc.cfg, tc.profile, &log)
			if len(tc.errWant) > 0 {
				if err == nil {
					t.Fatalf("Preflight = %q, want an error", got)
				}
				for _, want := range tc.errWant {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("err = %v\nwant %q", err, want)
					}
				}
				if log.Len() != 0 {
					t.Errorf("refused, but announced a profile: %s", log.String())
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("Preflight = %q, %v; want %q", got, err, tc.want)
			}
			if logged := strings.Contains(log.String(), "Using profile "+tc.want+" (the only App Store profile)"); logged != tc.logged {
				t.Errorf("announced = %v, want %v; log = %q", logged, tc.logged, log.String())
			}
		})
	}

	// The selected profile reaches the build, not the empty one Run was given.
	f := newFake(t)
	b := &fakeBuilder{t: t}
	cfg := &config.Config{Project: "App", IOS: config.IOSConfig{BundleID: bundleID}, Profiles: map[string]config.Profile{"beta": store, "dev": dev}}
	var log bytes.Buffer
	if _, err := Run(context.Background(), cfg, b, f.client(t), &Options{
		Build: build.BuildOptions{OutputDir: filepath.Join(t.TempDir(), "dist")}, PollInterval: time.Millisecond, Log: &log,
	}); err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	if b.opts.Profile != "beta" {
		t.Errorf("built with profile %q, want beta", b.opts.Profile)
	}
	if !strings.Contains(log.String(), "Using profile beta (the only App Store profile)") {
		t.Errorf("selection not logged:\n%s", log.String())
	}
}

func TestRunBuildFailureKeepsSnapshotOfProgress(t *testing.T) {
	f := newFake(t)
	b := &fakeBuilder{t: t, err: errors.New("runner exploded")}
	res, err := Run(context.Background(), releaseConfig(), b, f.client(t), &Options{Build: build.BuildOptions{OutputDir: t.TempDir()}})
	if err == nil || !strings.Contains(err.Error(), "runner exploded") || res == nil || res.BuildNumber != "1" {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
}

func TestResolveBundleID(t *testing.T) {
	dir := t.TempDir()
	if _, err := resolveBundleID("", "", dir); err == nil || !strings.Contains(err.Error(), "--bundle-id") {
		t.Errorf("no source: %v", err)
	}
	writeIPA(t, filepath.Join(dir, "old.ipa"), "com.example.old", "1.0", "1")
	if got, err := resolveBundleID("", "", dir); err != nil || got != "com.example.old" {
		t.Errorf("from IPA: %q %v", got, err)
	}
	if got, _ := resolveBundleID("", "com.example.cfg", dir); got != "com.example.cfg" {
		t.Errorf("config must beat the IPA: %q", got)
	}
	if got, _ := resolveBundleID("com.example.flag", "com.example.cfg", dir); got != "com.example.flag" {
		t.Errorf("flag must beat config: %q", got)
	}
}

func TestNextBuildNumber(t *testing.T) {
	for _, tt := range []struct {
		in   []string
		want string
	}{
		{nil, "1"},
		{[]string{"beta", ""}, "1"},
		{[]string{"7", "12", "9"}, "13"},
		{[]string{"1.0.5", "1.0.4"}, "1.0.6"},
		{[]string{"1.0.5", "12"}, "13"},
		{[]string{"2", "1.9.9"}, "3"},
	} {
		if got := nextBuildNumber(tt.in); got != tt.want {
			t.Errorf("nextBuildNumber(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
