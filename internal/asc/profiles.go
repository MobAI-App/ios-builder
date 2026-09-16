package asc

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"time"
)

// iOS profile types.
const (
	ProfileTypeIOSAppDevelopment = "IOS_APP_DEVELOPMENT"
	ProfileTypeIOSAppAdHoc       = "IOS_APP_ADHOC"
	ProfileTypeIOSAppStore       = "IOS_APP_STORE"
)

// Profile states. A profile turns INVALID when a certificate or device in it
// is revoked, removed or expired; Apple does not repair it, it must be recreated.
const (
	ProfileStateActive  = "ACTIVE"
	ProfileStateInvalid = "INVALID"
)

// Profile is a provisioning profile.
type Profile struct {
	ID             string
	Name           string
	UUID           string
	Type           string
	State          string
	Platform       string
	CreatedDate    time.Time
	ExpirationDate time.Time
	// Content is the .mobileprovision file.
	Content []byte
}

type profileAttributes struct {
	Name           string     `json:"name,omitempty"`
	Platform       string     `json:"platform,omitempty"`
	ProfileContent string     `json:"profileContent,omitempty"`
	UUID           string     `json:"uuid,omitempty"`
	CreatedDate    *time.Time `json:"createdDate,omitempty"`
	ProfileState   string     `json:"profileState,omitempty"`
	ProfileType    string     `json:"profileType,omitempty"`
	ExpirationDate *time.Time `json:"expirationDate,omitempty"`
}

func toProfile(r Resource[profileAttributes]) (Profile, error) {
	p := Profile{
		ID:       r.ID,
		Name:     r.Attributes.Name,
		UUID:     r.Attributes.UUID,
		Type:     r.Attributes.ProfileType,
		State:    r.Attributes.ProfileState,
		Platform: r.Attributes.Platform,
	}
	if r.Attributes.CreatedDate != nil {
		p.CreatedDate = *r.Attributes.CreatedDate
	}
	if r.Attributes.ExpirationDate != nil {
		p.ExpirationDate = *r.Attributes.ExpirationDate
	}
	if r.Attributes.ProfileContent != "" {
		data, err := base64.StdEncoding.DecodeString(r.Attributes.ProfileContent)
		if err != nil {
			return p, fmt.Errorf("profile %s: decode profileContent: %w", r.ID, err)
		}
		p.Content = data
	}
	return p, nil
}

// ListProfilesByName lists the profiles with exactly the given name; the
// portal allows duplicates.
func (c *Client) ListProfilesByName(ctx context.Context, name string) ([]Profile, error) {
	rs, err := getAll[profileAttributes](ctx, c, "/v1/profiles", url.Values{"filter[name]": {name}})
	if err != nil {
		return nil, err
	}
	profiles := make([]Profile, 0, len(rs))
	for _, r := range rs {
		if r.Attributes.Name != name {
			continue
		}
		p, err := toProfile(r)
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, p)
	}
	return profiles, nil
}

// ProfileCertificateIDs returns the IDs of the certificates in a profile.
func (c *Client) ProfileCertificateIDs(ctx context.Context, profileID string) ([]string, error) {
	return c.relatedIDs(ctx, "/v1/profiles/"+profileID+"/relationships/certificates")
}

// ProfileDeviceIDs returns the IDs of the devices in a profile.
func (c *Client) ProfileDeviceIDs(ctx context.Context, profileID string) ([]string, error) {
	return c.relatedIDs(ctx, "/v1/profiles/"+profileID+"/relationships/devices")
}

// relatedIDs reads a paginated to-many relationship endpoint.
func (c *Client) relatedIDs(ctx context.Context, path string) ([]string, error) {
	rs, err := getAll[struct{}](ctx, c, path, nil)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(rs))
	for _, r := range rs {
		ids = append(ids, r.ID)
	}
	return ids, nil
}

// CreateProfile creates a profile for the App ID with the given certificates
// and devices. deviceIDs must be nil for ProfileTypeIOSAppStore.
func (c *Client) CreateProfile(ctx context.Context, name, profileType, bundleIDResourceID string, certificateIDs, deviceIDs []string) (*Profile, error) {
	rels := Relationships{
		"bundleId":     ToOne("bundleIds", bundleIDResourceID),
		"certificates": ToMany("certificates", certificateIDs),
	}
	if deviceIDs != nil {
		rels["devices"] = ToMany("devices", deviceIDs)
	}
	req := Resource[profileAttributes]{Type: "profiles", Attributes: profileAttributes{Name: name, ProfileType: profileType}, Relationships: rels}
	r, err := post[profileAttributes, profileAttributes](ctx, c, "/v1/profiles", req)
	if err != nil {
		return nil, err
	}
	p, err := toProfile(*r)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// DeleteProfile removes a profile. Certificates and devices are untouched.
func (c *Client) DeleteProfile(ctx context.Context, profileID string) error {
	return c.Delete(ctx, "/v1/profiles/"+profileID, nil)
}
