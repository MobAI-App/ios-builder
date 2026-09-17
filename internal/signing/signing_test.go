package signing

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"

	pkcs12 "software.sslmate.com/src/go-pkcs12"
)

func TestGenerateKeyAndCSR(t *testing.T) {
	keyPEM, csrPEM, err := GenerateKeyAndCSR("Jane Developer", "jane@example.com")
	if err != nil {
		t.Fatalf("GenerateKeyAndCSR: %v", err)
	}

	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil || keyBlock.Type != "PRIVATE KEY" {
		t.Fatalf("key is not a PEM PKCS#8 PRIVATE KEY block")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		t.Fatalf("ParsePKCS8PrivateKey: %v", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok || key.N.BitLen() != 2048 {
		t.Errorf("key = %T, want an RSA key of 2048 bits", parsed)
	}

	csrBlock, _ := pem.Decode(csrPEM)
	if csrBlock == nil || csrBlock.Type != "CERTIFICATE REQUEST" {
		t.Fatalf("CSR is not a PEM CERTIFICATE REQUEST block")
	}
	csr, err := x509.ParseCertificateRequest(csrBlock.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificateRequest: %v", err)
	}
	if err := csr.CheckSignature(); err != nil {
		t.Errorf("CSR signature invalid: %v", err)
	}
	if csr.Subject.CommonName != "Jane Developer" {
		t.Errorf("CommonName = %q, want %q", csr.Subject.CommonName, "Jane Developer")
	}
	// emailAddress must be present and IA5String-encoded (tag 0x16), the
	// encoding Keychain and OpenSSL use; Subject.String() hex-prints it, so
	// check the raw DER.
	emailIA5 := append([]byte{0x16, byte(len("jane@example.com"))}, "jane@example.com"...)
	if !bytes.Contains(csrBlock.Bytes, emailIA5) {
		t.Errorf("CSR does not contain IA5String-encoded email address")
	}
}

// issueCert signs a certificate for pub, standing in for the Apple portal.
func issueCert(t *testing.T, pub *rsa.PublicKey, signer *rsa.PrivateKey) []byte {
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

func TestBuildP12Roundtrip(t *testing.T) {
	keyPEM, _, err := GenerateKeyAndCSR("Jane Developer", "jane@example.com")
	if err != nil {
		t.Fatalf("GenerateKeyAndCSR: %v", err)
	}
	key, err := parseKey(keyPEM)
	if err != nil {
		t.Fatalf("parseKey: %v", err)
	}
	certDER := issueCert(t, &key.PublicKey, key)

	p12, err := BuildP12(keyPEM, certDER, "secret")
	if err != nil {
		t.Fatalf("BuildP12: %v", err)
	}

	gotKey, gotCert, err := pkcs12.Decode(p12, "secret")
	if err != nil {
		t.Fatalf("pkcs12.Decode: %v", err)
	}
	rsaKey, ok := gotKey.(*rsa.PrivateKey)
	if !ok || !rsaKey.Equal(key) {
		t.Errorf("decoded key does not match original")
	}
	if gotCert.Subject.CommonName != "Apple Development: Jane Developer" {
		t.Errorf("decoded cert CN = %q", gotCert.Subject.CommonName)
	}
}

func TestBuildP12AcceptsPEMCertificate(t *testing.T) {
	keyPEM, _, err := GenerateKeyAndCSR("Jane Developer", "jane@example.com")
	if err != nil {
		t.Fatalf("GenerateKeyAndCSR: %v", err)
	}
	key, _ := parseKey(keyPEM)
	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: issueCert(t, &key.PublicKey, key),
	})

	if _, err := BuildP12(keyPEM, certPEM, "secret"); err != nil {
		t.Errorf("BuildP12 with PEM certificate: %v", err)
	}
}

// Keys written by earlier versions are PKCS#1 "RSA PRIVATE KEY" blocks; they
// still open, so a --key from before this change keeps its certificate.
func TestParseKeyAcceptsPKCS1(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pkcs1 := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	got, err := parseKey(pkcs1)
	if err != nil || !got.Equal(key) {
		t.Fatalf("parseKey(PKCS#1) = %v, err = %v", got, err)
	}
	if _, err := CreateCSR(pkcs1, "Jane", ""); err != nil {
		t.Errorf("CreateCSR with a PKCS#1 key: %v", err)
	}
	if !KeyMatchesCertificate(pkcs1, issueCert(t, &key.PublicKey, key)) {
		t.Error("KeyMatchesCertificate with a PKCS#1 key")
	}
}

func TestBuildP12RejectsMismatchedCertificate(t *testing.T) {
	keyPEM, _, err := GenerateKeyAndCSR("Jane Developer", "jane@example.com")
	if err != nil {
		t.Fatalf("GenerateKeyAndCSR: %v", err)
	}
	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	certDER := issueCert(t, &otherKey.PublicKey, otherKey)

	if _, err := BuildP12(keyPEM, certDER, "secret"); err == nil {
		t.Errorf("BuildP12 accepted a certificate for a different key")
	}
}
