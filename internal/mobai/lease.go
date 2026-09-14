package mobai

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MobAI lets a client drive a device only after claiming it when the request
// comes over the network (builder in WSL reaching MobAI on Windows), or from
// the MobAI host itself with its "Require device claim" setting on. Anything
// else fails with 409 CLAIM_REQUIRED.

const (
	leaseTokenHeader = "X-Lease-Token"

	// leaseTTL is how long a claim outlives its last renewal. When it lapses
	// MobAI frees the device and stops a bridge a network claimer left running.
	leaseTTL = 10 * time.Minute

	// minRenewInterval bounds how often KeepLease renews, whatever TTL MobAI
	// granted. MobAI caps claims from its own host at its idle timeout setting.
	minRenewInterval = 10 * time.Second
)

// lease is a claim MobAI granted on one device.
type lease struct {
	deviceID string
	token    string
	ttl      time.Duration
}

type claimRequest struct {
	Device     string `json:"device"`
	TTLSeconds int    `json:"ttlSeconds"`
	Holder     string `json:"holder,omitempty"`
	ClientID   string `json:"clientId,omitempty"`
}

type claimResponse struct {
	LeaseToken string `json:"leaseToken"`
	ExpiresAt  string `json:"expiresAt"`
}

// Claim leases deviceID and sends the lease with every later request,
// including the debug WebSocket.
//
// MobAI uses this install's client ID as the lease token, which makes the
// claim idempotent: the separate builder processes Flutter starts (install,
// run-debug, forward) share one lease, and a restarted builder takes its own
// lease back instead of waiting for it to expire.
//
// builder never releases the lease. A bridge keeps running after release, and
// MobAI refuses network claims on a running bridge nobody has claimed, so the
// next builder run from WSL would be locked out. The lease lapses on its own.
//
// MobAI versions without device claims have no claim endpoint and need no
// claim, so that is not an error.
func (c *Client) Claim(ctx context.Context, deviceID string) error {
	req := claimRequest{
		Device:     deviceID,
		TTLSeconds: int(leaseTTL / time.Second),
		Holder:     holderName(),
		ClientID:   clientID(),
	}
	var resp claimResponse
	err := c.do(ctx, "POST", "/api/v1/devices/claim", req, &resp)
	var respErr *ResponseError
	if errors.As(err, &respErr) {
		switch respErr.StatusCode {
		case http.StatusNotFound, http.StatusMethodNotAllowed:
			return nil
		case http.StatusConflict:
			return fmt.Errorf("claim device %s: %w (another client holds it, or its bridge was started from the MobAI app: stop the bridge there and retry)", deviceID, err)
		}
	}
	if err != nil {
		return fmt.Errorf("claim device %s: %w", deviceID, err)
	}

	ttl := leaseTTL
	if expires, err := time.Parse(time.RFC3339, resp.ExpiresAt); err == nil {
		ttl = time.Until(expires)
	}
	c.mu.Lock()
	c.lease = lease{deviceID: deviceID, token: resp.LeaseToken, ttl: ttl}
	c.mu.Unlock()
	return nil
}

// KeepLease renews the claim until ctx is done. MobAI renews a lease only when
// a request uses it, and a debug stream or port forward makes no requests
// after it starts, so long-running commands would lose the device without it.
func (c *Client) KeepLease(ctx context.Context) {
	for {
		l := c.currentLease()
		if l.token == "" {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(max(l.ttl/3, minRenewInterval)):
		}
		if err := c.renew(ctx, l); err != nil {
			if ctx.Err() == nil {
				fmt.Fprintf(os.Stderr, "Warning: lost the MobAI device lease: %v\n", err)
			}
			return
		}
	}
}

// renew extends l. A lease that lapsed anyway, say while the laptop slept, is
// claimed again. Other failures are left for the next renewal.
func (c *Client) renew(ctx context.Context, l lease) error {
	err := c.do(ctx, "POST", "/api/v1/devices/renew", map[string]string{"leaseToken": l.token}, nil)
	var respErr *ResponseError
	if errors.As(err, &respErr) && respErr.StatusCode == http.StatusNotFound {
		return c.Claim(ctx, l.deviceID)
	}
	return nil
}

func (c *Client) currentLease() lease {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lease
}

// holderName is how MobAI names builder when it reports the device in use.
func holderName() string {
	if host, err := os.Hostname(); err == nil && host != "" {
		return "builder on " + host
	}
	return "builder"
}

// clientID returns this install's claim ID, created on first use. It returns
// "" when the ID can't be stored; MobAI then generates a token per claim.
func clientID() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	path := filepath.Join(dir, "ios-builder", "mobai-client-id")
	if data, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(data)); id != "" {
			return id
		}
	}

	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return ""
	}
	id := hex.EncodeToString(buf)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return ""
	}
	if err := os.WriteFile(path, []byte(id), 0o600); err != nil {
		return ""
	}
	return id
}
