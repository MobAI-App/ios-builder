package asc

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestListCertificatesDecodesContent(t *testing.T) {
	der := []byte{0x30, 0x03, 0x02, 0x01, 0x01}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/certificates" || r.URL.Query().Get("filter[certificateType]") != "DEVELOPMENT" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		writeJSON(w, 200, map[string]any{"data": []map[string]any{{
			"type": "certificates", "id": "cert-1",
			"attributes": map[string]any{
				"certificateContent": base64.StdEncoding.EncodeToString(der),
				"displayName":        "Jane Doe",
				"name":               "Apple Development: Jane Doe (ABC123)",
				"serialNumber":       "1A2B3C",
				"certificateType":    "DEVELOPMENT",
				"platform":           "IOS",
				"expirationDate":     "2027-09-16T10:00:00.000+00:00",
			},
		}}})
	}))
	defer srv.Close()
	certs, err := newTestClient(t, srv).ListCertificates(context.Background(), CertificateTypeDevelopment)
	if err != nil {
		t.Fatal(err)
	}
	if len(certs) != 1 {
		t.Fatalf("certs = %+v", certs)
	}
	c := certs[0]
	if c.ID != "cert-1" || c.SerialNumber != "1A2B3C" || c.Type != CertificateTypeDevelopment || c.DisplayName != "Jane Doe" || c.ExpirationDate.Year() != 2027 || !bytes.Equal(c.Content, der) {
		t.Errorf("cert = %+v", c)
	}
}

func TestListCertificatesRejectsBadContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"data": []map[string]any{{"type": "certificates", "id": "cert-1", "attributes": map[string]any{"certificateContent": "not base64!"}}}})
	}))
	defer srv.Close()
	_, err := newTestClient(t, srv).ListCertificates(context.Background(), CertificateTypeDistribution)
	if err == nil || !strings.Contains(err.Error(), "cert-1") {
		t.Errorf("err = %v", err)
	}
}

func TestCreateCertificateSendsCSR(t *testing.T) {
	var body map[string]any
	csr := []byte("-----BEGIN CERTIFICATE REQUEST-----\nMIIB\n-----END CERTIFICATE REQUEST-----\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/certificates" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		writeJSON(w, 201, map[string]any{"data": map[string]any{"type": "certificates", "id": "cert-2", "attributes": map[string]any{
			"certificateContent": base64.StdEncoding.EncodeToString([]byte("DER")), "certificateType": "DISTRIBUTION", "name": "Apple Distribution: Team (ABC123)",
		}}})
	}))
	defer srv.Close()
	cert, err := newTestClient(t, srv).CreateCertificate(context.Background(), CertificateTypeDistribution, csr)
	if err != nil {
		t.Fatal(err)
	}
	if cert.ID != "cert-2" || cert.Type != CertificateTypeDistribution || string(cert.Content) != "DER" {
		t.Errorf("cert = %+v", cert)
	}
	attrs := obj(t, body, "data", "attributes")
	if obj(t, body, "data")["type"] != "certificates" || attrs["certificateType"] != "DISTRIBUTION" || attrs["csrContent"] != string(csr) {
		t.Errorf("POST body = %v", body)
	}
	if _, has := attrs["certificateContent"]; has {
		t.Errorf("request must not carry certificateContent: %v", attrs)
	}
}
