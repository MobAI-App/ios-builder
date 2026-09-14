package mobai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/gorilla/websocket"
)

func TestDotEnvValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	content := "# MobAI\r\n" +
		"OTHER=1\r\n" +
		"export MOBAI_ACCESS_KEY = \"quoted key\"\r\n" +
		"PLAIN=value # comment\n" +
		"SINGLE='x=y'\n" +
		"EMPTY=\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	for key, want := range map[string]string{
		"MOBAI_ACCESS_KEY": "quoted key",
		"PLAIN":            "value",
		"SINGLE":           "x=y",
		"EMPTY":            "",
		"MISSING":          "",
	} {
		if got := dotEnvValue(path, key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if got := dotEnvValue(filepath.Join(t.TempDir(), ".env"), "PLAIN"); got != "" {
		t.Errorf("missing file = %q, want empty", got)
	}
}

func TestAccessKeySource(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv(accessKeyEnv, "")
	if got := accessKey(); got != "" {
		t.Fatalf("no env, no .env: accessKey = %q, want empty", got)
	}

	if err := os.WriteFile(".env", []byte("MOBAI_ACCESS_KEY=from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := accessKey(); got != "from-file" {
		t.Errorf(".env: accessKey = %q, want from-file", got)
	}

	t.Setenv(accessKeyEnv, "from-env")
	if got := accessKey(); got != "from-env" {
		t.Errorf("env set: accessKey = %q, want the environment to win", got)
	}
}

func TestAccessKeySentOnEveryCall(t *testing.T) {
	isolateConfigDir(t)
	t.Chdir(t.TempDir())
	t.Setenv(accessKeyEnv, "secret")

	var mu sync.Mutex
	seen := map[string]string{} // path -> X-API-Key
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.URL.Path] = r.Header.Get(accessKeyHeader)
		mu.Unlock()
		switch r.URL.Path {
		case "/api/v1/devices":
			_, _ = w.Write([]byte(`[]`))
		case "/api/v1/devices/dev1/debug":
			conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
			if err == nil {
				_ = conn.Close()
			}
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	ctx := context.Background()
	if _, err := c.ListDevices(ctx); err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	_, conn, err := c.DebugStream(ctx, "dev1", "com.example.app", nil)
	if err != nil {
		t.Fatalf("DebugStream: %v", err)
	}
	_ = conn.Close()

	mu.Lock()
	defer mu.Unlock()
	for _, path := range []string{"/api/v1/devices", "/api/v1/devices/dev1/debug"} {
		if got := seen[path]; got != "secret" {
			t.Errorf("%s: %s = %q, want secret", path, accessKeyHeader, got)
		}
	}
}
