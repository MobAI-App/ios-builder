package asc

import (
	"context"
	"net/url"
	"strconv"
	"time"
)

// PlatformIOS is the App Store Connect platform value for iOS.
const PlatformIOS = "IOS"

// Build processing states.
const (
	ProcessingStateProcessing = "PROCESSING"
	ProcessingStateFailed     = "FAILED"
	ProcessingStateInvalid    = "INVALID"
	ProcessingStateValid      = "VALID"
)

// Build is a processed (or processing) build of an app.
type Build struct {
	ID              string
	BuildNumber     string // CFBundleVersion; ASC calls it "version"
	ProcessingState string
	UploadedDate    time.Time
	ExpirationDate  time.Time
	Expired         bool
	MinOSVersion    string
	// UsesNonExemptEncryption is nil while the export compliance question is
	// unanswered ("Missing Compliance" in TestFlight).
	UsesNonExemptEncryption *bool
}

type buildAttributes struct {
	Version                 string     `json:"version,omitempty"`
	UploadedDate            *time.Time `json:"uploadedDate,omitempty"`
	ExpirationDate          *time.Time `json:"expirationDate,omitempty"`
	Expired                 *bool      `json:"expired,omitempty"`
	MinOsVersion            string     `json:"minOsVersion,omitempty"`
	ProcessingState         string     `json:"processingState,omitempty"`
	UsesNonExemptEncryption *bool      `json:"usesNonExemptEncryption,omitempty"`
}

func toBuild(r Resource[buildAttributes]) Build {
	b := Build{
		ID:                      r.ID,
		BuildNumber:             r.Attributes.Version,
		ProcessingState:         r.Attributes.ProcessingState,
		MinOSVersion:            r.Attributes.MinOsVersion,
		UsesNonExemptEncryption: r.Attributes.UsesNonExemptEncryption,
	}
	if r.Attributes.UploadedDate != nil {
		b.UploadedDate = *r.Attributes.UploadedDate
	}
	if r.Attributes.ExpirationDate != nil {
		b.ExpirationDate = *r.Attributes.ExpirationDate
	}
	if r.Attributes.Expired != nil {
		b.Expired = *r.Attributes.Expired
	}
	return b
}

// BuildFilter narrows ListBuilds. Empty fields are not filtered on.
type BuildFilter struct {
	AppID           string
	Platform        string // e.g. PlatformIOS
	Version         string // marketing version (CFBundleShortVersionString)
	BuildNumber     string // CFBundleVersion
	ProcessingState string
	// ExcludeExpired drops builds past their 90-day TestFlight life.
	ExcludeExpired bool
	// Limit caps the result to the newest N builds; 0 returns every match.
	Limit int
}

// ListBuilds lists builds, newest first.
func (c *Client) ListBuilds(ctx context.Context, f *BuildFilter) ([]Build, error) {
	q := url.Values{"sort": {"-uploadedDate"}}
	if f.AppID != "" {
		q.Set("filter[app]", f.AppID)
	}
	if f.Platform != "" {
		q.Set("filter[preReleaseVersion.platform]", f.Platform)
	}
	if f.Version != "" {
		q.Set("filter[preReleaseVersion.version]", f.Version)
	}
	if f.BuildNumber != "" {
		q.Set("filter[version]", f.BuildNumber)
	}
	if f.ProcessingState != "" {
		q.Set("filter[processingState]", f.ProcessingState)
	}
	if f.ExcludeExpired {
		q.Set("filter[expired]", "false")
	}
	var rs []Resource[buildAttributes]
	var err error
	if f.Limit > 0 {
		q.Set("limit", strconv.Itoa(f.Limit))
		rs, err = getPage[buildAttributes](ctx, c, "/v1/builds", q)
	} else {
		rs, err = getAll[buildAttributes](ctx, c, "/v1/builds", q)
	}
	if err != nil {
		return nil, err
	}
	builds := make([]Build, 0, len(rs))
	for _, r := range rs {
		builds = append(builds, toBuild(r))
	}
	return builds, nil
}

// GetBuild fetches one build.
func (c *Client) GetBuild(ctx context.Context, id string) (*Build, error) {
	r, err := getOne[buildAttributes](ctx, c, "/v1/builds/"+id, nil)
	if err != nil {
		return nil, err
	}
	b := toBuild(*r)
	return &b, nil
}

// SetUsesNonExemptEncryption answers the export compliance question for a build.
func (c *Client) SetUsesNonExemptEncryption(ctx context.Context, buildID string, uses bool) (*Build, error) {
	req := Resource[buildAttributes]{Type: "builds", ID: buildID, Attributes: buildAttributes{UsesNonExemptEncryption: &uses}}
	r, err := patch[buildAttributes, buildAttributes](ctx, c, "/v1/builds/"+buildID, req)
	if err != nil {
		return nil, err
	}
	b := toBuild(*r)
	return &b, nil
}

// AddBuildToBetaGroups makes the build available to the given TestFlight groups.
func (c *Client) AddBuildToBetaGroups(ctx context.Context, buildID string, groupIDs []string) error {
	return c.Post(ctx, "/v1/builds/"+buildID+"/relationships/betaGroups", ToMany("betaGroups", groupIDs), nil)
}
