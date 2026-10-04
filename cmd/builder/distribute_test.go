package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MobAI-App/ios-builder/internal/config"
)

// TestBuildDistributePreflight: --distribute is checked against the profile
// before anything is pushed, and cannot be combined with --submit or --unsigned.
func TestBuildDistributePreflight(t *testing.T) {
	t.Chdir(t.TempDir())
	cfg := &config.Config{Project: "App", Platform: "ios", GitHub: config.GitHubConfig{Owner: "o", Repo: "r"}, Profiles: map[string]config.Profile{
		"store": {Distribution: "store"},
		"plain": {},
	}}
	if err := config.NewManager().Save(cfg); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--submit", "--distribute"}, "only one of --submit"},
		{[]string{"--distribute", "--unsigned"}, "drop --unsigned"},
		{[]string{"--distribute", "--profile", "store"}, `profile "store" has distribution store`},
		{[]string{"--distribute", "--profile", "plain"}, `profile "plain" has no distribution`},
		{[]string{"--distribute"}, "pass --profile"},
	} {
		_, _, err := run(t, append([]string{"ios", "build"}, tc.args...)...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: err = %v, want %q", tc.args, err, tc.want)
		}
	}
}

func TestDistributeNeedsAnIPA(t *testing.T) {
	t.Chdir(t.TempDir())
	cfg := &config.Config{Project: "App", Platform: "ios", GitHub: config.GitHubConfig{Owner: "o", Repo: "r"}}
	if err := config.NewManager().Save(cfg); err != nil {
		t.Fatal(err)
	}
	_, _, err := run(t, "ios", "distribute")
	if err == nil || !strings.Contains(err.Error(), "no .ipa found in dist") {
		t.Errorf("err = %v", err)
	}
	_, _, err = run(t, "ios", "distribute", "--ipa", "missing.ipa")
	if err == nil || !strings.Contains(err.Error(), "open IPA") {
		t.Errorf("err = %v", err)
	}
}

// TestBuildDistributeBackendPreflight: the backend's needs are checked before
// anything is pushed, and the backend flags need --distribute.
func TestBuildDistributeBackendPreflight(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("AWS_ACCESS_KEY_ID", "AK")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "SK")
	t.Setenv("AZURE_STORAGE_CONNECTION_STRING", "")
	t.Setenv("AZURE_STORAGE_ACCOUNT", "")
	t.Setenv("AZURE_STORAGE_KEY", "")
	cfg := &config.Config{Project: "App", Platform: "ios", GitHub: config.GitHubConfig{Owner: "o", Repo: "r"}, Profiles: map[string]config.Profile{
		"store": {Distribution: "store"},
		"dev":   {Distribution: "development"},
	}}
	if err := config.NewManager().Save(cfg); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--backend", "s3"}, "--backend goes with --distribute"},
		{[]string{"--submit", "--group", "Team"}, "--group goes with --distribute"},
		{[]string{"--distribute", "--backend", "ftp", "--profile", "dev"}, `unknown distribute backend "ftp"`},
		{[]string{"--distribute", "--backend", "testflight", "--profile", "dev"}, "TestFlight takes only App Store builds"},
		{[]string{"--distribute", "--backend", "testflight", "--profile", "store"}, "needs an internal TestFlight group"},
		{[]string{"--distribute", "--backend", "s3", "--profile", "store"}, `profile "store" has distribution store`},
		{[]string{"--distribute", "--backend", "s3", "--profile", "dev"}, "needs a bucket"},
		{[]string{"--distribute", "--backend", "s3", "--profile", "dev", "--group", "Team"}, "--group applies to --backend testflight"},
		{[]string{"--distribute", "--backend", "azure", "--profile", "dev"}, "needs an account and a container"},
		{[]string{"--distribute", "--profile", "dev", "--ttl", "1h"}, "--ttl applies to the s3 and azure backends"},
	} {
		_, _, err := run(t, append([]string{"ios", "build"}, tc.args...)...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: err = %v, want %q", tc.args, err, tc.want)
		}
	}

	// distribute.backend in builder.json applies without the flag.
	cfg.Distribute = &config.DistributeConfig{Backend: "s3", Bucket: "b"}
	if err := config.NewManager().Save(cfg); err != nil {
		t.Fatal(err)
	}
	_, _, err := run(t, "ios", "build", "--distribute", "--profile", "dev", "--ttl", "200h")
	if err == nil || !strings.Contains(err.Error(), "--ttl must be between") {
		t.Errorf("config backend: err = %v", err)
	}
}

func writeSignedIPA(t *testing.T, profileBody string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "App.ipa")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range map[string]string{
		"Payload/App.app/Info.plist":               `<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>com.example.app</string><key>CFBundleShortVersionString</key><string>1.0</string><key>CFBundleVersion</key><string>3</string></dict></plist>`,
		"Payload/App.app/embedded.mobileprovision": "\x30\x82" + `<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict>` + profileBody + `</dict></plist>`,
	} {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDistributeBackendChecksTheIPA(t *testing.T) {
	t.Chdir(t.TempDir())
	cfg := &config.Config{Project: "App", Platform: "ios", GitHub: config.GitHubConfig{Owner: "o", Repo: "r"}, Distribute: &config.DistributeConfig{Group: "Team"}}
	if err := config.NewManager().Save(cfg); err != nil {
		t.Fatal(err)
	}
	dev := writeSignedIPA(t, `<key>ProvisionedDevices</key><array><string>u1</string></array><key>Entitlements</key><dict><key>get-task-allow</key><true/></dict>`)
	store := writeSignedIPA(t, `<key>Entitlements</key><dict><key>get-task-allow</key><false/></dict>`)
	_, _, err := run(t, "ios", "distribute", "--backend", "testflight", "--ipa", dev)
	if err == nil || !strings.Contains(err.Error(), "TestFlight takes only App Store signed builds") {
		t.Errorf("dev IPA to testflight: %v", err)
	}
	t.Setenv("AWS_ACCESS_KEY_ID", "AK")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "SK")
	cfg.Distribute.Bucket = "b"
	if err := config.NewManager().Save(cfg); err != nil {
		t.Fatal(err)
	}
	_, _, err = run(t, "ios", "distribute", "--backend", "s3", "--ipa", store)
	if err == nil || !strings.Contains(err.Error(), "use --backend testflight") {
		t.Errorf("store IPA over the air: %v", err)
	}
	_, _, err = run(t, "ios", "distribute", "--backend", "testflight", "--cleanup")
	if err == nil || !strings.Contains(err.Error(), "leaves nothing to clean up") {
		t.Errorf("testflight cleanup: %v", err)
	}
}
