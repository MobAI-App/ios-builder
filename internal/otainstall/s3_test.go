package otainstall

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

var fakeCreds = AWSCredentials{AccessKeyID: "AKIDFAKE", SecretAccessKey: "fake/secret+key", SessionToken: "sess"}

type fakeObject struct {
	data        []byte
	contentType string
	marked      bool
}

// fakeS3 is a path-style S3 for bucket "b" that checks every signature the
// way S3 does: it rebuilds the canonical request from what arrived on the wire.
type fakeS3 struct {
	t       *testing.T
	srv     *httptest.Server
	mu      sync.Mutex
	objects map[string]*fakeObject
	log     []string
	// putFails answers PUTs with 403 AccessDenied; deleteFails DELETEs with 500.
	putFails, deleteFails bool
	// pageSize makes listings paginate.
	pageSize int
}

func newFakeS3(t *testing.T) *fakeS3 {
	f := &fakeS3{t: t, objects: map[string]*fakeObject{}, pageSize: 2}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeS3) backend(t *testing.T, prefix string, ttl time.Duration) *Bucket {
	t.Helper()
	b, err := NewS3(&S3Options{Bucket: "b", Region: "auto", Endpoint: f.srv.URL, Prefix: prefix, Credentials: fakeCreds, TTL: ttl, HTTPClient: f.srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var authRe = regexp.MustCompile(`^AWS4-HMAC-SHA256 Credential=AKIDFAKE/(\d{8})/auto/s3/aws4_request, SignedHeaders=([a-z0-9;-]+), Signature=[0-9a-f]{64}$`)

// verify reports why r's signature is wrong, or "".
func (f *fakeS3) verify(r *http.Request) string {
	s := &sigv4{creds: fakeCreds, region: "auto", service: "s3"}
	u := *r.URL
	u.Scheme, u.Host = "http", r.Host
	if q := r.URL.Query(); q.Get("X-Amz-Signature") != "" {
		date, err := time.Parse(amzDateFormat, q.Get("X-Amz-Date"))
		if err != nil {
			return "bad X-Amz-Date"
		}
		secs, _ := strconv.Atoi(q.Get("X-Amz-Expires"))
		if time.Now().After(date.Add(time.Duration(secs) * time.Second)) {
			return "expired"
		}
		got := q.Get("X-Amz-Signature")
		for _, k := range []string{"X-Amz-Signature", "X-Amz-Algorithm", "X-Amz-Credential", "X-Amz-Date", "X-Amz-Expires", "X-Amz-SignedHeaders", "X-Amz-Security-Token"} {
			q.Del(k)
		}
		u.RawQuery = q.Encode()
		want := s.presign(r.Method, &u, time.Duration(secs)*time.Second, date)
		if !strings.HasSuffix(want, "X-Amz-Signature="+got) {
			return "presigned signature mismatch"
		}
		return ""
	}
	m := authRe.FindStringSubmatch(r.Header.Get("Authorization"))
	if m == nil {
		return "no or malformed Authorization: " + r.Header.Get("Authorization")
	}
	date, err := time.Parse(amzDateFormat, r.Header.Get("X-Amz-Date"))
	if err != nil {
		return "bad X-Amz-Date"
	}
	if r.Header.Get("X-Amz-Security-Token") != "sess" {
		return "session token missing"
	}
	req, _ := http.NewRequest(r.Method, u.String(), nil)
	for _, h := range strings.Split(m[2], ";") {
		if h != "host" && h != "x-amz-date" && h != "x-amz-content-sha256" && h != "x-amz-security-token" {
			req.Header.Set(h, r.Header.Get(h))
		}
	}
	s.sign(req, r.Header.Get("X-Amz-Content-Sha256"), date)
	if req.Header.Get("Authorization") != r.Header.Get("Authorization") {
		return "signature mismatch"
	}
	return ""
}

func (f *fakeS3) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, r.Method+" "+r.URL.Path)
	if why := f.verify(r); why != "" {
		f.t.Errorf("%s %s: %s", r.Method, r.URL, why)
		w.WriteHeader(403)
		fmt.Fprint(w, `<Error><Code>SignatureDoesNotMatch</Code><Message>bad</Message></Error>`)
		return
	}
	if r.URL.Path == "/b/" || r.URL.Path == "/b" {
		f.list(w, r)
		return
	}
	key, ok := strings.CutPrefix(r.URL.Path, "/b/")
	if !ok {
		w.WriteHeader(404)
		return
	}
	presigned := r.URL.Query().Get("X-Amz-Signature") != ""
	switch {
	case r.Method == "PUT" && !presigned:
		if f.putFails {
			w.WriteHeader(403)
			fmt.Fprint(w, `<?xml version="1.0"?><Error><Code>AccessDenied</Code><Message>Access Denied</Message></Error>`)
			return
		}
		if r.Header.Get("X-Amz-Content-Sha256") != unsignedPayload {
			f.t.Errorf("PUT payload hash %q", r.Header.Get("X-Amz-Content-Sha256"))
		}
		data, _ := io.ReadAll(r.Body)
		if int64(len(data)) != r.ContentLength {
			f.t.Errorf("PUT Content-Length %d for %d bytes", r.ContentLength, len(data))
		}
		f.objects[key] = &fakeObject{data: data, contentType: r.Header.Get("Content-Type"), marked: r.Header.Get(s3MarkerHeader) == markerValue}
	case r.Method == "DELETE" && !presigned:
		if f.deleteFails {
			w.WriteHeader(500)
			fmt.Fprint(w, `<Error><Code>InternalError</Code><Message>boom</Message></Error>`)
			return
		}
		delete(f.objects, key)
		w.WriteHeader(204)
	case r.Method == "HEAD" && !presigned:
		o := f.objects[key]
		if o == nil {
			w.WriteHeader(404)
			return
		}
		if o.marked {
			w.Header().Set(s3MarkerHeader, markerValue)
		}
	case r.Method == "GET" && presigned: // the device
		o := f.objects[key]
		if o == nil {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", o.contentType)
		_, _ = w.Write(o.data)
	default:
		f.t.Errorf("unexpected %s %s", r.Method, r.URL)
		w.WriteHeader(400)
	}
}

func (f *fakeS3) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("list-type") != "2" {
		f.t.Errorf("list query %v", q)
	}
	var keys []string
	for k := range f.objects {
		if strings.HasPrefix(k, q.Get("prefix")) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	start, _ := strconv.Atoi(q.Get("continuation-token"))
	end := min(start+f.pageSize, len(keys))
	type content struct {
		Key string `xml:"Key"`
	}
	page := struct {
		XMLName               xml.Name  `xml:"ListBucketResult"`
		Contents              []content `xml:"Contents"`
		IsTruncated           bool      `xml:"IsTruncated"`
		NextContinuationToken string    `xml:"NextContinuationToken,omitempty"`
	}{IsTruncated: end < len(keys)}
	for _, k := range keys[start:end] {
		page.Contents = append(page.Contents, content{k})
	}
	if page.IsTruncated {
		page.NextContinuationToken = strconv.Itoa(end)
	}
	_ = xml.NewEncoder(w).Encode(page)
}

func (f *fakeS3) keys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for k := range f.objects {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// install does what iOS does with a link: fetch the manifest, then the IPA it names.
func install(t *testing.T, client *http.Client, links *Links) (manifest string, ipa []byte) {
	t.Helper()
	get := func(u string) []byte {
		resp, err := client.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			t.Fatalf("GET %s: %s", u, resp.Status)
		}
		return data
	}
	escaped := strings.TrimPrefix(links.Link, "itms-services://?action=download-manifest&url=")
	manifestURL, err := url.QueryUnescape(strings.ReplaceAll(escaped, "+", "%2B"))
	if err != nil || manifestURL != links.ManifestURL {
		t.Fatalf("link does not unescape to the manifest URL: %q vs %q (%v)", manifestURL, links.ManifestURL, err)
	}
	manifest = string(get(manifestURL))
	m := regexp.MustCompile(`<string>(http[^<]+\.ipa[^<]*)</string>`).FindStringSubmatch(manifest)
	if m == nil {
		t.Fatalf("no IPA URL in manifest:\n%s", manifest)
	}
	return manifest, get(html.UnescapeString(m[1]))
}

func TestS3SessionRefreshesInstallsAndCleansUp(t *testing.T) {
	f := newFakeS3(t)
	app := testApp(t)
	var log, out syncBuffer
	var progressCalls int
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opts := &Options{App: app, Backend: f.backend(t, "team/ota", 2*time.Minute), Log: &log, JSON: &out, Timeout: time.Minute,
		Progress: func(done, total int64) { progressCalls++ }, refreshLead: 119 * time.Second}
	done := make(chan error, 1)
	go func() {
		_, err := Run(ctx, opts)
		done <- err
	}()
	waitFor(t, "the automatic refresh", func() bool { return out.links() >= 2 })

	links := decodeLinks(t, out.String())
	first, second := links[0], links[1]
	if !strings.HasPrefix(first.ManifestURL, f.srv.URL+"/b/team/ota/"+BucketDir) || !strings.Contains(first.ManifestURL, "/m.plist?") {
		t.Errorf("manifest URL = %s", first.ManifestURL)
	}
	if len(first.Objects) != 2 || !strings.HasPrefix(first.Objects[0], "s3://b/team/ota/ios-builder/") || !strings.HasSuffix(first.Objects[0], "/Tap-Dash-Runner.ipa") {
		t.Errorf("objects = %v", first.Objects)
	}
	if d := time.Until(first.ExpiresAt); d < time.Minute || d > 2*time.Minute {
		t.Errorf("expires in %s, want about two minutes", d)
	}
	// Both links install, the older one serving the newest manifest.
	want, _ := os.ReadFile(app.Path)
	for _, l := range []*Links{&first, &second} {
		manifest, ipa := install(t, f.srv.Client(), l)
		if !bytes.Equal(ipa, want) || !strings.Contains(manifest, "run.mobai.tapdash") {
			t.Errorf("install from %s fetched %d bytes", l.ManifestURL, len(ipa))
		}
	}
	if progressCalls == 0 {
		t.Error("no upload progress")
	}
	if keys := f.keys(); len(keys) != 2 {
		t.Errorf("objects during the session: %v", keys)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if keys := f.keys(); len(keys) != 0 {
		t.Errorf("leftovers: %v", keys)
	}
	if !strings.Contains(log.String(), "Removed the upload and the manifest.") {
		t.Errorf("log: %s", log.String())
	}
}

func TestS3SessionOnceLeavesObjects(t *testing.T) {
	f := newFakeS3(t)
	res, err := Run(context.Background(), &Options{App: testApp(t), Backend: f.backend(t, "", 7*24*time.Hour), Once: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Leftovers) != 2 || !strings.HasSuffix(res.Leftovers[1], "/m.plist") || len(f.keys()) != 2 {
		t.Errorf("leftovers = %v, objects = %v", res.Leftovers, f.keys())
	}
	if !strings.Contains(res.ManifestURL, "X-Amz-Expires=604800") || time.Until(res.ExpiresAt) < 167*time.Hour {
		t.Errorf("seven-day link: %s, expires %s", res.ManifestURL, res.ExpiresAt)
	}
}

func TestS3SessionReportsWhatCleanupCouldNotRemove(t *testing.T) {
	f := newFakeS3(t)
	f.deleteFails = true
	var log syncBuffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan *Result, 1)
	go func() {
		res, err := Run(ctx, &Options{App: testApp(t), Backend: f.backend(t, "", 0), Log: &log, Timeout: time.Minute})
		if err != nil {
			t.Error(err)
		}
		done <- res
	}()
	waitFor(t, "the first link", func() bool { return log.links() == 1 })
	cancel()
	res := <-done
	if len(res.Leftovers) != 2 || !strings.HasPrefix(res.Leftovers[0], "s3://b/ios-builder/") || !strings.Contains(log.String(), "Cleanup failed") {
		t.Errorf("leftovers = %v\n%s", res.Leftovers, log.String())
	}
}

func TestS3UploadErrorNamesTheS3Code(t *testing.T) {
	f := newFakeS3(t)
	f.putFails = true
	_, err := Run(context.Background(), &Options{App: testApp(t), Backend: f.backend(t, "", 0)})
	if err == nil || !strings.Contains(err.Error(), "403 Forbidden AccessDenied: Access Denied") {
		t.Fatalf("err = %v", err)
	}
	if len(f.keys()) != 0 {
		t.Errorf("objects: %v", f.keys())
	}
}

// Cleanup deletes only marked objects in the upload folder under the prefix.
func TestS3CleanupSweepsOnlyMarkedObjectsUnderThePrefix(t *testing.T) {
	f := newFakeS3(t)
	for key, marked := range map[string]bool{
		"ota/ios-builder/old1/App.ipa": true,
		"ota/ios-builder/old1/m.plist": true,
		"ota/ios-builder/old2/m.plist": true,
		"ota/ios-builder/notes.txt":    false, // the user's, no marker
		"ota/release.ipa":              true,  // outside the upload folder
		"ios-builder/x/m.plist":        true,  // outside the prefix
	} {
		f.objects[key] = &fakeObject{marked: marked}
	}
	n, err := f.backend(t, "ota/", 0).Cleanup(context.Background())
	if err != nil || n != 3 {
		t.Fatalf("Cleanup = %d, %v", n, err)
	}
	if got := strings.Join(f.keys(), ","); got != "ios-builder/x/m.plist,ota/ios-builder/notes.txt,ota/release.ipa" {
		t.Errorf("left: %s", got)
	}
}

// A code wider than 80 columns comes with a note, a normal one without.
func TestWideQRNote(t *testing.T) {
	for _, tc := range []struct {
		link string
		note bool
	}{
		{representativeLink, false},
		{Link("https://b.s3.us-east-1.amazonaws.com/ios-builder/x/m.plist?X-Amz-Security-Token=" + strings.Repeat("A", 800)), true},
	} {
		var log bytes.Buffer
		o := &Options{Log: &log, QR: true}
		if err := o.print(&Result{Links: Links{Link: tc.link, ExpiresAt: time.Now()}}); err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(log.String(), "columns wide"); got != tc.note {
			t.Errorf("link of %d characters: note = %v\n%s", len(tc.link), got, log.String())
		}
	}
}

func TestBucketTTLLimits(t *testing.T) {
	for _, ttl := range []time.Duration{time.Minute, 8 * 24 * time.Hour} {
		_, err := NewS3(&S3Options{Bucket: "b", Credentials: fakeCreds, TTL: ttl})
		if err == nil || !strings.Contains(err.Error(), "--ttl must be between") {
			t.Errorf("ttl %s: err = %v", ttl, err)
		}
	}
	b, err := NewS3(&S3Options{Bucket: "b", Credentials: fakeCreds})
	if err != nil || b.ttl != DefaultBucketTTL {
		t.Errorf("default ttl: %v, %v", b, err)
	}
}

func TestS3BaseURL(t *testing.T) {
	for _, tc := range []struct{ bucket, region, endpoint, want string }{
		{"builds", "eu-west-1", "", "https://builds.s3.eu-west-1.amazonaws.com/"},
		{"my.builds", "us-east-1", "", "https://s3.us-east-1.amazonaws.com/my.builds/"},
		{"builds", "auto", "https://acct.r2.cloudflarestorage.com/", "https://acct.r2.cloudflarestorage.com/builds/"},
		{"builds", "us-east-1", "http://localhost:9000", "http://localhost:9000/builds/"},
	} {
		if got, err := s3BaseURL(tc.bucket, tc.region, tc.endpoint); err != nil || got != tc.want {
			t.Errorf("s3BaseURL(%q, %q, %q) = %q, %v", tc.bucket, tc.region, tc.endpoint, got, err)
		}
	}
	if _, err := s3BaseURL("b", "r", "ftp://x"); err == nil {
		t.Error("ftp endpoint accepted")
	}
}

func TestLoadAWSCredentials(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "credentials")
	if err := os.WriteFile(file, []byte("[default]\naws_access_key_id = AKDEF\naws_secret_access_key = SKDEF\n\n# comment\n[ci]\naws_access_key_id=AKCI\naws_secret_access_key=SKCI\naws_session_token=TOK\n[sso]\nsso_start_url = https://x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	for _, tc := range []struct {
		name    string
		env     map[string]string
		want    AWSCredentials
		wantErr string
	}{
		{"env wins", map[string]string{"AWS_ACCESS_KEY_ID": "AKENV", "AWS_SECRET_ACCESS_KEY": "SKENV", "AWS_SESSION_TOKEN": "T", "AWS_SHARED_CREDENTIALS_FILE": file}, AWSCredentials{"AKENV", "SKENV", "T"}, ""},
		{"partial env", map[string]string{"AWS_ACCESS_KEY_ID": "AKENV"}, AWSCredentials{}, "set both"},
		{"default profile", map[string]string{"AWS_SHARED_CREDENTIALS_FILE": file}, AWSCredentials{"AKDEF", "SKDEF", ""}, ""},
		{"named profile", map[string]string{"AWS_SHARED_CREDENTIALS_FILE": file, "AWS_PROFILE": "ci"}, AWSCredentials{"AKCI", "SKCI", "TOK"}, ""},
		{"home", map[string]string{"HOME": dir, "AWS_PROFILE": "ci"}, AWSCredentials{}, "no AWS credentials"},
		{"sso profile", map[string]string{"AWS_SHARED_CREDENTIALS_FILE": file, "AWS_PROFILE": "sso"}, AWSCredentials{}, "SSO and credential_process profiles are not read"},
	} {
		got, err := LoadAWSCredentials(env(tc.env))
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("%s: err = %v", tc.name, err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%s: %+v, %v", tc.name, got, err)
		}
	}
}
