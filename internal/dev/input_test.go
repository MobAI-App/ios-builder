package dev

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MobAI-App/ios-builder/internal/exitcode"
	"github.com/MobAI-App/ios-builder/internal/mobai"
)

// fakeMobAI lists two physical devices and has no claim endpoint (MobAI
// before device claims), so Claim succeeds without a lease.
func fakeMobAI(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/devices" {
			_, _ = w.Write([]byte(`[{"id":"00008030-000A1B2C3D4E5F60","name":"iPhone A","platform":"ios"},{"id":"00008030-000A1B2C3D4E5F61","name":"iPhone B","platform":"ios"}]`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestDeviceChoiceWithoutTerminal(t *testing.T) {
	// Claim keeps a client ID in the user config dir; keep it out of $HOME.
	home := t.TempDir()
	for _, env := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "AppData"} {
		t.Setenv(env, home)
	}
	url := fakeMobAI(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	s := NewSession(url, "", "", nil)
	s.SetInput(Input{})
	err := s.connectDevice(ctx)
	var inputErr *InputError
	if !errors.As(err, &inputErr) || exitcode.Code(err) != exitcode.Usage || !strings.Contains(err.Error(), "--device") || !strings.Contains(err.Error(), "iPhone B") {
		t.Fatalf("err = %v, want an InputError naming --device and the devices", err)
	}

	var events []Event
	s = NewSession(url, "", "", nil)
	s.SetInput(Input{Yes: true})
	s.SetEvents(func(e Event) { events = append(events, e) })
	if err := s.connectDevice(ctx); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Event != "device" || events[0].DeviceName != "iPhone A" {
		t.Errorf("events = %+v, want the first device", events)
	}
}

func TestResignWithoutTerminal(t *testing.T) {
	yes, no := true, false
	tests := []struct {
		name    string
		in      Input
		resign  bool
		wantErr string
	}{
		{"default is no", Input{}, false, ""},
		{"explicit no", Input{Resign: &no}, false, ""},
		{"needs an Apple ID", Input{Resign: &yes}, false, "--apple-id"},
		{"needs a password", Input{Resign: &yes, AppleID: "a@example.com"}, false, "BUILDER_APPLE_ID_PASSWORD"},
		{"complete", Input{Resign: &yes, AppleID: "a@example.com", Password: "pw"}, true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewSession("http://unused", "", "", nil)
			s.SetInput(tt.in)
			var req mobai.InstallAppRequest
			err := s.resignRequest(&req)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) || exitcode.Code(err) != exitcode.Usage {
					t.Fatalf("err = %v, want a usage error naming %s", err, tt.wantErr)
				}
				return
			}
			if err != nil || req.Resign != tt.resign {
				t.Fatalf("resign = %v, err = %v; want %v", req.Resign, err, tt.resign)
			}
		})
	}
}

func TestFindIPAWithoutTerminalTakesNewest(t *testing.T) {
	dir := t.TempDir()
	old, newer := filepath.Join(dir, "a.ipa"), filepath.Join(dir, "b.ipa")
	for _, p := range []string{old, newer} {
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	got, err := FindIPA(dir, false)
	if err != nil || got != newer {
		t.Fatalf("FindIPA = %q, %v; want %q", got, err, newer)
	}
}
