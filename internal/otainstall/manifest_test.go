package otainstall

import (
	"encoding/base64"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestManifest(t *testing.T) {
	app := &App{BundleID: "run.mobai.tapdash", Version: "1.0.0", Build: "7", Title: "Tap & Dash"}
	data, err := Manifest(app, "https://example.com/app.ipa?sig=a&b=c")
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{
		`<key>items</key>`,
		`<key>kind</key>` + "\n" + `            <string>software-package</string>`,
		`<string>https://example.com/app.ipa?sig=a&amp;b=c</string>`,
		`<key>bundle-identifier</key>` + "\n" + `          <string>run.mobai.tapdash</string>`,
		`<key>bundle-version</key>` + "\n" + `          <string>1.0.0</string>`,
		`<string>software</string>`,
		`<string>Tap &amp; Dash</string>`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("manifest lacks %q:\n%s", want, got)
		}
	}
}

func TestLinkEscapesManifestURL(t *testing.T) {
	got := Link("https://gist.githubusercontent.com/u/abc/raw/def/manifest.plist")
	want := "itms-services://?action=download-manifest&url=https%3A%2F%2Fgist.githubusercontent.com%2Fu%2Fabc%2Fraw%2Fdef%2Fmanifest.plist"
	if got != want {
		t.Errorf("Link = %q, want %q", got, want)
	}
}

func jwtWithExp(exp int64) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"iss":"github.com","exp":` + strconv.FormatInt(exp, 10) + `,"nbf":1}`))
	return "eyJhbGciOiJIUzI1NiJ9." + payload + ".sig"
}

func TestExpiryReadsJWT(t *testing.T) {
	now := time.Date(2026, 9, 18, 7, 0, 0, 0, time.UTC)
	exp := now.Add(300 * time.Second).Unix()
	signed := "https://release-assets.githubusercontent.com/x/y?se=2026-09-18T08%3A00%3A00Z&jwt=" + jwtWithExp(exp) + "&sig=abc"
	if got := Expiry(signed, now); !got.Equal(time.Unix(exp, 0)) {
		t.Errorf("Expiry = %v, want %v", got, time.Unix(exp, 0))
	}
	for name, u := range map[string]string{
		"no jwt":      "https://example.com/a?sig=1",
		"bad jwt":     "https://example.com/a?jwt=not.a.jwt",
		"not base64":  "https://example.com/a?jwt=x.%%%.y",
		"no exp":      "https://example.com/a?jwt=h." + base64.RawURLEncoding.EncodeToString([]byte(`{"iss":"x"}`)) + ".s",
		"unparseable": "://nope",
	} {
		if got := Expiry(u, now); !got.Equal(now.Add(DefaultTTL)) {
			t.Errorf("%s: Expiry = %v, want fallback %v", name, got, now.Add(DefaultTTL))
		}
	}
}
