package signing

import (
	"context"
	"crypto/rsa"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MobAI-App/ios-builder/internal/asc"
	"github.com/MobAI-App/ios-builder/internal/signing/signingtest"
	pkcs12 "software.sslmate.com/src/go-pkcs12"
)

func devOpts(dir string) *AutoOptions {
	return &AutoOptions{
		BundleID: "com.example.app",
		Type:     TypeDevelopment,
		Devices:  []Device{{Name: "Jane's iPhone", UDID: "00008030-000000000000001E"}},
		Password: "secret",
		OutDir:   dir,
		now:      func() time.Time { return signingtest.Now },
	}
}

func run(t *testing.T, p *signingtest.Portal, opts *AutoOptions) *AutoResult {
	t.Helper()
	p.Reset()
	res, err := Auto(context.Background(), p.Client(t), opts)
	if err != nil {
		t.Fatalf("Auto: %v", err)
	}
	return res
}

func TestAutoFirstRunCreatesEverything(t *testing.T) {
	p := signingtest.New(t)
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
	if p.Count("DELETE /v1/profiles/prof-3") != 0 || p.Count("POST /v1/profiles") != 1 || p.Count("POST /v1/certificates") != 1 || p.Count("POST /v1/devices") != 1 || p.Count("POST /v1/bundleIds") != 1 {
		t.Errorf("calls = %v", p.Calls())
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
	if !slices.Equal(gotCert.Raw, p.Certs[0].DER) {
		t.Error(".p12 holds a different certificate than the portal issued")
	}
	profile, err := os.ReadFile(res.Files.Profile)
	if err != nil || string(profile) != "profile:prof-3" || string(res.ProfileContent) != "profile:prof-3" {
		t.Errorf("profile file = %q, err = %v", profile, err)
	}
}

func TestAutoSecondRunReusesEverything(t *testing.T) {
	p := signingtest.New(t)
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
	for _, call := range p.Calls() {
		if strings.HasPrefix(call, "POST") || strings.HasPrefix(call, "DELETE") {
			t.Errorf("second run made %s", call)
		}
	}
	if _, err := os.Stat(res.Files.P12); err != nil {
		t.Errorf(".p12 not rewritten: %v", err)
	}
}

func TestAutoRecreatesProfileWhenDevicesChange(t *testing.T) {
	p := signingtest.New(t)
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
	if p.Count("DELETE /v1/profiles/"+first.Profile.ID) != 1 || p.Count("POST /v1/profiles") != 1 || len(p.Profiles) != 1 {
		t.Errorf("calls = %v, profiles = %+v", p.Calls(), p.Profiles)
	}
	if len(p.Profiles[0].DeviceIDs) != 2 {
		t.Errorf("new profile devices = %v", p.Profiles[0].DeviceIDs)
	}
}

func TestAutoRecreatesInvalidProfile(t *testing.T) {
	p := signingtest.New(t)
	dir := t.TempDir()
	first := run(t, p, devOpts(dir))
	keyPEM, _ := os.ReadFile(first.Files.Key)
	p.Profiles[0].State = asc.ProfileStateInvalid

	opts := devOpts(dir)
	opts.KeyPEM = keyPEM
	res := run(t, p, opts)
	if res.Certificate.Created || !res.Profile.Created || res.Profile.Reason != "invalid" || res.Profile.ID == first.Profile.ID {
		t.Errorf("result = %+v", res)
	}
	// An INVALID profile needs no relationship lookups to be condemned.
	if p.Count("GET /v1/profiles/"+first.Profile.ID+"/relationships/certificates") != 0 {
		t.Errorf("calls = %v", p.Calls())
	}
}

func TestAutoRecreatesProfileWhenCertificateChanges(t *testing.T) {
	p := signingtest.New(t)
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
	if len(p.Certs) != 2 || p.Profiles[0].CertIDs[0] != res.Certificate.ID {
		t.Errorf("certs = %d, profile certs = %v", len(p.Certs), p.Profiles[0].CertIDs)
	}
}

func TestAutoForceIssuesNewCertificateAndProfile(t *testing.T) {
	p := signingtest.New(t)
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
	if len(p.Certs) != 2 {
		t.Errorf("force must not revoke the old certificate: %d certificates left", len(p.Certs))
	}
	if !KeyMatchesCertificate(keyPEM, p.Certs[1].DER) {
		t.Error("the new certificate must be issued for the supplied key")
	}
}

func TestAutoAppStoreNeedsNoDevices(t *testing.T) {
	p := signingtest.New(t)
	p.BundleIDs = []string{"com.example.app.widget", "com.example.app"}
	opts := devOpts(t.TempDir())
	opts.Type = TypeStore
	opts.Devices = nil
	res := run(t, p, opts)
	if res.BundleID.Created || res.BundleID.ID != "bid-com.example.app" {
		t.Errorf("bundle ID = %+v (must match the exact identifier)", res.BundleID)
	}
	if res.Certificate.Type != asc.CertificateTypeDistribution || res.Profile.Type != asc.ProfileTypeIOSAppStore || res.Profile.Name != "Builder store com.example.app" {
		t.Errorf("result = %+v", res)
	}
	if res.Devices.InProfile != 0 || p.Count("GET /v1/devices") != 0 || p.Profiles[0].DeviceIDs != nil {
		t.Errorf("App Store setup touched devices: %+v, calls %v", res.Devices, p.Calls())
	}
}

func TestAutoDevelopmentWithoutDevicesFails(t *testing.T) {
	p := signingtest.New(t)
	opts := devOpts(t.TempDir())
	opts.Devices = nil
	_, err := Auto(context.Background(), p.Client(t), opts)
	if err == nil || !strings.Contains(err.Error(), "--device") || !strings.Contains(err.Error(), "--devices-from-mobai") {
		t.Errorf("err = %v", err)
	}
	// Devices are checked before the certificate: no device means no key
	// written and no certificate slot spent.
	if p.Count("POST /v1/certificates") != 0 || p.Count("POST /v1/profiles") != 0 {
		t.Errorf("certificate or profile created without devices: %v", p.Calls())
	}
	if _, err := os.Stat(filepath.Join(opts.OutDir, KeyFileName(TypeDevelopment))); err == nil {
		t.Error("a key was written although no certificate was requested")
	}
}

func TestAutoDisabledDevicesStayOutOfProfile(t *testing.T) {
	p := signingtest.New(t)
	p.Devices = []signingtest.Device{
		{ID: "dev-old", Name: "Old", UDID: "00008020-0000000000000001", Status: "DISABLED"},
		{ID: "dev-ok", Name: "Jane's iPhone", UDID: "00008030-000000000000001e", Status: "ENABLED"},
	}
	res := run(t, p, devOpts(t.TempDir()))
	if len(res.Devices.Registered) != 0 {
		t.Errorf("UDID matching must be case-insensitive: registered %+v", res.Devices.Registered)
	}
	if res.Devices.InProfile != 1 || !slices.Equal(p.Profiles[0].DeviceIDs, []string{"dev-ok"}) {
		t.Errorf("profile devices = %v", p.Profiles[0].DeviceIDs)
	}
}

func TestAutoWithSuppliedKeyReusesMatchingCertificate(t *testing.T) {
	p := signingtest.New(t)
	keyPEM, _, err := GenerateKeyAndCSR("Jane", "jane@example.com")
	if err != nil {
		t.Fatal(err)
	}
	key, _ := parseKey(keyPEM)
	p.Issue(asc.CertificateTypeDevelopment, &key.PublicKey, signingtest.Now.AddDate(0, 6, 0))
	// An expired one for the same key must not be picked.
	expired := p.Issue(asc.CertificateTypeDevelopment, &key.PublicKey, signingtest.Now.AddDate(0, -1, 0))

	opts := devOpts(t.TempDir())
	opts.KeyPEM = keyPEM
	res := run(t, p, opts)
	if res.Certificate.Created || res.Certificate.ID != "cert-1" || res.Certificate.ID == expired.ID || res.Certificate.ValidOnAccount != 1 {
		t.Errorf("certificate = %+v", res.Certificate)
	}
	if p.Count("POST /v1/certificates") != 0 {
		t.Errorf("calls = %v", p.Calls())
	}
}

func TestAutoCertificateLimitHint(t *testing.T) {
	p := signingtest.New(t)
	p.RefuseCertificates = true
	dir := t.TempDir()
	res, err := Auto(context.Background(), p.Client(t), devOpts(dir))
	if err == nil || !strings.Contains(err.Error(), "maximum number of certificates") || !strings.Contains(err.Error(), "Revoke one") || !strings.Contains(err.Error(), "--key") {
		t.Errorf("err = %v", err)
	}
	// The key is on disk before the request goes out, so whatever Apple did
	// with it, the next run can carry on with the same key.
	keyPEM, readErr := os.ReadFile(res.Files.Key)
	if readErr != nil || res.Files.Key != filepath.Join(dir, KeyFileName(TypeDevelopment)) {
		t.Fatalf("key after a refused certificate: %+v, %v", res.Files, readErr)
	}
	p.RefuseCertificates = false
	opts := devOpts(dir)
	opts.KeyPEM = keyPEM
	res = run(t, p, opts)
	if !res.Certificate.Created || !KeyMatchesCertificate(keyPEM, p.Certs[0].DER) || res.Files.Key != "" {
		t.Errorf("retry = %+v", res)
	}
}

func TestAutoDeviceLimitHint(t *testing.T) {
	p := signingtest.New(t)
	p.RefuseDevices = true
	_, err := Auto(context.Background(), p.Client(t), devOpts(t.TempDir()))
	if err == nil || !strings.Contains(err.Error(), "00008030-000000000000001E") || !strings.Contains(err.Error(), "100 iOS devices") {
		t.Errorf("err = %v", err)
	}
}

func TestAutoRejectsBadOptions(t *testing.T) {
	p := signingtest.New(t)
	for name, mutate := range map[string]func(*AutoOptions){
		"no bundle ID": func(o *AutoOptions) { o.BundleID = "" },
		"bad type":     func(o *AutoOptions) { o.Type = "enterprise" },
		"no password":  func(o *AutoOptions) { o.Password = "" },
	} {
		opts := devOpts(t.TempDir())
		mutate(opts)
		if _, err := Auto(context.Background(), p.Client(t), opts); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if len(p.Calls()) != 0 {
		t.Errorf("invalid options reached the API: %v", p.Calls())
	}
}

// A second type in the same directory leaves the first type's key, .p12 and
// profile in place: each set is a separate file trio.
func TestAutoTypesKeepSeparateFiles(t *testing.T) {
	p := signingtest.New(t)
	dir := t.TempDir()
	dev := run(t, p, devOpts(dir))
	opts := devOpts(dir)
	opts.Type, opts.Devices = TypeStore, nil
	store := run(t, p, opts)
	if dev.Files.Key == store.Files.Key || dev.Files.P12 == store.Files.P12 || dev.Files.Profile == store.Files.Profile {
		t.Fatalf("files collide: %+v vs %+v", dev.Files, store.Files)
	}
	for _, f := range []string{dev.Files.Key, dev.Files.P12, dev.Files.Profile, store.Files.Key, store.Files.P12, store.Files.Profile} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	if len(p.Profiles) != 2 || len(p.Certs) != 2 {
		t.Errorf("one certificate and profile per type expected: %d profiles, %d certificates", len(p.Profiles), len(p.Certs))
	}
}

func TestAutoRefusesEnterprise(t *testing.T) {
	p := signingtest.New(t)
	opts := devOpts(t.TempDir())
	opts.Type = TypeEnterprise
	if _, err := Auto(context.Background(), p.Client(t), opts); err == nil || !strings.Contains(err.Error(), "--certificate") || len(p.Calls()) != 0 {
		t.Errorf("err = %v, calls %v", err, p.Calls())
	}
}

func TestBundleIDName(t *testing.T) {
	for in, want := range map[string]string{"com.example.app": "com example app", "com.example.my-app_2": "com example my app 2", "App": "App"} {
		if got := bundleIDName(in); got != want {
			t.Errorf("bundleIDName(%q) = %q, want %q", in, got, want)
		}
	}
}
