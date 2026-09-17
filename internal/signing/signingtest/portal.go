// Package signingtest is an in-memory Apple Developer portal behind the App
// Store Connect endpoints signing.Auto uses, for tests of the provisioning
// flow in any package. It must not import internal/signing, whose own tests
// use it.
package signingtest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MobAI-App/ios-builder/internal/asc"
)

// Now is the clock the portal dates its certificates and profiles from; pass
// it as the AutoOptions clock so expiry checks agree with the portal.
var Now = time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)

// Cert is a certificate the portal issued.
type Cert struct {
	ID, Type string
	DER      []byte
	Exp      time.Time
}

// Device is a registered device.
type Device struct{ ID, Name, UDID, Status string }

// Profile is a provisioning profile on the portal; its content is
// "profile:<id>".
type Profile struct {
	ID, Name, Type, State string
	CertIDs, DeviceIDs    []string
	Exp                   time.Time
}

// Portal serves the endpoints and records every call.
type Portal struct {
	t      *testing.T
	srv    *httptest.Server
	signer *rsa.PrivateKey
	mu     sync.Mutex
	calls  []string
	seq    int

	BundleIDs []string // registered identifiers
	Certs     []Cert
	Devices   []Device
	Profiles  []Profile
	// RefuseCertificates / RefuseDevices make the POST fail with Apple's quota wording.
	RefuseCertificates, RefuseDevices bool
}

// New starts a portal that closes with the test.
func New(t *testing.T) *Portal {
	t.Helper()
	signer, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &Portal{t: t, signer: signer}
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
	attrs := func(b map[string]any) map[string]any { return Obj(p.t, b, "data", "attributes") }
	str := func(m map[string]any, key string) string {
		s, _ := m[key].(string)
		return s
	}
	linkIDs := func(b map[string]any, rel string) []string {
		rels := Obj(p.t, b, "data", "relationships")
		raw, ok := rels[rel]
		if !ok {
			return nil
		}
		var ids []string
		for _, l := range Arr(p.t, raw, "data") {
			ids = append(ids, str(Obj(p.t, l), "id"))
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
	certRes := func(c Cert) map[string]any {
		return res("certificates", c.ID, map[string]any{"certificateType": c.Type, "name": "Apple " + c.Type + ": Builder", "serialNumber": c.ID, "certificateContent": base64.StdEncoding.EncodeToString(c.DER), "expirationDate": c.Exp.Format(time.RFC3339)})
	}
	deviceRes := func(d Device) map[string]any {
		return res("devices", d.ID, map[string]any{"name": d.Name, "udid": d.UDID, "platform": "IOS", "status": d.Status, "deviceClass": "IPHONE"})
	}
	profileRes := func(pr Profile) map[string]any {
		return res("profiles", pr.ID, map[string]any{"name": pr.Name, "profileType": pr.Type, "profileState": pr.State, "uuid": "uuid-" + pr.ID, "platform": "IOS", "profileContent": base64.StdEncoding.EncodeToString([]byte("profile:" + pr.ID)), "expirationDate": pr.Exp.Format(time.RFC3339)})
	}

	mux.HandleFunc("GET /v1/bundleIds", wrap(func(w http.ResponseWriter, r *http.Request) {
		want := r.URL.Query().Get("filter[identifier]")
		var rs []any
		for _, id := range p.BundleIDs {
			if strings.HasPrefix(id, want) {
				rs = append(rs, res("bundleIds", "bid-"+id, map[string]any{"identifier": id, "name": strings.ReplaceAll(id, ".", " "), "platform": "IOS"}))
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
		p.BundleIDs = append(p.BundleIDs, id)
		writeJSON(w, 201, map[string]any{"data": res("bundleIds", "bid-"+id, a)})
	}))
	mux.HandleFunc("GET /v1/certificates", wrap(func(w http.ResponseWriter, r *http.Request) {
		var rs []any
		for _, c := range p.Certs {
			if c.Type == r.URL.Query().Get("filter[certificateType]") {
				rs = append(rs, certRes(c))
			}
		}
		many(w, rs...)
	}))
	mux.HandleFunc("POST /v1/certificates", wrap(func(w http.ResponseWriter, r *http.Request) {
		if p.RefuseCertificates {
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
		c := p.issue(str(a, "certificateType"), pub, Now.AddDate(1, 0, 0))
		writeJSON(w, 201, map[string]any{"data": certRes(c)})
	}))
	mux.HandleFunc("GET /v1/devices", wrap(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("filter[platform]") != "IOS" {
			p.t.Errorf("devices query = %v", r.URL.Query())
		}
		var rs []any
		for _, d := range p.Devices {
			rs = append(rs, deviceRes(d))
		}
		many(w, rs...)
	}))
	mux.HandleFunc("POST /v1/devices", wrap(func(w http.ResponseWriter, r *http.Request) {
		if p.RefuseDevices {
			refuse(w, "You have reached the maximum number of devices for this membership year.")
			return
		}
		a := attrs(body(r))
		d := Device{ID: p.nextID("dev"), Name: str(a, "name"), UDID: str(a, "udid"), Status: "ENABLED"}
		p.Devices = append(p.Devices, d)
		writeJSON(w, 201, map[string]any{"data": deviceRes(d)})
	}))
	mux.HandleFunc("GET /v1/profiles", wrap(func(w http.ResponseWriter, r *http.Request) {
		var rs []any
		for i := range p.Profiles {
			if p.Profiles[i].Name == r.URL.Query().Get("filter[name]") {
				rs = append(rs, profileRes(p.Profiles[i]))
			}
		}
		many(w, rs...)
	}))
	mux.HandleFunc("GET /v1/profiles/{id}/relationships/{rel}", wrap(func(w http.ResponseWriter, r *http.Request) {
		for i := range p.Profiles {
			pr := &p.Profiles[i]
			if pr.ID != r.PathValue("id") {
				continue
			}
			ids, typ := pr.CertIDs, "certificates"
			if r.PathValue("rel") == "devices" {
				ids, typ = pr.DeviceIDs, "devices"
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
		bundle, ok := Obj(p.t, b, "data", "relationships", "bundleId", "data")["id"].(string)
		if !ok || !slices.Contains(p.BundleIDs, strings.TrimPrefix(bundle, "bid-")) {
			p.t.Errorf("profile POST for unknown bundle ID %q", bundle)
		}
		pr := Profile{ID: p.nextID("prof"), Name: str(a, "name"), Type: str(a, "profileType"), State: "ACTIVE", CertIDs: linkIDs(b, "certificates"), DeviceIDs: linkIDs(b, "devices"), Exp: Now.AddDate(1, 0, 0)}
		if pr.Type == asc.ProfileTypeIOSAppStore && pr.DeviceIDs != nil {
			p.t.Errorf("App Store profile POST carries devices: %v", pr.DeviceIDs)
		}
		p.Profiles = append(p.Profiles, pr)
		writeJSON(w, 201, map[string]any{"data": profileRes(pr)})
	}))
	mux.HandleFunc("DELETE /v1/profiles/{id}", wrap(func(w http.ResponseWriter, r *http.Request) {
		p.Profiles = slices.DeleteFunc(p.Profiles, func(pr Profile) bool { return pr.ID == r.PathValue("id") })
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

func (p *Portal) nextID(prefix string) string {
	p.seq++
	return fmt.Sprintf("%s-%d", prefix, p.seq)
}

// issue signs a certificate for pub and records it; callers hold p.mu or run before the server.
func (p *Portal) issue(typ string, pub *rsa.PublicKey, exp time.Time) Cert {
	c := Cert{ID: p.nextID("cert"), Type: typ, DER: IssueCert(p.t, pub, p.signer), Exp: exp}
	p.Certs = append(p.Certs, c)
	return c
}

// Issue records a certificate of typ for pub, as if Apple had issued it
// earlier; call it before the client makes requests.
func (p *Portal) Issue(typ string, pub *rsa.PublicKey, exp time.Time) Cert {
	return p.issue(typ, pub, exp)
}

// Client is an ASC client pointed at the portal, with instant retries.
func (p *Portal) Client(t *testing.T) *asc.Client {
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

// Calls lists the recorded "METHOD /path" calls.
func (p *Portal) Calls() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.calls)
}

// Count returns how many recorded calls match "METHOD /path".
func (p *Portal) Count(key string) int {
	n := 0
	for _, c := range p.Calls() {
		if c == key {
			n++
		}
	}
	return n
}

// Reset forgets the recorded calls.
func (p *Portal) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = nil
}

// IssueCert signs a certificate for pub with signer and returns its DER.
func IssueCert(t *testing.T, pub *rsa.PublicKey, signer *rsa.PrivateKey) []byte {
	t.Helper()
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "Apple Development: Jane Developer"},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, pub, signer)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	return certDER
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Obj walks keys into nested JSON objects.
func Obj(t *testing.T, v any, keys ...string) map[string]any {
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

// Arr walks keys and returns the JSON array at the end.
func Arr(t *testing.T, v any, keys ...string) []any {
	t.Helper()
	if len(keys) > 0 {
		v = Obj(t, v, keys[:len(keys)-1]...)[keys[len(keys)-1]]
	}
	a, ok := v.([]any)
	if !ok {
		t.Errorf("JSON path %v: %T is not an array", keys, v)
	}
	return a
}
