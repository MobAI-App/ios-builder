package asc

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"
)

// testKey returns a fresh P-256 key and its PKCS#8 PEM, as Apple's .p8 files are encoded.
func testKey(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return key, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func testCredentials(t *testing.T) (Credentials, *ecdsa.PrivateKey) {
	t.Helper()
	key, pemKey := testKey(t)
	return Credentials{IssuerID: "issuer-1", KeyID: "KEY123", PrivateKey: pemKey}, key
}

func decodeSegment(t *testing.T, s string, out any) {
	t.Helper()
	data, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		t.Fatal(err)
	}
}

func TestTokenClaimsAndSignature(t *testing.T) {
	creds, key := testCredentials(t)
	ts, err := newTokenSource(creds)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	ts.now = func() time.Time { return now }

	token, err := ts.Token()
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d segments", len(parts))
	}
	var header map[string]string
	decodeSegment(t, parts[0], &header)
	if header["alg"] != "ES256" || header["kid"] != "KEY123" || header["typ"] != "JWT" {
		t.Errorf("header = %v", header)
	}
	var claims map[string]any
	decodeSegment(t, parts[1], &claims)
	if claims["iss"] != "issuer-1" || claims["aud"] != audience {
		t.Errorf("claims = %v", claims)
	}
	iat, exp := int64(claims["iat"].(float64)), int64(claims["exp"].(float64))
	if iat != now.Unix() {
		t.Errorf("iat = %d, want %d", iat, now.Unix())
	}
	if lifetime := exp - iat; lifetime <= 0 || lifetime > 20*60 {
		t.Errorf("exp-iat = %ds, must be within Apple's 20 minute cap", lifetime)
	}

	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		t.Fatalf("signature: %v, %d bytes (want raw 64-byte R||S)", err, len(sig))
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r, s := new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(&key.PublicKey, digest[:], r, s) {
		t.Error("signature does not verify with the key's public half")
	}
}

func TestTokenCachedAndRefreshedBeforeExpiry(t *testing.T) {
	creds, _ := testCredentials(t)
	ts, err := newTokenSource(creds)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ts.now = func() time.Time { return now }
	first, _ := ts.Token()
	now = now.Add(5 * time.Minute)
	if again, _ := ts.Token(); again != first {
		t.Error("token reissued while still valid")
	}
	// Inside the refresh margin: a new token must be minted even though the
	// old one has not technically expired yet.
	now = now.Add(tokenLifetime - 5*time.Minute - refreshMargin/2)
	if again, _ := ts.Token(); again == first {
		t.Error("token not refreshed before expiry")
	}
}

func TestCredentialsValidate(t *testing.T) {
	_, pemKey := testKey(t)
	cases := map[string]Credentials{
		"missing issuer": {KeyID: "K", PrivateKey: pemKey},
		"missing key id": {IssuerID: "I", PrivateKey: pemKey},
		"not pem":        {IssuerID: "I", KeyID: "K", PrivateKey: "-----BEGIN NOTHING"},
		"wrong block":    {IssuerID: "I", KeyID: "K", PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte{1}}))},
	}
	for name, c := range cases {
		if err := c.Validate(); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	if err := (Credentials{IssuerID: "I", KeyID: "K", PrivateKey: pemKey}).Validate(); err != nil {
		t.Errorf("valid credentials rejected: %v", err)
	}
	// SEC 1 "EC PRIVATE KEY" encoding is accepted too.
	key, _ := testKey(t)
	der, _ := x509.MarshalECPrivateKey(key)
	sec1 := string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))
	if _, err := ParsePrivateKey(sec1); err != nil {
		t.Errorf("SEC 1 key rejected: %v", err)
	}
}
