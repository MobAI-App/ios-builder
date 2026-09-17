package asc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBundleIDByIdentifierSkipsPrefixMatches(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/bundleIds" || r.URL.Query().Get("filter[identifier]") != "com.example.app" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		writeJSON(w, 200, map[string]any{"data": []map[string]any{
			{"type": "bundleIds", "id": "bid-2", "attributes": map[string]any{"identifier": "com.example.app.watch", "name": "Watch", "platform": "IOS"}},
			{"type": "bundleIds", "id": "bid-1", "attributes": map[string]any{"identifier": "com.example.app", "name": "Example", "platform": "IOS", "seedId": "TEAM1"}},
		}})
	}))
	defer srv.Close()
	b, err := newTestClient(t, srv).BundleIDByIdentifier(context.Background(), "com.example.app")
	if err != nil {
		t.Fatal(err)
	}
	if b == nil || b.ID != "bid-1" || b.Name != "Example" || b.SeedID != "TEAM1" || b.Platform != PlatformIOS {
		t.Errorf("bundle ID = %+v", b)
	}
}

func TestBundleIDByIdentifierAbsent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"data": []any{}})
	}))
	defer srv.Close()
	b, err := newTestClient(t, srv).BundleIDByIdentifier(context.Background(), "com.missing")
	if err != nil || b != nil {
		t.Errorf("bundle ID = %+v, err = %v", b, err)
	}
}

func TestCreateBundleID(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/bundleIds" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		writeJSON(w, 201, map[string]any{"data": map[string]any{"type": "bundleIds", "id": "bid-9", "attributes": map[string]any{"identifier": "com.example.app", "name": "com example app", "platform": "IOS"}}})
	}))
	defer srv.Close()
	b, err := newTestClient(t, srv).CreateBundleID(context.Background(), "com.example.app", "com example app", PlatformIOS)
	if err != nil {
		t.Fatal(err)
	}
	if b.ID != "bid-9" || b.Identifier != "com.example.app" {
		t.Errorf("bundle ID = %+v", b)
	}
	attrs := obj(t, body, "data", "attributes")
	if obj(t, body, "data")["type"] != "bundleIds" || attrs["identifier"] != "com.example.app" || attrs["name"] != "com example app" || attrs["platform"] != "IOS" {
		t.Errorf("POST body = %v", body)
	}
}
