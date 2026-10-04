package github

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/crypto/nacl/box"
)

func TestSetSecretSealsTheValue(t *testing.T) {
	pub, priv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var stored CreateSecretRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /repos/o/r/actions/secrets/public-key":
			fmt.Fprintf(w, `{"key_id":"kid","key":%q}`, base64.StdEncoding.EncodeToString(pub[:]))
		case "PUT /repos/o/r/actions/secrets/SENTRY_TOKEN":
			_ = json.NewDecoder(r.Body).Decode(&stored)
			w.WriteHeader(http.StatusCreated)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
		}
	}))
	defer srv.Close()
	if err := NewClientWithBaseURL("tok", srv.URL).SetSecret(context.Background(), "o", "r", "SENTRY_TOKEN", "s3cret"); err != nil {
		t.Fatal(err)
	}
	sealed, err := base64.StdEncoding.DecodeString(stored.EncryptedValue)
	if err != nil {
		t.Fatal(err)
	}
	plain, ok := box.OpenAnonymous(nil, sealed, pub, priv)
	if !ok || string(plain) != "s3cret" || stored.KeyID != "kid" {
		t.Fatalf("stored %+v, opened %q %v", stored, plain, ok)
	}
}

func TestDeleteSecret(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("unexpected %s", r.Method)
		}
		switch r.URL.Path {
		case "/repos/o/r/actions/secrets/THERE":
			w.WriteHeader(http.StatusNoContent)
		case "/repos/o/r/actions/secrets/GONE":
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	defer srv.Close()
	c := NewClientWithBaseURL("tok", srv.URL)
	if ok, err := c.DeleteSecret(context.Background(), "o", "r", "THERE"); !ok || err != nil {
		t.Fatalf("THERE: %v %v", ok, err)
	}
	if ok, err := c.DeleteSecret(context.Background(), "o", "r", "GONE"); ok || err != nil {
		t.Fatalf("GONE: %v %v", ok, err)
	}
	if _, err := c.DeleteSecret(context.Background(), "o", "r", "DENIED"); err == nil {
		t.Fatal("403 accepted")
	}
}
