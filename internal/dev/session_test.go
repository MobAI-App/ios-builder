package dev

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"

	"github.com/MobAI-App/ios-builder/internal/mobai"
)

func writeTestIPA(t *testing.T, bundleID string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "App.ipa")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.Create("Payload/App.app/Info.plist")
	if err != nil {
		t.Fatal(err)
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict><key>CFBundleIdentifier</key><string>` + bundleID + `</string></dict></plist>`
	if _, err := w.Write([]byte(plist)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExtractBundleIDFromIPA(t *testing.T) {
	path := writeTestIPA(t, "com.example.app")
	if got := extractBundleIDFromIPA(path); got != "com.example.app" {
		t.Errorf("extractBundleIDFromIPA = %q, want com.example.app", got)
	}
	if got := extractBundleIDFromIPA(filepath.Join(t.TempDir(), "missing.ipa")); got != "" {
		t.Errorf("missing IPA = %q, want empty", got)
	}
}

func TestGuessBundleID(t *testing.T) {
	response := func(teamID string) *mobai.InstallAppResponse {
		resp := &mobai.InstallAppResponse{}
		resp.Data.TeamID = teamID
		return resp
	}

	tests := []struct {
		name     string
		resp     *mobai.InstallAppResponse
		ipaID    string
		resigned bool
		want     string
	}{
		{"as-is install keeps IPA ID", response(""), "com.example.app", false, "com.example.app"},
		{"as-is install ignores team ID", response("TEAM"), "com.example.app", false, "com.example.app"},
		{"re-sign appends team ID", response("TEAM"), "com.example.app", true, "com.example.app.TEAM"},
		{"re-sign without team ID keeps IPA ID", response(""), "com.example.app", true, "com.example.app"},
		{"unreadable IPA has no guess", response("TEAM"), "", true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := guessBundleID(tt.resp, tt.ipaID, tt.resigned); got != tt.want {
				t.Errorf("guessBundleID = %q, want %q", got, tt.want)
			}
		})
	}
}
