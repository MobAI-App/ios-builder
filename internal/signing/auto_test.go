package signing

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MobAI-App/ios-builder/internal/asc"
	pkcs12 "software.sslmate.com/src/go-pkcs12"
)

var testNow = time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)

type certRec struct {
	id, typ string
	der     []byte
	exp     time.Time
}

type deviceRec struct{ id, name, udid, status string }

type profileRec struct {
	id, name, typ, state string
	certIDs, deviceIDs   []string
	exp                  time.Time
}

// portal is an in-memory Apple Developer portal behind the ASC endpoints Auto uses.
type portal struct {
	t      *testing.T
	srv    *httptest.Server
	signer *rsa.PrivateKey
	mu     sync.Mutex
	calls  []string
	seq    int

	bundleIDs []string // registered identifiers
	certs     []certRec
	devices   []deviceRec
	profiles  []profileRec
	// refuseCertificates / refuseDevices make the POST fail with Apple's quota wording.
	refuseCertificates, refuseDevices bool
}

func newPortal(t *testing.T) *portal {
	t.Helper()
	signer, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &portal{t: t, signer: signer}
	mux := http.NewServeMux()
	res := func(typ, id string, attrs map[string]any) map[string]any {
		return map[string]any{"type": typ, "id": id, "attributes": attrs}
	}
	many := func(w http.ResponseWriter, rs ...any) {
		if rs == nil {
			rs = []any{}
		}
		writeJSON(w, 200, map[string]any{"data": rs})
	}
	refuse := func(w http.ResponseWriter, detail string) {
		writeJSON(w, 409, map[string]any{"errors": []map[string]any{{"status": "409", "code": "ENTITY_ERROR.ATTRIBUTE.INVALID", "title": "There is a problem with the request entity", "detail": detail}}})
	}
	body := func(r *http.Request) map[string]any {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		return b
	}
	attrs := func(b map[string]any) map[string]any { return obj(p.t, b, "data", "attributes") }
	str := func(m map[string]any, key string) string {
		s, _ := m[key].(string)
		return s
	}
	linkIDs := func(b map[string]any, rel string) []string {
		rels := obj(p.t, b, "data", "relationships")
		raw, ok := rels[rel]
		if !ok {
			return nil
		}
		var ids []string
		for _, l := range arr(p.t, raw, "data") {
			ids = append(ids, str(obj(p.t, l), "id"))
		}
		return ids
	}
	wrap := func(h func(w http.ResponseWriter, r *http.Request)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				p.t.Errorf("%s %s without bearer token", r.Method, r.URL.Path)
			}
			p.mu.Lock()
			defer p.mu.Unlock()
			p.calls = append(p.calls, r.Method+" "+r.URL.Path)
			h(w, r)
		}
	}
	certRes := func(c certRec) map[string]any {
		return res("certificates", c.id, map[string]any{"certificateType": c.typ, "name": "Apple " + c.typ + ": Builder", "serialNumber": c.id, "certificateContent": base64.StdEncoding.EncodeToString(c.der), "expirationDate": c.exp.Format(time.RFC3339)})
	}
	deviceRes := func(d deviceRec) map[string]any {
		return res("devices", d.id, map[string]any{"name": d.name, "udid": d.udid, "platform": "IOS", "status": d.status, "deviceClass": "IPHONE"})
	}
	profileRes := func(pr profileRec) map[string]any {
		return res("profiles", pr.id, map[string]any{"name": pr.name, "profileType": pr.typ, "profileState": pr.state, "uuid": "uuid-" + pr.id, "platform": "IOS", "profileContent": base64.StdEncoding.EncodeToString([]byte("profile:" + pr.id)), "expirationDate": pr.exp.Format(time.RFC3339)})
	}

	mux.HandleFunc("GET /v1/bundleIds", wrap(func(w http.ResponseWriter, r *http.Request) {
		want := r.URL.Query().Get("filter[identifier]")
		var rs []any
		for _, id := range p.bundleIDs {
			if strings.HasPrefix(id, want) {
				rs = append(rs, res("bundleIds", "bid-"+id, map[string]any{"identifier": id, "name": bundleIDName(id), "platform": "IOS"}))
			}
		}
		many(w, rs...)
	}))
	mux.HandleFunc("POST /v1/bundleIds", wrap(func(w http.ResponseWriter, r *http.Request) {
		a := attrs(body(r))
		if a["platform"] != "IOS" || a["name"] == "" {
			p.t.Errorf("bundleIds POST attributes = %v", a)
		}
		id := str(a, "identifier")
		p.bundleIDs = append(p.bundleIDs, id)
		writeJSON(w, 201, map[string]any{"data": res("bundleIds", "bid-"+id, a)})
	}))
	mux.HandleFunc("GET /v1/certificates", wrap(func(w http.ResponseWriter, r *http.Request) {
		var rs []any
		for _, c := range p.certs {
			if c.typ == r.URL.Query().Get("filter[certificateType]") {
				rs = append(rs, certRes(c))
			}
		}
		many(w, rs...)
	}))
	mux.HandleFunc("POST /v1/certificates", wrap(func(w http.ResponseWriter, r *http.Request) {
		if p.refuseCertificates {
			refuse(w, "You already have a current Development certificate or a pending certificate request; the maximum number of certificates has been reached.")
			return
		}
		a := attrs(body(r))
		block, _ := pem.Decode([]byte(str(a, "csrContent")))
		if block == nil {
			p.t.Fatalf("csrContent is not PEM: %v", a["csrContent"])
		}
		csr, err := x509.ParseCertificateRequest(block.Bytes)
		if err != nil {
			p.t.Fatalf("parse CSR: %v", err)
		}
		if err := csr.CheckSignature(); err != nil {
			p.t.Fatalf("CSR signature: %v", err)
		}
		pub, ok := csr.PublicKey.(*rsa.PublicKey)
		if !ok {
			p.t.Fatalf("CSR public key is %T, want RSA", csr.PublicKey)
		}
		c := p.issue(str(a, "certificateType"), pub, testNow.AddDate(1, 0, 0))
		writeJSON(w, 201, map[string]any{"data": certRes(c)})
	}))
	mux.HandleFunc("GET /v1/devices", wrap(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("filter[platform]") != "IOS" {
			p.t.Errorf("devices query = %v", r.URL.Query())
		}
		var rs []any
		for _, d := range p.devices {
			rs = append(rs, deviceRes(d))
		}
		many(w, rs...)
	}))
	mux.HandleFunc("POST /v1/devices", wrap(func(w http.ResponseWriter, r *http.Request) {
		if p.refuseDevices {
			refuse(w, "You have reached the maximum number of devices for this membership year.")
			return
		}
		a := attrs(body(r))
		d := deviceRec{id: p.nextID("dev"), name: str(a, "name"), udid: str(a, "udid"), status: "ENABLED"}
		p.devices = append(p.devices, d)
		writeJSON(w, 201, map[string]any{"data": deviceRes(d)})
	}))
	mux.HandleFunc("GET /v1/profiles", wrap(func(w http.ResponseWriter, r *http.Request) {
		var rs []any
		for i := range p.profiles {
			if p.profiles[i].name == r.URL.Query().Get("filter[name]") {
				rs = append(rs, profileRes(p.profiles[i]))
			}
		}
		many(w, rs...)
	}))
	mux.HandleFunc("GET /v1/profiles/{id}/relationships/{rel}", wrap(func(w http.ResponseWriter, r *http.Request) {
		for i := range p.profiles {
			pr := &p.profiles[i]
			if pr.id != r.PathValue("id") {
				continue
			}
			ids, typ := pr.certIDs, "certificates"
			if r.PathValue("rel") == "devices" {
				ids, typ = pr.deviceIDs, "devices"
			}
			var rs []any
			for _, id := range ids {
				rs = append(rs, map[string]any{"type": typ, "id": id})
			}
			many(w, rs...)
			return
		}
		writeJSON(w, 404, map[string]any{"errors": []map[string]any{{"code": "NOT_FOUND", "title": "not found"}}})
	}))
	mux.HandleFunc("POST /v1/profiles", wrap(func(w http.ResponseWriter, r *http.Request) {
		b := body(r)
		a := attrs(b)
		bundle, ok := obj(p.t, b, "data", "relationships", "bundleId", "data")["id"].(string)
		if !ok || !slices.Contains(p.bundleIDs, strings.TrimPrefix(bundle, "bid-")) {
			p.t.Errorf("profile POST for unknown bundle ID %q", bundle)
		}
		pr := profileRec{id: p.nextID("prof"), name: str(a, "name"), typ: str(a, "profileType"), state: "ACTIVE", certIDs: linkIDs(b, "certificates"), deviceIDs: linkIDs(b, "devices"), exp: testNow.AddDate(1, 0, 0)}
		if pr.typ == asc.ProfileTypeIOSAppStore && pr.deviceIDs != nil {
			p.t.Errorf("App Store profile POST carries devices: %v", pr.deviceIDs)
		}
		p.profiles = append(p.profiles, pr)
		writeJSON(w, 201, map[string]any{"data": profileRes(pr)})
	}))
	mux.HandleFunc("DELETE /v1/profiles/{id}", wrap(func(w http.ResponseWriter, r *http.Request) {
		p.profiles = slices.DeleteFunc(p.profiles, func(pr profileRec) bool { return pr.id == r.PathValue("id") })
		w.WriteHeader(204)
	}))
	mux.HandleFunc("/", wrap(func(w http.ResponseWriter, r *http.Request) {
		p.t.Errorf("unexpected request %s %s", r.Method, r.URL)
		w.WriteHeader(404)
	}))
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	return p
}

func (p *portal) nextID(prefix string) string {
	p.seq++
	return fmt.Sprintf("%s-%d", prefix, p.seq)
}

// issue signs a certificate for pub and records it; callers hold p.mu or run before the server.
func (p *portal) issue(typ string, pub *rsa.PublicKey, exp time.Time) certRec {
	c := certRec{id: p.nextID("cert"), typ: typ, der: issueCert(p.t, pub, p.signer), exp: exp}
	p.certs = append(p.certs, c)
	return c
}

func (p *portal) client(t *testing.T) *asc.Client {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	creds := asc.Credentials{IssuerID: "iss", KeyID: "kid", PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))}
	c, err := asc.NewClient(creds, asc.WithBaseURL(p.srv.URL), asc.WithRetryDelay(time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// count returns how many recorded calls match "METHOD /path".
func (p *portal) count(key string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, c := range p.calls {
		if c == key {
			n++
		}
	}
	return n
}

func (p *portal) reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

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

func arr(t *testing.T, v any, keys ...string) []any {
	t.Helper()
	if len(keys) > 0 {
		v = obj(t, v, keys[:len(keys)-1]...)[keys[len(keys)-1]]
	}
	a, ok := v.([]any)
	if !ok {
		t.Errorf("JSON path %v: %T is not an array", keys, v)
	}
	return a
}

func devOpts(dir string) *AutoOptions {
	return &AutoOptions{
		BundleID: "com.example.app",
		Type:     TypeDevelopment,
		Devices:  []Device{{Name: "Jane's iPhone", UDID: "00008030-000000000000001E"}},
		Password: "secret",
		OutDir:   dir,
		now:      func() time.Time { return testNow },
	}
}

func run(t *testing.T, p *portal, opts *AutoOptions) *AutoResult {
	t.Helper()
	p.reset()
	res, err := Auto(context.Background(), p.client(t), opts)
	if err != nil {
		t.Fatalf("Auto: %v", err)
	}
	return res
}

func TestAutoFirstRunCreatesEverything(t *testing.T) {
	p := newPortal(t)
	dir := t.TempDir()
	res := run(t, p, devOpts(dir))

	if !res.BundleID.Created || res.BundleID.ID != "bid-com.example.app" {
		t.Errorf("bundle ID = %+v", res.BundleID)
	}
	if !res.Certificate.Created || res.Certificate.Type != asc.CertificateTypeDevelopment || res.Certificate.ValidOnAccount != 0 {
		t.Errorf("certificate = %+v", res.Certificate)
	}
	if len(res.Devices.Registered) != 1 || res.Devices.Registered[0].Name != "Jane's iPhone" || res.Devices.InProfile != 1 {
		t.Errorf("devices = %+v", res.Devices)
	}
	if !res.Profile.Created || res.Profile.Reason != "missing" || res.Profile.Name != "Builder development com.example.app" || res.Profile.Type != asc.ProfileTypeIOSAppDevelopment || res.Profile.State != asc.ProfileStateActive {
		t.Errorf("profile = %+v", res.Profile)
	}
	if p.count("DELETE /v1/profiles/prof-3") != 0 || p.count("POST /v1/profiles") != 1 || p.count("POST /v1/certificates") != 1 || p.count("POST /v1/devices") != 1 || p.count("POST /v1/bundleIds") != 1 {
		t.Errorf("calls = %v", p.calls)
	}

	// Files: key, .p12 and profile in the output directory; the .p12 opens with the password and holds the issued certificate.
	if res.Files.Key != filepath.Join(dir, "ios-signing-development.key") || res.Files.P12 != filepath.Join(dir, "ios-signing-development.p12") || res.Files.Profile != filepath.Join(dir, "Builder-development-com.example.app.mobileprovision") {
		t.Errorf("files = %+v", res.Files)
	}
	keyPEM, err := os.ReadFile(res.Files.Key)
	if err != nil {
		t.Fatal(err)
	}
	p12, err := os.ReadFile(res.Files.P12)
	if err != nil {
		t.Fatal(err)
	}
	gotKey, gotCert, err := pkcs12.Decode(p12, "secret")
	if err != nil {
		t.Fatalf("pkcs12.Decode: %v", err)
	}
	rsaKey, ok := gotKey.(*rsa.PrivateKey)
	if !ok || !KeyMatchesCertificate(keyPEM, gotCert.Raw) || !rsaKey.PublicKey.Equal(gotCert.PublicKey) {
		t.Error(".p12 key and certificate do not match the written key")
	}
	if !slices.Equal(gotCert.Raw, p.certs[0].der) {
		t.Error(".p12 holds a different certificate than the portal issued")
	}
	profile, err := os.ReadFile(res.Files.Profile)
	if err != nil || string(profile) != "profile:prof-3" || string(res.ProfileContent) != "profile:prof-3" {
		t.Errorf("profile file = %q, err = %v", profile, err)
	}
}

func TestAutoSecondRunReusesEverything(t *testing.T) {
	p := newPortal(t)
	dir := t.TempDir()
	first := run(t, p, devOpts(dir))
	keyPEM, err := os.ReadFile(first.Files.Key)
	if err != nil {
		t.Fatal(err)
	}

	opts := devOpts(dir)
	opts.KeyPEM = keyPEM
	res := run(t, p, opts)
	if res.BundleID.Created || res.Certificate.Created || res.Profile.Created || res.Profile.Reason != "" || len(res.Devices.Registered) != 0 {
		t.Errorf("second run created something: %+v", res)
	}
	if res.Certificate.ID != first.Certificate.ID || res.Profile.ID != first.Profile.ID || res.Certificate.ValidOnAccount != 1 {
		t.Errorf("second run = %+v, first = %+v", res, first)
	}
	if res.Files.Key != "" {
		t.Errorf("a supplied key must not be rewritten: %+v", res.Files)
	}
	for _, call := range p.calls {
		if strings.HasPrefix(call, "POST") || strings.HasPrefix(call, "DELETE") {
			t.Errorf("second run made %s", call)
		}
	}
	if _, err := os.Stat(res.Files.P12); err != nil {
		t.Errorf(".p12 not rewritten: %v", err)
	}
}

func TestAutoRecreatesProfileWhenDevicesChange(t *testing.T) {
	p := newPortal(t)
	dir := t.TempDir()
	first := run(t, p, devOpts(dir))
	keyPEM, _ := os.ReadFile(first.Files.Key)

	opts := devOpts(dir)
	opts.KeyPEM = keyPEM
	opts.Devices = append(opts.Devices, Device{UDID: "00008110-00000000000000AB"})
	res := run(t, p, opts)
	if res.Certificate.Created || !res.Profile.Created || res.Profile.Reason != "devices changed" || res.Devices.InProfile != 2 {
		t.Errorf("result = %+v", res)
	}
	if len(res.Devices.Registered) != 1 || res.Devices.Registered[0].Name != "iPhone 0000AB" {
		t.Errorf("registered = %+v (want the default name from the UDID)", res.Devices.Registered)
	}
	if p.count("DELETE /v1/profiles/"+first.Profile.ID) != 1 || p.count("POST /v1/profiles") != 1 || len(p.profiles) != 1 {
		t.Errorf("calls = %v, profiles = %+v", p.calls, p.profiles)
	}
	if len(p.profiles[0].deviceIDs) != 2 {
		t.Errorf("new profile devices = %v", p.profiles[0].deviceIDs)
	}
}

func TestAutoRecreatesInvalidProfile(t *testing.T) {
	p := newPortal(t)
	dir := t.TempDir()
	first := run(t, p, devOpts(dir))
	keyPEM, _ := os.ReadFile(first.Files.Key)
	p.profiles[0].state = asc.ProfileStateInvalid

	opts := devOpts(dir)
	opts.KeyPEM = keyPEM
	res := run(t, p, opts)
	if res.Certificate.Created || !res.Profile.Created || res.Profile.Reason != "invalid" || res.Profile.ID == first.Profile.ID {
		t.Errorf("result = %+v", res)
	}
	// An INVALID profile needs no relationship lookups to be condemned.
	if p.count("GET /v1/profiles/"+first.Profile.ID+"/relationships/certificates") != 0 {
		t.Errorf("calls = %v", p.calls)
	}
}

func TestAutoRecreatesProfileWhenCertificateChanges(t *testing.T) {
	p := newPortal(t)
	dir := t.TempDir()
	first := run(t, p, devOpts(dir))

	// No key on disk any more: a new certificate is issued and the profile
	// follows it; the old certificate stays on the account.
	res := run(t, p, devOpts(t.TempDir()))
	if !res.Certificate.Created || res.Certificate.ID == first.Certificate.ID || res.Certificate.ValidOnAccount != 1 {
		t.Errorf("certificate = %+v", res.Certificate)
	}
	if !res.Profile.Created || res.Profile.Reason != "certificate changed" {
		t.Errorf("profile = %+v", res.Profile)
	}
	if len(p.certs) != 2 || p.profiles[0].certIDs[0] != res.Certificate.ID {
		t.Errorf("certs = %d, profile certs = %v", len(p.certs), p.profiles[0].certIDs)
	}
}

func TestAutoForceIssuesNewCertificateAndProfile(t *testing.T) {
	p := newPortal(t)
	dir := t.TempDir()
	first := run(t, p, devOpts(dir))
	keyPEM, _ := os.ReadFile(first.Files.Key)

	opts := devOpts(dir)
	opts.KeyPEM = keyPEM
	opts.Force = true
	res := run(t, p, opts)
	if !res.Certificate.Created || res.Certificate.ID == first.Certificate.ID || !res.Profile.Created || res.Profile.Reason != "forced" {
		t.Errorf("result = %+v", res)
	}
	if len(p.certs) != 2 {
		t.Errorf("force must not revoke the old certificate: %d certificates left", len(p.certs))
	}
	if !KeyMatchesCertificate(keyPEM, p.certs[1].der) {
		t.Error("the new certificate must be issued for the supplied key")
	}
}

func TestAutoAppStoreNeedsNoDevices(t *testing.T) {
	p := newPortal(t)
	p.bundleIDs = []string{"com.example.app.widget", "com.example.app"}
	opts := devOpts(t.TempDir())
	opts.Type = TypeAppStore
	opts.Devices = nil
	res := run(t, p, opts)
	if res.BundleID.Created || res.BundleID.ID != "bid-com.example.app" {
		t.Errorf("bundle ID = %+v (must match the exact identifier)", res.BundleID)
	}
	if res.Certificate.Type != asc.CertificateTypeDistribution || res.Profile.Type != asc.ProfileTypeIOSAppStore || res.Profile.Name != "Builder app-store com.example.app" {
		t.Errorf("result = %+v", res)
	}
	if res.Devices.InProfile != 0 || p.count("GET /v1/devices") != 0 || p.profiles[0].deviceIDs != nil {
		t.Errorf("App Store setup touched devices: %+v, calls %v", res.Devices, p.calls)
	}
}

func TestAutoDevelopmentWithoutDevicesFails(t *testing.T) {
	p := newPortal(t)
	opts := devOpts(t.TempDir())
	opts.Devices = nil
	_, err := Auto(context.Background(), p.client(t), opts)
	if err == nil || !strings.Contains(err.Error(), "--device") || !strings.Contains(err.Error(), "--devices-from-mobai") {
		t.Errorf("err = %v", err)
	}
	// Devices are checked before the certificate: no device means no key
	// written and no certificate slot spent.
	if p.count("POST /v1/certificates") != 0 || p.count("POST /v1/profiles") != 0 {
		t.Errorf("certificate or profile created without devices: %v", p.calls)
	}
	if _, err := os.Stat(filepath.Join(opts.OutDir, KeyFileName(TypeDevelopment))); err == nil {
		t.Error("a key was written although no certificate was requested")
	}
}

func TestAutoDisabledDevicesStayOutOfProfile(t *testing.T) {
	p := newPortal(t)
	p.devices = []deviceRec{
		{id: "dev-old", name: "Old", udid: "00008020-0000000000000001", status: "DISABLED"},
		{id: "dev-ok", name: "Jane's iPhone", udid: "00008030-000000000000001e", status: "ENABLED"},
	}
	res := run(t, p, devOpts(t.TempDir()))
	if len(res.Devices.Registered) != 0 {
		t.Errorf("UDID matching must be case-insensitive: registered %+v", res.Devices.Registered)
	}
	if res.Devices.InProfile != 1 || !slices.Equal(p.profiles[0].deviceIDs, []string{"dev-ok"}) {
		t.Errorf("profile devices = %v", p.profiles[0].deviceIDs)
	}
}

func TestAutoWithSuppliedKeyReusesMatchingCertificate(t *testing.T) {
	p := newPortal(t)
	keyPEM, _, err := GenerateKeyAndCSR("Jane", "jane@example.com")
	if err != nil {
		t.Fatal(err)
	}
	key, _ := parseKey(keyPEM)
	p.issue(asc.CertificateTypeDevelopment, &key.PublicKey, testNow.AddDate(0, 6, 0))
	// An expired one for the same key must not be picked.
	expired := p.issue(asc.CertificateTypeDevelopment, &key.PublicKey, testNow.AddDate(0, -1, 0))

	opts := devOpts(t.TempDir())
	opts.KeyPEM = keyPEM
	res := run(t, p, opts)
	if res.Certificate.Created || res.Certificate.ID != "cert-1" || res.Certificate.ID == expired.id || res.Certificate.ValidOnAccount != 1 {
		t.Errorf("certificate = %+v", res.Certificate)
	}
	if p.count("POST /v1/certificates") != 0 {
		t.Errorf("calls = %v", p.calls)
	}
}

func TestAutoCertificateLimitHint(t *testing.T) {
	p := newPortal(t)
	p.refuseCertificates = true
	dir := t.TempDir()
	res, err := Auto(context.Background(), p.client(t), devOpts(dir))
	if err == nil || !strings.Contains(err.Error(), "maximum number of certificates") || !strings.Contains(err.Error(), "Revoke one") || !strings.Contains(err.Error(), "--key") {
		t.Errorf("err = %v", err)
	}
	// The key is on disk before the request goes out, so whatever Apple did
	// with it, the next run can carry on with the same key.
	keyPEM, readErr := os.ReadFile(res.Files.Key)
	if readErr != nil || res.Files.Key != filepath.Join(dir, KeyFileName(TypeDevelopment)) {
		t.Fatalf("key after a refused certificate: %+v, %v", res.Files, readErr)
	}
	p.refuseCertificates = false
	opts := devOpts(dir)
	opts.KeyPEM = keyPEM
	res = run(t, p, opts)
	if !res.Certificate.Created || !KeyMatchesCertificate(keyPEM, p.certs[0].der) || res.Files.Key != "" {
		t.Errorf("retry = %+v", res)
	}
}

func TestAutoDeviceLimitHint(t *testing.T) {
	p := newPortal(t)
	p.refuseDevices = true
	_, err := Auto(context.Background(), p.client(t), devOpts(t.TempDir()))
	if err == nil || !strings.Contains(err.Error(), "00008030-000000000000001E") || !strings.Contains(err.Error(), "100 iOS devices") {
		t.Errorf("err = %v", err)
	}
}

func TestAutoRejectsBadOptions(t *testing.T) {
	p := newPortal(t)
	for name, mutate := range map[string]func(*AutoOptions){
		"no bundle ID": func(o *AutoOptions) { o.BundleID = "" },
		"bad type":     func(o *AutoOptions) { o.Type = "enterprise" },
		"no password":  func(o *AutoOptions) { o.Password = "" },
	} {
		opts := devOpts(t.TempDir())
		mutate(opts)
		if _, err := Auto(context.Background(), p.client(t), opts); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if len(p.calls) != 0 {
		t.Errorf("invalid options reached the API: %v", p.calls)
	}
}

// A second type in the same directory leaves the first type's key, .p12 and
// profile in place: each set is a separate file trio.
func TestAutoTypesKeepSeparateFiles(t *testing.T) {
	p := newPortal(t)
	dir := t.TempDir()
	dev := run(t, p, devOpts(dir))
	opts := devOpts(dir)
	opts.Type, opts.Devices = TypeAppStore, nil
	store := run(t, p, opts)
	if dev.Files.Key == store.Files.Key || dev.Files.P12 == store.Files.P12 || dev.Files.Profile == store.Files.Profile {
		t.Fatalf("files collide: %+v vs %+v", dev.Files, store.Files)
	}
	for _, f := range []string{dev.Files.Key, dev.Files.P12, dev.Files.Profile, store.Files.Key, store.Files.P12, store.Files.Profile} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	if len(p.profiles) != 2 || len(p.certs) != 2 {
		t.Errorf("one certificate and profile per type expected: %d profiles, %d certificates", len(p.profiles), len(p.certs))
	}
}

func TestAutoRefusesEnterprise(t *testing.T) {
	p := newPortal(t)
	opts := devOpts(t.TempDir())
	opts.Type = TypeEnterprise
	if _, err := Auto(context.Background(), p.client(t), opts); err == nil || !strings.Contains(err.Error(), "--certificate") || len(p.calls) != 0 {
		t.Errorf("err = %v, calls %v", err, p.calls)
	}
}

func TestBundleIDName(t *testing.T) {
	for in, want := range map[string]string{"com.example.app": "com example app", "com.example.my-app_2": "com example my app 2", "App": "App"} {
		if got := bundleIDName(in); got != want {
			t.Errorf("bundleIDName(%q) = %q, want %q", in, got, want)
		}
	}
}
