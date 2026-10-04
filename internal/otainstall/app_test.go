package otainstall

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MobAI-App/ios-builder/internal/config"
)

const appPlist = `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>run.mobai.tapdash</string>
<key>CFBundleShortVersionString</key><string>1.0.0</string>
<key>CFBundleVersion</key><string>7</string>
<key>CFBundleName</key><string>TapDash</string>
<key>CFBundleDisplayName</key><string>Tap Dash Runner</string>
</dict></plist>`

// mobileprovision wraps a plist body in bytes standing in for the CMS envelope.
func mobileprovision(body string) string {
	return "\x30\x82\x1a\x00" + `<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict>` + body + `</dict></plist>` + "\x00\x00\xa1\xa1"
}

const (
	twoDevices  = `<key>ProvisionedDevices</key><array><string>00008030-001</string><string>00008030-002</string></array>`
	taskAllow   = `<key>Entitlements</key><dict><key>get-task-allow</key><true/></dict>`
	noTaskAllow = `<key>Entitlements</key><dict><key>get-task-allow</key><false/></dict>`
)

func writeIPA(t *testing.T, entries map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "App.ipa")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestInspect(t *testing.T) {
	for _, tc := range []struct {
		name, profile string
		want          Profile
		wantErr       string
	}{
		{"development", twoDevices + taskAllow, Profile{Type: config.DistributionDevelopment, Devices: 2}, ""},
		{"ad-hoc", twoDevices + noTaskAllow, Profile{Type: config.DistributionAdHoc, Devices: 2}, ""},
		{"enterprise", `<key>ProvisionsAllDevices</key><true/>` + noTaskAllow, Profile{Type: config.DistributionEnterprise}, ""},
		{"store", noTaskAllow, Profile{}, "use --backend testflight"},
		{"unsigned", "", Profile{}, "is unsigned"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entries := map[string]string{"Payload/App.app/Info.plist": appPlist}
			if tc.profile != "" {
				entries["Payload/App.app/embedded.mobileprovision"] = mobileprovision(tc.profile)
				// An embedded framework's profile must not be the one read.
				entries["Payload/App.app/Frameworks/X.framework/embedded.mobileprovision"] = mobileprovision(noTaskAllow)
			}
			app, err := Inspect(writeIPA(t, entries), BackendS3)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if app.Profile != tc.want || app.BundleID != "run.mobai.tapdash" || app.Version != "1.0.0" || app.Build != "7" || app.Title != "Tap Dash Runner" {
				t.Errorf("app = %+v", app)
			}
		})
	}
}

// The testflight backend wants the opposite signature of the OTA ones.
func TestInspectForTestFlight(t *testing.T) {
	for _, tc := range []struct {
		name, profile, wantErr string
	}{
		{"store", noTaskAllow, ""},
		{"development", twoDevices + taskAllow, "signed for development, but TestFlight takes only App Store"},
		{"ad-hoc", twoDevices + noTaskAllow, "signed for ad-hoc"},
		{"enterprise", `<key>ProvisionsAllDevices</key><true/>` + noTaskAllow, "signed for enterprise"},
		{"unsigned", "", "TestFlight needs an App Store signed IPA"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entries := map[string]string{"Payload/App.app/Info.plist": appPlist}
			if tc.profile != "" {
				entries["Payload/App.app/embedded.mobileprovision"] = mobileprovision(tc.profile)
			}
			app, err := Inspect(writeIPA(t, entries), BackendTestFlight)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if app.Profile.Type != config.DistributionStore || app.BundleID != "run.mobai.tapdash" {
				t.Errorf("app = %+v", app)
			}
		})
	}
}

func TestBackendName(t *testing.T) {
	cfg := &config.Config{Distribute: &config.DistributeConfig{Backend: "s3"}}
	for _, tc := range []struct {
		flag string
		cfg  *config.Config
		want string
	}{
		{"", nil, BackendGitHub},
		{"", &config.Config{}, BackendGitHub},
		{"", cfg, BackendS3},
		{"azure", cfg, BackendAzure},
		{"testflight", nil, BackendTestFlight},
	} {
		if got, err := BackendName(tc.flag, tc.cfg); err != nil || got != tc.want {
			t.Errorf("BackendName(%q) = %q, %v; want %q", tc.flag, got, err, tc.want)
		}
	}
	if _, err := BackendName("mobai", nil); err == nil || !strings.Contains(err.Error(), "unknown distribute backend") {
		t.Errorf("err = %v", err)
	}
}

func TestProfileString(t *testing.T) {
	if got := (Profile{Type: "development", Devices: 2}).String(); got != "development, 2 device(s)" {
		t.Errorf("got %q", got)
	}
	if got := (Profile{Type: "enterprise"}).String(); got != "enterprise" {
		t.Errorf("got %q", got)
	}
}

func TestCheckDistribution(t *testing.T) {
	for _, tc := range []struct {
		backend, profile, distribution, wantErr string
	}{
		{BackendGitHub, "dev", config.DistributionDevelopment, ""},
		{BackendS3, "internal", config.DistributionAdHoc, ""},
		{BackendAzure, "inhouse", config.DistributionEnterprise, ""},
		{BackendGitHub, "store", config.DistributionStore, `profile "store" has distribution store`},
		{BackendGitHub, "plain", "", `profile "plain" has no distribution`},
		{BackendS3, "", "", "pass --profile"},
		{BackendTestFlight, "store", config.DistributionStore, ""},
		{BackendTestFlight, "", "", ""},
		{BackendTestFlight, "dev", config.DistributionDevelopment, "TestFlight takes only App Store builds"},
		{BackendTestFlight, "inhouse", config.DistributionEnterprise, "TestFlight takes only App Store builds"},
		{BackendTestFlight, "plain", "", `profile "plain" has no distribution`},
	} {
		err := CheckDistribution(&config.BuildSettings{Profile: tc.profile, Distribution: tc.distribution}, tc.backend)
		if tc.wantErr == "" && err != nil {
			t.Errorf("%s %s/%s: %v", tc.backend, tc.profile, tc.distribution, err)
		}
		if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
			t.Errorf("%s %s/%s: err = %v, want %q", tc.backend, tc.profile, tc.distribution, err, tc.wantErr)
		}
	}
}

func TestAssetName(t *testing.T) {
	for in, want := range map[string]string{"Tap Dash Runner": "Tap-Dash-Runner.ipa", "app/../x": "app-..-x.ipa", "!!!": "app.ipa", "My.App": "My.App.ipa"} {
		if got := assetName(in); got != want {
			t.Errorf("assetName(%q) = %q, want %q", in, got, want)
		}
	}
}
