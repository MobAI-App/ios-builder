// Package signing creates iOS code-signing material without a Mac: it
// generates the certificate signing request that Keychain Access would
// normally produce, and assembles a .p12 from the certificate Apple issues.
package signing

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"fmt"

	pkcs12 "software.sslmate.com/src/go-pkcs12"
)

// GenerateKeyAndCSR creates an RSA-2048 private key and a certificate signing
// request for the Apple Developer portal, both PEM-encoded. Apple requires
// RSA 2048 for signing certificates; the subject mirrors what Keychain Access
// puts in its CSRs (email address and common name).
func GenerateKeyAndCSR(commonName, email string) (keyPEM, csrPEM []byte, err error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate private key: %w", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	csrPEM, err = CreateCSR(keyPEM, commonName, email)
	if err != nil {
		return nil, nil, err
	}
	return keyPEM, csrPEM, nil
}

// CreateCSR makes a PEM certificate signing request for an existing private
// key (as written by GenerateKeyAndCSR). The email is omitted from the
// subject when empty.
func CreateCSR(keyPEM []byte, commonName, email string) ([]byte, error) {
	key, err := parseKey(keyPEM)
	if err != nil {
		return nil, err
	}
	template := x509.CertificateRequest{
		Subject:            pkix.Name{CommonName: commonName},
		SignatureAlgorithm: x509.SHA256WithRSA,
	}
	if email != "" {
		// emailAddress (OID 1.2.840.113549.1.9.1), as in Keychain CSRs.
		// Forced to IA5String: Go would otherwise encode the '@' as
		// UTF8String, which is not the standard encoding for this field.
		template.Subject.ExtraNames = []pkix.AttributeTypeAndValue{{
			Type:  []int{1, 2, 840, 113549, 1, 9, 1},
			Value: asn1.RawValue{Tag: asn1.TagIA5String, Bytes: []byte(email)},
		}}
	}

	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &template, key)
	if err != nil {
		return nil, fmt.Errorf("failed to create CSR: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE REQUEST",
		Bytes: csrDER,
	}), nil
}

func parseKey(keyPEM []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, fmt.Errorf("invalid private key: not PEM encoded")
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse private key: %w", err)
	}
	return key, nil
}

func parseCertificate(certData []byte) (*x509.Certificate, error) {
	certDER := certData
	if certBlock, _ := pem.Decode(certData); certBlock != nil {
		certDER = certBlock.Bytes
	}
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, fmt.Errorf("failed to parse certificate (expected the .cer file downloaded from the Apple Developer portal): %w", err)
	}
	return cert, nil
}

// KeyMatchesCertificate reports whether the certificate (DER or PEM) was
// issued for the private key's public key.
func KeyMatchesCertificate(keyPEM, certData []byte) bool {
	key, err := parseKey(keyPEM)
	if err != nil {
		return false
	}
	cert, err := parseCertificate(certData)
	if err != nil {
		return false
	}
	certKey, ok := cert.PublicKey.(*rsa.PublicKey)
	return ok && certKey.Equal(key.Public())
}

// BuildP12 combines a PEM private key (from GenerateKeyAndCSR) with the
// certificate Apple issued for its CSR (DER .cer as downloaded from the
// portal, or PEM) into a password-protected PKCS#12 bundle, the same format
// Keychain Access exports. The legacy encoding is used because that is what
// macOS `security import` expects.
func BuildP12(keyPEM, certData []byte, password string) ([]byte, error) {
	key, err := parseKey(keyPEM)
	if err != nil {
		return nil, err
	}
	cert, err := parseCertificate(certData)
	if err != nil {
		return nil, err
	}

	certKey, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok || !certKey.Equal(key.Public()) {
		return nil, fmt.Errorf("certificate does not match the private key: it was issued for a different CSR (was the key regenerated after uploading the CSR?)")
	}

	p12, err := pkcs12.Legacy.Encode(key, cert, nil, password)
	if err != nil {
		return nil, fmt.Errorf("failed to create .p12: %w", err)
	}
	return p12, nil
}
