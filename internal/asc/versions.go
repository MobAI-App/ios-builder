package asc

import (
	"context"
	"net/url"
	"time"
)

// Release types of an App Store version.
const (
	ReleaseTypeManual        = "MANUAL"
	ReleaseTypeAfterApproval = "AFTER_APPROVAL"
	ReleaseTypeScheduled     = "SCHEDULED"
)

// AppStoreVersion is a version of the app on the App Store.
type AppStoreVersion struct {
	ID            string
	Platform      string
	VersionString string
	// State is appVersionState (e.g. PREPARE_FOR_SUBMISSION, READY_FOR_REVIEW,
	// WAITING_FOR_REVIEW, IN_REVIEW, READY_FOR_DISTRIBUTION).
	State         string
	AppStoreState string
	ReleaseType   string
	BuildID       string
	CreatedDate   time.Time
}

type appStoreVersionAttributes struct {
	Platform        string     `json:"platform,omitempty"`
	VersionString   string     `json:"versionString,omitempty"`
	AppStoreState   string     `json:"appStoreState,omitempty"`
	AppVersionState string     `json:"appVersionState,omitempty"`
	ReleaseType     string     `json:"releaseType,omitempty"`
	CreatedDate     *time.Time `json:"createdDate,omitempty"`
}

func toAppStoreVersion(r Resource[appStoreVersionAttributes]) AppStoreVersion {
	v := AppStoreVersion{
		ID:            r.ID,
		Platform:      r.Attributes.Platform,
		VersionString: r.Attributes.VersionString,
		State:         r.Attributes.AppVersionState,
		AppStoreState: r.Attributes.AppStoreState,
		ReleaseType:   r.Attributes.ReleaseType,
	}
	if r.Attributes.CreatedDate != nil {
		v.CreatedDate = *r.Attributes.CreatedDate
	}
	if l, ok := r.Relationships.One("build"); ok {
		v.BuildID = l.ID
	}
	return v
}

// ListAppStoreVersions lists the app's versions for a platform, optionally one version string.
func (c *Client) ListAppStoreVersions(ctx context.Context, appID, platform, versionString string) ([]AppStoreVersion, error) {
	q := url.Values{"include": {"build"}}
	if platform != "" {
		q.Set("filter[platform]", platform)
	}
	if versionString != "" {
		q.Set("filter[versionString]", versionString)
	}
	rs, err := getAll[appStoreVersionAttributes](ctx, c, "/v1/apps/"+appID+"/appStoreVersions", q)
	if err != nil {
		return nil, err
	}
	versions := make([]AppStoreVersion, 0, len(rs))
	for _, r := range rs {
		versions = append(versions, toAppStoreVersion(r))
	}
	return versions, nil
}

// CreateAppStoreVersion adds a new version to the app.
func (c *Client) CreateAppStoreVersion(ctx context.Context, appID, platform, versionString string) (*AppStoreVersion, error) {
	req := Resource[appStoreVersionAttributes]{
		Type:          "appStoreVersions",
		Attributes:    appStoreVersionAttributes{Platform: platform, VersionString: versionString},
		Relationships: Relationships{"app": ToOne("apps", appID)},
	}
	r, err := post[appStoreVersionAttributes, appStoreVersionAttributes](ctx, c, "/v1/appStoreVersions", req)
	if err != nil {
		return nil, err
	}
	v := toAppStoreVersion(*r)
	return &v, nil
}

// AppStoreVersionUpdate lists the fields UpdateAppStoreVersion changes; empty ones are left alone.
type AppStoreVersionUpdate struct {
	ReleaseType string
	BuildID     string
}

// UpdateAppStoreVersion attaches a build and/or sets the release type.
func (c *Client) UpdateAppStoreVersion(ctx context.Context, id string, u AppStoreVersionUpdate) (*AppStoreVersion, error) {
	req := Resource[appStoreVersionAttributes]{Type: "appStoreVersions", ID: id, Attributes: appStoreVersionAttributes{ReleaseType: u.ReleaseType}}
	if u.BuildID != "" {
		req.Relationships = Relationships{"build": ToOne("builds", u.BuildID)}
	}
	r, err := patch[appStoreVersionAttributes, appStoreVersionAttributes](ctx, c, "/v1/appStoreVersions/"+id, req)
	if err != nil {
		return nil, err
	}
	v := toAppStoreVersion(*r)
	if v.BuildID == "" {
		v.BuildID = u.BuildID
	}
	return &v, nil
}
