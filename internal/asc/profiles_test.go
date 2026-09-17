package asc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListProfilesByNameDropsOtherNames(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/profiles" || r.URL.Query().Get("filter[name]") != "Builder development com.example.app" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		writeJSON(w, 200, map[string]any{"data": []map[string]any{
			{"type": "profiles", "id": "prof-1", "attributes": map[string]any{
				"name": "Builder development com.example.app", "platform": "IOS", "uuid": "1111-2222", "profileState": "ACTIVE", "profileType": "IOS_APP_DEVELOPMENT",
				"profileContent": base64.StdEncoding.EncodeToString([]byte("plist")), "createdDate": "2026-09-16T10:00:00.000+00:00", "expirationDate": "2027-09-16T10:00:00.000+00:00",
			}},
			{"type": "profiles", "id": "prof-2", "attributes": map[string]any{"name": "Builder development com.example.app 2", "profileState": "INVALID"}},
		}})
	}))
	defer srv.Close()
	profiles, err := newTestClient(t, srv).ListProfilesByName(context.Background(), "Builder development com.example.app")
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 1 {
		t.Fatalf("profiles = %+v", profiles)
	}
	p := profiles[0]
	if p.ID != "prof-1" || p.UUID != "1111-2222" || p.State != ProfileStateActive || p.Type != ProfileTypeIOSAppDevelopment || string(p.Content) != "plist" || p.ExpirationDate.Year() != 2027 || p.CreatedDate.Year() != 2026 {
		t.Errorf("profile = %+v", p)
	}
}

func TestProfileRelationshipIDsFollowPagination(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/profiles/prof-1/relationships/certificates":
			writeJSON(w, 200, map[string]any{"data": []map[string]any{{"type": "certificates", "id": "cert-1"}}})
		case r.URL.Path == "/v1/profiles/prof-1/relationships/devices" && r.URL.Query().Get("cursor") == "":
			writeJSON(w, 200, map[string]any{
				"data":  []map[string]any{{"type": "devices", "id": "dev-1"}},
				"links": map[string]string{"next": srv.URL + "/v1/profiles/prof-1/relationships/devices?limit=200&cursor=n"},
			})
		case r.URL.Path == "/v1/profiles/prof-1/relationships/devices":
			writeJSON(w, 200, map[string]any{"data": []map[string]any{{"type": "devices", "id": "dev-2"}}})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	certs, err := c.ProfileCertificateIDs(context.Background(), "prof-1")
	if err != nil || len(certs) != 1 || certs[0] != "cert-1" {
		t.Errorf("certs = %v, err = %v", certs, err)
	}
	devices, err := c.ProfileDeviceIDs(context.Background(), "prof-1")
	if err != nil || len(devices) != 2 || devices[0] != "dev-1" || devices[1] != "dev-2" {
		t.Errorf("devices = %v, err = %v", devices, err)
	}
}

func TestCreateAndDeleteProfile(t *testing.T) {
	var body map[string]any
	var deleted string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/profiles":
			_ = json.NewDecoder(r.Body).Decode(&body)
			writeJSON(w, 201, map[string]any{"data": map[string]any{"type": "profiles", "id": "prof-9", "attributes": map[string]any{
				"name": "Builder ad-hoc com.example.app", "profileType": "IOS_APP_ADHOC", "profileState": "ACTIVE", "uuid": "u-9", "profileContent": base64.StdEncoding.EncodeToString([]byte("plist")),
			}}})
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/profiles/prof-1":
			deleted = "prof-1"
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	ctx := context.Background()

	p, err := c.CreateProfile(ctx, "Builder ad-hoc com.example.app", ProfileTypeIOSAppAdHoc, "bid-1", []string{"cert-1"}, []string{"dev-1", "dev-2"})
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "prof-9" || p.State != ProfileStateActive || p.UUID != "u-9" || string(p.Content) != "plist" {
		t.Errorf("profile = %+v", p)
	}
	data := obj(t, body, "data")
	attrs := obj(t, data, "attributes")
	if data["type"] != "profiles" || attrs["name"] != "Builder ad-hoc com.example.app" || attrs["profileType"] != "IOS_APP_ADHOC" {
		t.Errorf("POST body = %v", body)
	}
	if _, has := attrs["profileContent"]; has {
		t.Errorf("request must not carry profileContent: %v", attrs)
	}
	if obj(t, data, "relationships", "bundleId", "data")["id"] != "bid-1" {
		t.Errorf("bundleId relationship = %v", obj(t, data, "relationships"))
	}
	certs := arr(t, data, "relationships", "certificates", "data")
	if len(certs) != 1 || obj(t, certs[0])["type"] != "certificates" || obj(t, certs[0])["id"] != "cert-1" {
		t.Errorf("certificates relationship = %v", certs)
	}
	devices := arr(t, data, "relationships", "devices", "data")
	if len(devices) != 2 || obj(t, devices[1])["id"] != "dev-2" {
		t.Errorf("devices relationship = %v", devices)
	}

	// App Store profiles take no devices; the relationship must be absent, not empty.
	if _, err := c.CreateProfile(ctx, "Builder app-store com.example.app", ProfileTypeIOSAppStore, "bid-1", []string{"cert-1"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, has := obj(t, body, "data", "relationships")["devices"]; has {
		t.Errorf("App Store profile request carries devices: %v", body)
	}

	if err := c.DeleteProfile(ctx, "prof-1"); err != nil || deleted != "prof-1" {
		t.Errorf("delete: err = %v, deleted = %q", err, deleted)
	}
}

func TestDeleteProfileToleratesGone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/profiles/gone":
			writeJSON(w, 404, map[string]any{"errors": []map[string]any{{"status": "404", "code": "NOT_FOUND", "title": "The specified resource does not exist"}}})
		default:
			writeJSON(w, 409, map[string]any{"errors": []map[string]any{{"status": "409", "code": "STATE_ERROR", "title": "in use"}}})
		}
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	if err := c.DeleteProfile(context.Background(), "gone"); err != nil {
		t.Errorf("a profile that is already gone must not fail the delete: %v", err)
	}
	if err := c.DeleteProfile(context.Background(), "busy"); !IsStatus(err, 409) {
		t.Errorf("other errors must surface: %v", err)
	}
}
