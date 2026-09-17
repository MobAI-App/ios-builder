// Package asc is a client for the App Store Connect API.
//
// It runs on the developer's machine (or a CI agent) rather than on the macOS
// runner, authenticating with an App Store Connect API key: no Mac, altool or
// Transporter is involved.
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
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Credentials is an App Store Connect API key: the team's issuer ID, the key
// ID and the .p8 private key as downloaded from App Store Connect (PEM).
type Credentials struct {
	IssuerID   string
	KeyID      string
	PrivateKey string
}

// Validate checks that every field is present and that the key is a P-256 key.
func (c Credentials) Validate() error {
	_, err := newTokenSource(c)
	return err
}

// ParsePrivateKey parses the PEM .p8 key App Store Connect issues (PKCS#8 or
// SEC 1 encoded) and checks it is usable for ES256.
func ParsePrivateKey(pemKey string) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(strings.TrimSpace(pemKey)))
	if block == nil {
		return nil, errors.New("key is not valid PEM (expected the AuthKey_*.p8 contents)")
	}
	var key any
	var err error
	switch block.Type {
	case "PRIVATE KEY":
		key, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	case "EC PRIVATE KEY":
		key, err = x509.ParseECPrivateKey(block.Bytes)
	default:
		return nil, fmt.Errorf("unsupported PEM block %q", block.Type)
	}
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	ecKey, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("key is not an EC key (got %T); App Store Connect API keys are P-256", key)
	}
	if ecKey.Curve != elliptic.P256() {
		return nil, errors.New("private key is not on the P-256 curve")
	}
	return ecKey, nil
}

const (
	audience = "appstoreconnect-v1"
	// Apple caps tokens at 20 minutes; leave a margin for clock skew.
	tokenLifetime = 15 * time.Minute
	// A token is reissued this long before it expires so an in-flight
	// request never carries a token that lapses on the way.
	refreshMargin = time.Minute
)

// tokenSource signs and caches JWTs for one key.
type tokenSource struct {
	creds Credentials
	key   *ecdsa.PrivateKey
	now   func() time.Time

	mu     sync.Mutex
	token  string
	expiry time.Time
}

func newTokenSource(creds Credentials) (*tokenSource, error) {
	if strings.TrimSpace(creds.IssuerID) == "" {
		return nil, errors.New("issuer ID is empty")
	}
	if strings.TrimSpace(creds.KeyID) == "" {
		return nil, errors.New("key ID is empty")
	}
	key, err := ParsePrivateKey(creds.PrivateKey)
	if err != nil {
		return nil, err
	}
	return &tokenSource{creds: creds, key: key, now: time.Now}, nil
}

// Token returns a valid bearer token, reusing the cached one until it nears expiry.
func (t *tokenSource) Token() (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	if t.token != "" && now.Before(t.expiry.Add(-refreshMargin)) {
		return t.token, nil
	}
	exp := now.Add(tokenLifetime)
	token, err := signJWT(t.key, t.creds.KeyID, t.creds.IssuerID, now, exp)
	if err != nil {
		return "", err
	}
	t.token, t.expiry = token, exp
	return token, nil
}

// signJWT produces an ES256 JWT with the claims App Store Connect requires.
func signJWT(key *ecdsa.PrivateKey, keyID, issuerID string, issuedAt, expiresAt time.Time) (string, error) {
	header, err := json.Marshal(map[string]string{"alg": "ES256", "kid": keyID, "typ": "JWT"})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(map[string]any{
		"iss": issuerID,
		"iat": issuedAt.Unix(),
		"exp": expiresAt.Unix(),
		"aud": audience,
	})
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	signingInput := enc.EncodeToString(header) + "." + enc.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		return "", fmt.Errorf("sign token: %w", err)
	}
	// JWS wants the raw R||S pair, each left-padded to the curve size, not
	// the ASN.1 sequence ecdsa.SignASN1 produces.
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signingInput + "." + enc.EncodeToString(sig), nil
}
