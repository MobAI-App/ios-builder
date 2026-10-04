package otainstall

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"github.com/MobAI-App/ios-builder/internal/config"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

const fakeAzureKey = "ZmFrZS1henVyZS1hY2NvdW50LWtleQ=="

// azureSig is the service SAS signature computed straight from the string-to-
// sign of "Create a service SAS" (version 2020-12-06 and later): sixteen
// fields, the optional ones empty.
func azureSig(perms, expiry, resource, canonical string) string {
	key, _ := base64.StdEncoding.DecodeString(fakeAzureKey)
	toSign := perms + "\n" + // signedPermissions
		"\n" + // signedStart
		expiry + "\n" + // signedExpiry
		canonical + "\n" + // canonicalizedResource
		"\n" + // signedIdentifier
		"\n" + // signedIP
		"\n" + // signedProtocol
		"2022-11-02\n" + // signedVersion
		resource + "\n" + // signedResource
		"\n" + // signedSnapshotTime
		"\n" + // signedEncryptionScope
		"\n\n\n\n" // rscc, rscd, rsce, rscl, then rsct (empty, no newline)
	m := hmac.New(sha256.New, key)
	m.Write([]byte(toSign))
	return base64.StdEncoding.EncodeToString(m.Sum(nil))
}

func TestAzureSASMatchesTheDocumentedStringToSign(t *testing.T) {
	b, err := NewAzure(&AzureOptions{Account: "acct", Container: "builds", Key: fakeAzureKey})
	if err != nil {
		t.Fatal(err)
	}
	store := b.store.(*azureStore)
	exp := time.Date(2026, 10, 4, 13, 0, 0, 0, time.UTC)
	got, _ := url.ParseQuery(store.sas("ios-builder/x/m.plist", "r", exp))
	if got.Get("sig") != azureSig("r", "2026-10-04T13:00:00Z", "b", "/blob/acct/builds/ios-builder/x/m.plist") ||
		got.Get("sv") != "2022-11-02" || got.Get("sr") != "b" || got.Get("sp") != "r" || got.Get("se") != "2026-10-04T13:00:00Z" {
		t.Errorf("blob SAS = %v", got)
	}
	got, _ = url.ParseQuery(store.sas("", "l", exp))
	if got.Get("sig") != azureSig("l", "2026-10-04T13:00:00Z", "c", "/blob/acct/builds") || got.Get("sr") != "c" {
		t.Errorf("container SAS = %v", got)
	}
	u, _ := store.presign("ios-builder/x/m.plist", time.Hour, exp.Add(-time.Hour))
	if !strings.HasPrefix(u, "https://acct.blob.core.windows.net/builds/ios-builder/x/m.plist?sv=2022-11-02&se=2026-10-04T13:00:00Z&sr=b&sp=r&sig=") {
		t.Errorf("presign = %s", u)
	}
}

// fakeAzure is Blob Storage for account "acct", container "c", served under
// /acct like Azurite, checking every SAS.
type fakeAzure struct {
	t           *testing.T
	srv         *httptest.Server
	mu          sync.Mutex
	blobs       map[string]*fakeObject
	deleteFails bool
}

func newFakeAzure(t *testing.T) *fakeAzure {
	f := &fakeAzure{t: t, blobs: map[string]*fakeObject{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAzure) backend(t *testing.T, prefix string, ttl time.Duration) *Bucket {
	t.Helper()
	b, err := NewAzure(&AzureOptions{Account: "acct", Container: "c", Key: fakeAzureKey, Endpoint: f.srv.URL + "/acct", Prefix: prefix, TTL: ttl, HTTPClient: f.srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (f *fakeAzure) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	q := r.URL.Query()
	blob, _ := strings.CutPrefix(r.URL.Path, "/acct/c/")
	canonical, resource := "/blob/acct/c/"+blob, "b"
	if r.URL.Path == "/acct/c" {
		canonical, resource = "/blob/acct/c", "c"
	}
	exp, err := time.Parse(time.RFC3339, q.Get("se"))
	if err != nil || time.Now().After(exp) || q.Get("sr") != resource || q.Get("sig") != azureSig(q.Get("sp"), q.Get("se"), resource, canonical) {
		f.t.Errorf("%s %s: bad SAS", r.Method, r.URL)
		w.Header().Set("X-Ms-Error-Code", "AuthenticationFailed")
		w.WriteHeader(403)
		return
	}
	need := map[string]string{"PUT": "cw", "DELETE": "d", "GET": "r"}[r.Method]
	if resource == "c" {
		need = "l"
	}
	if q.Get("sp") != need {
		f.t.Errorf("%s %s with sp=%s, want %s", r.Method, r.URL.Path, q.Get("sp"), need)
	}
	if r.Method != "GET" || resource == "c" {
		if r.Header.Get("X-Ms-Version") != azureVersion {
			f.t.Errorf("%s without x-ms-version", r.Method)
		}
	}
	switch {
	case resource == "c":
		if q.Get("restype") != "container" || q.Get("comp") != "list" || q.Get("include") != "metadata" {
			f.t.Errorf("list query %v", q)
		}
		var names []string
		for k := range f.blobs {
			if strings.HasPrefix(k, q.Get("prefix")) {
				names = append(names, k)
			}
		}
		sort.Strings(names)
		// One blob per page, to exercise NextMarker.
		start := 0
		if m := q.Get("marker"); m != "" {
			start = sort.SearchStrings(names, m)
		}
		fmt.Fprint(w, `<?xml version="1.0" encoding="utf-8"?><EnumerationResults><Blobs>`)
		if start < len(names) {
			meta := ""
			if f.blobs[names[start]].marked {
				meta = "<iosbuilder>distribute</iosbuilder>"
			}
			fmt.Fprintf(w, `<Blob><Name>%s</Name><Metadata>%s</Metadata></Blob>`, names[start], meta)
		}
		fmt.Fprint(w, `</Blobs>`)
		if start+1 < len(names) {
			fmt.Fprintf(w, `<NextMarker>%s</NextMarker>`, names[start+1])
		} else {
			fmt.Fprint(w, `<NextMarker/>`)
		}
		fmt.Fprint(w, `</EnumerationResults>`)
	case r.Method == "PUT":
		if r.Header.Get("X-Ms-Blob-Type") != "BlockBlob" {
			f.t.Errorf("PUT without x-ms-blob-type")
		}
		data, _ := io.ReadAll(r.Body)
		f.blobs[blob] = &fakeObject{data: data, contentType: r.Header.Get("Content-Type"), marked: r.Header.Get("X-Ms-Meta-Iosbuilder") == markerValue}
		w.WriteHeader(201)
	case r.Method == "DELETE":
		if f.deleteFails {
			w.Header().Set("X-Ms-Error-Code", "InternalError")
			w.WriteHeader(500)
			return
		}
		if f.blobs[blob] == nil {
			w.Header().Set("X-Ms-Error-Code", "BlobNotFound")
			w.WriteHeader(404)
			return
		}
		delete(f.blobs, blob)
		w.WriteHeader(202)
	case r.Method == "GET":
		o := f.blobs[blob]
		if o == nil {
			w.WriteHeader(404)
			return
		}
		_, _ = w.Write(o.data)
	}
}

func (f *fakeAzure) names() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for k := range f.blobs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestAzureSessionInstallsAndCleansUp(t *testing.T) {
	f := newFakeAzure(t)
	app := testApp(t)
	var log syncBuffer
	stdin, keys := io.Pipe()
	done := make(chan *Result, 1)
	go func() {
		res, err := Run(context.Background(), &Options{App: app, Backend: f.backend(t, "ota", 0), Log: &log, Stdin: stdin, Timeout: time.Minute})
		if err != nil {
			t.Error(err)
		}
		done <- res
	}()
	waitFor(t, "the first link", func() bool { return log.links() == 1 })
	if _, err := io.WriteString(keys, "\n"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the refresh", func() bool { return log.links() == 2 })
	if n := len(f.names()); n != 2 {
		t.Errorf("blobs: %v", f.names())
	}
	if _, err := io.WriteString(keys, "q\n"); err != nil {
		t.Fatal(err)
	}
	res := <-done
	if !strings.HasPrefix(res.ManifestURL, f.srv.URL+"/acct/c/ota/ios-builder/") || !strings.HasPrefix(res.Objects[0], "azure://acct/c/ota/ios-builder/") {
		t.Errorf("links = %+v", res.Links)
	}
	if d := time.Until(res.ExpiresAt); d < 59*time.Minute {
		t.Errorf("default TTL: expires in %s", d)
	}
	if len(f.names()) != 0 || len(res.Leftovers) != 0 {
		t.Errorf("left: %v %v", f.names(), res.Leftovers)
	}
}

func TestAzureLinkInstalls(t *testing.T) {
	f := newFakeAzure(t)
	app := testApp(t)
	res, err := Run(context.Background(), &Options{App: app, Backend: f.backend(t, "", 0), Once: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(res.Link, "%25") != strings.Count(res.Link, "%252B") {
		t.Errorf("link has double-escaped characters, which cost QR versions: %s", res.Link)
	}
	_, ipa := install(t, f.srv.Client(), res.Links)
	want, _ := os.ReadFile(app.Path)
	if !bytes.Equal(ipa, want) {
		t.Error("installed IPA differs")
	}
	if len(res.Leftovers) != 2 || !strings.HasPrefix(res.Leftovers[0], "azure://acct/c/ios-builder/") {
		t.Errorf("leftovers = %v", res.Leftovers)
	}
}

func TestAzureCleanupAndLeftovers(t *testing.T) {
	f := newFakeAzure(t)
	for name, marked := range map[string]bool{
		"ios-builder/a/App.ipa": true, "ios-builder/a/m.plist": true, "ios-builder/mine.txt": false, "other/m.plist": true,
	} {
		f.blobs[name] = &fakeObject{marked: marked}
	}
	b := f.backend(t, "", 0)
	n, err := b.Cleanup(context.Background())
	if err != nil || n != 2 {
		t.Fatalf("Cleanup = %d, %v", n, err)
	}
	if got := strings.Join(f.names(), ","); got != "ios-builder/mine.txt,other/m.plist" {
		t.Errorf("left: %s", got)
	}

	f.deleteFails = true
	up, err := b.Upload(context.Background(), testApp(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := up.Close(context.Background()); err == nil || !strings.Contains(err.Error(), "500 Internal Server Error InternalError") {
		t.Errorf("Close: %v", err)
	}
	if left := up.Leftovers(); len(left) != 1 || !strings.HasSuffix(left[0], ".ipa") {
		t.Errorf("leftovers = %v", left)
	}
}

func TestAzureFromConfigReadsTheEnvironment(t *testing.T) {
	env := map[string]string{"AZURE_STORAGE_CONNECTION_STRING": "DefaultEndpointsProtocol=https;AccountName=fromcs;AccountKey=" + fakeAzureKey + ";EndpointSuffix=core.windows.net"}
	b, err := AzureFromConfig(nil, 0, func(k string) string { return env[k] })
	if err == nil || !strings.Contains(err.Error(), "container") {
		t.Fatalf("no container: %v, %v", b, err)
	}
	cfgB, err := AzureFromConfig(&config.DistributeConfig{Container: "c"}, 0, func(k string) string { return env[k] })
	if err != nil || cfgB.store.(*azureStore).account != "fromcs" {
		t.Fatalf("connection string: %v", err)
	}
	env = map[string]string{"AZURE_STORAGE_ACCOUNT": "acct"}
	if _, err := AzureFromConfig(&config.DistributeConfig{Container: "c"}, 0, func(k string) string { return env[k] }); err == nil || !strings.Contains(err.Error(), "AZURE_STORAGE_KEY") {
		t.Errorf("no key: %v", err)
	}
}
