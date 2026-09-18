package otainstall

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"howett.net/plist"
)

// manifest is Apple's over-the-air install manifest.
type manifest struct {
	Items []manifestItem `plist:"items"`
}

type manifestItem struct {
	Assets   []manifestAsset  `plist:"assets"`
	Metadata manifestMetadata `plist:"metadata"`
}

type manifestAsset struct {
	Kind string `plist:"kind"`
	URL  string `plist:"url"`
}

type manifestMetadata struct {
	BundleID      string `plist:"bundle-identifier"`
	BundleVersion string `plist:"bundle-version"`
	Kind          string `plist:"kind"`
	Title         string `plist:"title"`
}

// Manifest renders the manifest.plist that points the installer at ipaURL.
func Manifest(app *App, ipaURL string) ([]byte, error) {
	m := manifest{Items: []manifestItem{{
		Assets:   []manifestAsset{{Kind: "software-package", URL: ipaURL}},
		Metadata: manifestMetadata{BundleID: app.BundleID, BundleVersion: app.Version, Kind: "software", Title: app.Title},
	}}}
	data, err := plist.MarshalIndent(m, plist.XMLFormat, "  ")
	if err != nil {
		return nil, fmt.Errorf("render manifest: %w", err)
	}
	return data, nil
}

// linkEscaper hides only what would break the outer query string; a fully
// percent-encoded URL costs a QR version or two.
var linkEscaper = strings.NewReplacer("%", "%25", "&", "%26", "#", "%23", "?", "%3F", " ", "%20")

// Link is the itms-services URL iOS opens the installer for.
func Link(manifestURL string) string {
	return "itms-services://?action=download-manifest&url=" + linkEscaper.Replace(manifestURL)
}

// DefaultTTL is how long a signed release-asset URL lives when the URL does
// not say (it normally carries a JWT with the exact time).
const DefaultTTL = 5 * time.Minute

// Expiry reads the expiry of a signed URL from the exp claim of its jwt query
// parameter, else now plus DefaultTTL. A claim already in the past means the
// clocks disagree, and the fallback keeps the refresh loop from spinning.
func Expiry(signedURL string, now time.Time) time.Time {
	fallback := now.Add(DefaultTTL)
	u, err := url.Parse(signedURL)
	if err != nil {
		return fallback
	}
	parts := strings.Split(u.Query().Get("jwt"), ".")
	if len(parts) != 3 {
		return fallback
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return fallback
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Exp == 0 {
		return fallback
	}
	exp := time.Unix(claims.Exp, 0)
	if !exp.After(now) {
		return fallback
	}
	return exp
}
