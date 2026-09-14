package mobai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// isolateConfigDir points os.UserConfigDir at a temp dir on every OS, so the
// client ID file never lands in the developer's real config.
func isolateConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("AppData", dir)
	configDir, err := os.UserConfigDir()
	if err != nil {
		t.Fatalf("UserConfigDir: %v", err)
	}
	return configDir
}

// fakeMobAI records the lease header each endpoint saw and serves claims the
// way MobAI does: the client ID becomes the lease token.
type fakeMobAI struct {
	mu          sync.Mutex
	claims      []claimRequest
	tokens      map[string]string // path -> X-Lease-Token seen
	claimStatus int               // non-zero: reject claims with this status
	renewStatus int               // non-zero: reject renewals with this status
}

func newFakeMobAI(t *testing.T) (*fakeMobAI, *httptest.Server) {
	t.Helper()
	f := &fakeMobAI{tokens: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeMobAI) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.tokens[r.URL.Path] = r.Header.Get(leaseTokenHeader)
	claimStatus, renewStatus := f.claimStatus, f.renewStatus
	f.mu.Unlock()

	switch r.URL.Path {
	case "/api/v1/devices/claim":
		var req claimRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.claims = append(f.claims, req)
		f.mu.Unlock()
		if claimStatus != 0 {
			w.WriteHeader(claimStatus)
			_, _ = w.Write([]byte(`{"error":"device is in use","code":"DEVICE_IN_USE"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"deviceId":   req.Device,
			"leaseToken": req.ClientID,
			"expiresAt":  time.Now().Add(time.Duration(req.TTLSeconds) * time.Second).UTC().Format(time.RFC3339),
		})
	case "/api/v1/devices/renew":
		if renewStatus != 0 {
			w.WriteHeader(renewStatus)
			_, _ = w.Write([]byte(`{"error":"no active lease for that token"}`))
			return
		}
		_, _ = w.Write([]byte(`{"expiresAt":"2030-01-01T00:00:00Z"}`))
	case "/api/v1/devices/dev1/install-app":
		_, _ = w.Write([]byte(`{"success":true,"data":{"bundleId":"com.example.app"}}`))
	case "/api/v1/devices/dev1/forward":
		_, _ = w.Write([]byte(`{"id":"f1","hostPort":9000,"devicePort":9000}`))
	case "/api/v1/devices/dev1/debug":
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		_ = conn.Close()
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeMobAI) setClaimStatus(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claimStatus = status
}

func (f *fakeMobAI) setRenewStatus(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renewStatus = status
}

func (f *fakeMobAI) claimsMade() []claimRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.claims)
}

func (f *fakeMobAI) tokenSeen(path string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tokens[path]
}

func TestClaimSendsLeaseOnEveryDeviceCall(t *testing.T) {
	isolateConfigDir(t)
	f, srv := newFakeMobAI(t)
	c := NewClient(srv.URL)
	ctx := context.Background()

	if err := c.Claim(ctx, "dev1"); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	claims := f.claimsMade()
	if len(claims) != 1 {
		t.Fatalf("claims = %d, want 1", len(claims))
	}
	claim := claims[0]
	if claim.Device != "dev1" || claim.TTLSeconds != int(leaseTTL/time.Second) || claim.ClientID == "" || claim.Holder == "" {
		t.Fatalf("claim request = %+v", claim)
	}

	if _, err := c.InstallApp(ctx, "dev1", InstallAppRequest{Path: "app.ipa"}); err != nil {
		t.Fatalf("InstallApp: %v", err)
	}
	if _, err := c.ForwardPort(ctx, "dev1", PortForwardRequest{DevicePort: 9000}); err != nil {
		t.Fatalf("ForwardPort: %v", err)
	}
	_, conn, err := c.DebugStream(ctx, "dev1", "com.example.app", nil)
	if err != nil {
		t.Fatalf("DebugStream: %v", err)
	}
	_ = conn.Close()

	for _, path := range []string{
		"/api/v1/devices/dev1/install-app",
		"/api/v1/devices/dev1/forward",
		"/api/v1/devices/dev1/debug",
	} {
		if got := f.tokenSeen(path); got != claim.ClientID {
			t.Errorf("%s: X-Lease-Token = %q, want %q", path, got, claim.ClientID)
		}
	}
}

func TestClaimReusesClientIDAcrossClients(t *testing.T) {
	configDir := isolateConfigDir(t)
	f, srv := newFakeMobAI(t)
	ctx := context.Background()

	// Flutter starts a separate builder process per step; each must present
	// the same ID so they share one lease.
	for range 2 {
		if err := NewClient(srv.URL).Claim(ctx, "dev1"); err != nil {
			t.Fatalf("Claim: %v", err)
		}
	}
	claims := f.claimsMade()
	if claims[0].ClientID != claims[1].ClientID {
		t.Fatalf("client IDs differ: %q vs %q", claims[0].ClientID, claims[1].ClientID)
	}
	data, err := os.ReadFile(filepath.Join(configDir, "ios-builder", "mobai-client-id"))
	if err != nil {
		t.Fatalf("read client ID file: %v", err)
	}
	if string(data) != claims[0].ClientID {
		t.Fatalf("stored ID = %q, want %q", data, claims[0].ClientID)
	}
}

func TestClaimDeviceInUse(t *testing.T) {
	isolateConfigDir(t)
	f, srv := newFakeMobAI(t)
	f.setClaimStatus(http.StatusConflict)
	c := NewClient(srv.URL)
	ctx := context.Background()

	err := c.Claim(ctx, "dev1")
	var respErr *ResponseError
	if !errors.As(err, &respErr) || respErr.Code != "DEVICE_IN_USE" {
		t.Fatalf("Claim error = %v, want DEVICE_IN_USE", err)
	}
	if !strings.Contains(err.Error(), "stop the bridge") {
		t.Errorf("error %q has no hint about the running bridge", err)
	}
	if _, err := c.InstallApp(ctx, "dev1", InstallAppRequest{Path: "app.ipa"}); err != nil {
		t.Fatalf("InstallApp: %v", err)
	}
	if got := f.tokenSeen("/api/v1/devices/dev1/install-app"); got != "" {
		t.Errorf("X-Lease-Token = %q after a failed claim, want none", got)
	}
}

func TestClaimWithoutClaimSupport(t *testing.T) {
	isolateConfigDir(t)
	for _, status := range []int{http.StatusNotFound, http.StatusMethodNotAllowed} {
		f, srv := newFakeMobAI(t)
		f.setClaimStatus(status)
		c := NewClient(srv.URL)

		if err := c.Claim(context.Background(), "dev1"); err != nil {
			t.Errorf("status %d: Claim = %v, want nil", status, err)
		}
		if c.currentLease().token != "" {
			t.Errorf("status %d: lease token set without a claim", status)
		}
	}
}

func TestRenewReclaimsLapsedLease(t *testing.T) {
	isolateConfigDir(t)
	f, srv := newFakeMobAI(t)
	c := NewClient(srv.URL)
	ctx := context.Background()

	if err := c.Claim(ctx, "dev1"); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	f.setRenewStatus(http.StatusNotFound)
	if err := c.renew(ctx, c.currentLease()); err != nil {
		t.Fatalf("renew: %v", err)
	}
	if got := len(f.claimsMade()); got != 2 {
		t.Fatalf("claims = %d, want a second claim after the lease lapsed", got)
	}

	f.setClaimStatus(http.StatusConflict)
	if err := c.renew(ctx, c.currentLease()); err == nil {
		t.Fatal("renew = nil, want an error when someone else took the device")
	}
}

func TestDebugStreamReportsRejection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"device must be claimed first","code":"CLAIM_REQUIRED"}`))
	}))
	defer srv.Close()

	_, _, err := NewClient(srv.URL).DebugStream(context.Background(), "dev1", "com.example.app", nil)
	var respErr *ResponseError
	if !errors.As(err, &respErr) || respErr.Code != "CLAIM_REQUIRED" {
		t.Fatalf("DebugStream error = %v, want CLAIM_REQUIRED", err)
	}
}
