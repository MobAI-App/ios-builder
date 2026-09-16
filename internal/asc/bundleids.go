package asc

import (
	"context"
	"net/url"
)

// BundleID is a registered App ID (Certificates, Identifiers & Profiles → Identifiers).
type BundleID struct {
	ID         string
	Identifier string
	Name       string
	Platform   string
	SeedID     string
}

type bundleIDAttributes struct {
	Identifier string `json:"identifier,omitempty"`
	Name       string `json:"name,omitempty"`
	Platform   string `json:"platform,omitempty"`
	SeedID     string `json:"seedId,omitempty"`
}

func toBundleID(r Resource[bundleIDAttributes]) BundleID {
	return BundleID{ID: r.ID, Identifier: r.Attributes.Identifier, Name: r.Attributes.Name, Platform: r.Attributes.Platform, SeedID: r.Attributes.SeedID}
}

// BundleIDByIdentifier finds the App ID registered for an exact bundle
// identifier, or returns nil when none is.
func (c *Client) BundleIDByIdentifier(ctx context.Context, identifier string) (*BundleID, error) {
	rs, err := getAll[bundleIDAttributes](ctx, c, "/v1/bundleIds", url.Values{"filter[identifier]": {identifier}})
	if err != nil {
		return nil, err
	}
	for _, r := range rs {
		// The filter also matches wildcard and prefixed identifiers.
		if r.Attributes.Identifier == identifier {
			b := toBundleID(r)
			return &b, nil
		}
	}
	return nil, nil
}

// CreateBundleID registers an App ID. platform is PlatformIOS for iOS apps.
func (c *Client) CreateBundleID(ctx context.Context, identifier, name, platform string) (*BundleID, error) {
	req := Resource[bundleIDAttributes]{Type: "bundleIds", Attributes: bundleIDAttributes{Identifier: identifier, Name: name, Platform: platform}}
	r, err := post[bundleIDAttributes, bundleIDAttributes](ctx, c, "/v1/bundleIds", req)
	if err != nil {
		return nil, err
	}
	b := toBundleID(*r)
	return &b, nil
}
