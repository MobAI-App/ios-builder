package signing

import (
	"bytes"
	"maps"
	"strings"
	"testing"
)

func TestExtensionProfilesRoundTrip(t *testing.T) {
	profiles := map[string][]byte{"com.example.app.widget": []byte("widget\x00bytes"), "com.example.app.share": []byte("share")}
	secret := EncodeExtensionProfiles(profiles)
	if !strings.HasPrefix(secret, `{"com.example.app.share":"c2hhcmU=","com.example.app.widget":"`) {
		t.Errorf("secret = %s", secret)
	}
	got, err := DecodeExtensionProfiles(secret)
	if err != nil || !maps.EqualFunc(got, profiles, bytes.Equal) {
		t.Errorf("decoded = %q, %v", got, err)
	}
	if got, err := DecodeExtensionProfiles(""); err != nil || len(got) != 0 {
		t.Errorf("empty secret = %q, %v", got, err)
	}
	if EncodeExtensionProfiles(nil) != "{}" {
		t.Errorf("nil encodes as %s", EncodeExtensionProfiles(nil))
	}
	for _, bad := range []string{"[]", `{"a":"not base64!"}`, "nope"} {
		if _, err := DecodeExtensionProfiles(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestProfileBundleID(t *testing.T) {
	team := "<key>TeamIdentifier</key><array><string>ABCDE12345</string></array>"
	for name, tc := range map[string]struct{ body, want string }{
		"explicit":             {team + "<key>Entitlements</key><dict><key>application-identifier</key><string>ABCDE12345.com.example.app.widget</string></dict>", "com.example.app.widget"},
		"wildcard":             {team + "<key>Entitlements</key><dict><key>application-identifier</key><string>ABCDE12345.com.example.*</string></dict>", "com.example.*"},
		"no TeamIdentifier":    {"<key>Entitlements</key><dict><key>application-identifier</key><string>ABCDE12345.com.example.app</string></dict>", "com.example.app"},
		"other team in prefix": {team + "<key>Entitlements</key><dict><key>application-identifier</key><string>ZZZZZ99999.com.example.app</string></dict>", "com.example.app"},
	} {
		if got, err := ProfileBundleID(mobileprovision(tc.body)); err != nil || got != tc.want {
			t.Errorf("%s: ProfileBundleID = %q, %v; want %q", name, got, err, tc.want)
		}
	}
	for name, body := range map[string]string{"no entitlement": team, "no team prefix": "<key>Entitlements</key><dict><key>application-identifier</key><string>bare</string></dict>"} {
		if _, err := ProfileBundleID(mobileprovision(body)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := ProfileBundleID([]byte("not a profile")); err == nil {
		t.Error("unreadable profile accepted")
	}
}

func TestCovers(t *testing.T) {
	for _, tc := range []struct {
		appID, bundleID string
		want            bool
	}{
		{"com.example.app.widget", "com.example.app.widget", true},
		{"com.example.app", "com.example.app.widget", false},
		{"com.example.*", "com.example.app.widget", true},
		{"*", "com.example.app", true},
		{"com.example.*", "org.other.app", false},
	} {
		if got := Covers(tc.appID, tc.bundleID); got != tc.want {
			t.Errorf("Covers(%q, %q) = %v", tc.appID, tc.bundleID, got)
		}
	}
}
