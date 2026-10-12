package asc

import (
	"context"
	"encoding/json"
	"net/url"
)

// Attributes of appStoreVersionLocalizations.
const (
	AttrDescription     = "description"
	AttrKeywords        = "keywords"
	AttrWhatsNew        = "whatsNew"
	AttrPromotionalText = "promotionalText"
	AttrMarketingURL    = "marketingUrl"
	AttrSupportURL      = "supportUrl"
)

// Attributes of appInfoLocalizations.
const (
	AttrName             = "name"
	AttrSubtitle         = "subtitle"
	AttrPrivacyPolicyURL = "privacyPolicyUrl"
)

// Localization is one locale of an App Store version (appStoreVersionLocalizations)
// or of the app info (appInfoLocalizations).
type Localization struct {
	ID     string
	Locale string
	// Fields maps attribute names (AttrDescription, AttrName, ...) to their
	// values; attributes that are null in App Store Connect are absent.
	Fields map[string]string
}

func toLocalization(r Resource[map[string]any]) Localization {
	l := Localization{ID: r.ID, Fields: map[string]string{}}
	for k, v := range r.Attributes {
		s, ok := v.(string)
		switch {
		case !ok:
		case k == "locale":
			l.Locale = s
		default:
			l.Fields[k] = s
		}
	}
	return l
}

func listLocalizations(ctx context.Context, c *Client, path string) ([]Localization, error) {
	rs, err := getAll[map[string]any](ctx, c, path, nil)
	if err != nil {
		return nil, err
	}
	locs := make([]Localization, 0, len(rs))
	for _, r := range rs {
		locs = append(locs, toLocalization(r))
	}
	return locs, nil
}

func createLocalization(ctx context.Context, c *Client, resourceType, parentRel, parentType, parentID, locale string, fields map[string]string) (*Localization, error) {
	attrs := map[string]any{"locale": locale}
	for k, v := range fields {
		attrs[k] = v
	}
	req := Resource[map[string]any]{Type: resourceType, Attributes: attrs, Relationships: Relationships{parentRel: ToOne(parentType, parentID)}}
	r, err := post[map[string]any, map[string]any](ctx, c, "/v1/"+resourceType, req)
	if err != nil {
		return nil, err
	}
	l := toLocalization(*r)
	return &l, nil
}

func updateLocalization(ctx context.Context, c *Client, resourceType, id string, fields map[string]string) error {
	attrs := make(map[string]any, len(fields))
	for k, v := range fields {
		attrs[k] = v
	}
	req := Resource[map[string]any]{Type: resourceType, ID: id, Attributes: attrs}
	return c.Patch(ctx, "/v1/"+resourceType+"/"+id, Document[Resource[map[string]any]]{Data: req}, nil)
}

// ListVersionLocalizations lists the locales of an App Store version.
func (c *Client) ListVersionLocalizations(ctx context.Context, versionID string) ([]Localization, error) {
	return listLocalizations(ctx, c, "/v1/appStoreVersions/"+versionID+"/appStoreVersionLocalizations")
}

// CreateVersionLocalization adds a locale to an App Store version.
func (c *Client) CreateVersionLocalization(ctx context.Context, versionID, locale string, fields map[string]string) (*Localization, error) {
	return createLocalization(ctx, c, "appStoreVersionLocalizations", "appStoreVersion", "appStoreVersions", versionID, locale, fields)
}

// UpdateVersionLocalization sets the given attributes of a version locale.
func (c *Client) UpdateVersionLocalization(ctx context.Context, id string, fields map[string]string) error {
	return updateLocalization(ctx, c, "appStoreVersionLocalizations", id, fields)
}

// ListAppInfoLocalizations lists the locales of an app info record.
func (c *Client) ListAppInfoLocalizations(ctx context.Context, appInfoID string) ([]Localization, error) {
	return listLocalizations(ctx, c, "/v1/appInfos/"+appInfoID+"/appInfoLocalizations")
}

// CreateAppInfoLocalization adds a locale to an app info record; App Store
// Connect requires AttrName.
func (c *Client) CreateAppInfoLocalization(ctx context.Context, appInfoID, locale string, fields map[string]string) (*Localization, error) {
	return createLocalization(ctx, c, "appInfoLocalizations", "appInfo", "appInfos", appInfoID, locale, fields)
}

// UpdateAppInfoLocalization sets the given attributes of an app info locale.
func (c *Client) UpdateAppInfoLocalization(ctx context.Context, id string, fields map[string]string) error {
	return updateLocalization(ctx, c, "appInfoLocalizations", id, fields)
}

// App info states (AppInfo.State) a new version's metadata can be edited in.
const (
	AppInfoStatePrepareForSubmission = "PREPARE_FOR_SUBMISSION"
	AppInfoStateDeveloperRejected    = "DEVELOPER_REJECTED"
	AppInfoStateRejected             = "REJECTED"
)

// AppInfo is the version-independent part of an app's store listing: name,
// subtitle, privacy policy and categories. An app has one live record and,
// while a version is being prepared, an editable one.
type AppInfo struct {
	ID                  string
	State               string
	PrimaryCategoryID   string
	SecondaryCategoryID string
}

// Editable reports whether the record accepts changes.
func (a *AppInfo) Editable() bool {
	switch a.State {
	case AppInfoStatePrepareForSubmission, AppInfoStateDeveloperRejected, AppInfoStateRejected:
		return true
	}
	return false
}

type appInfoAttributes struct {
	State         string `json:"state,omitempty"`
	AppStoreState string `json:"appStoreState,omitempty"`
}

// ListAppInfos lists the app's info records with their categories.
func (c *Client) ListAppInfos(ctx context.Context, appID string) ([]AppInfo, error) {
	rs, err := getAll[appInfoAttributes](ctx, c, "/v1/apps/"+appID+"/appInfos", url.Values{"include": {"primaryCategory,secondaryCategory"}})
	if err != nil {
		return nil, err
	}
	infos := make([]AppInfo, 0, len(rs))
	for _, r := range rs {
		info := AppInfo{ID: r.ID, State: r.Attributes.State}
		if info.State == "" {
			info.State = r.Attributes.AppStoreState
		}
		if l, ok := r.Relationships.One("primaryCategory"); ok {
			info.PrimaryCategoryID = l.ID
		}
		if l, ok := r.Relationships.One("secondaryCategory"); ok {
			info.SecondaryCategoryID = l.ID
		}
		infos = append(infos, info)
	}
	return infos, nil
}

// UpdateAppInfoCategories sets the categories that are non-nil; an empty
// string clears the category.
func (c *Client) UpdateAppInfoCategories(ctx context.Context, appInfoID string, primary, secondary *string) error {
	rels := Relationships{}
	for name, id := range map[string]*string{"primaryCategory": primary, "secondaryCategory": secondary} {
		switch {
		case id == nil:
		case *id == "":
			rels[name] = Relationship{Data: json.RawMessage("null")}
		default:
			rels[name] = ToOne("appCategories", *id)
		}
	}
	req := Resource[struct{}]{Type: "appInfos", ID: appInfoID, Relationships: rels}
	return c.Patch(ctx, "/v1/appInfos/"+appInfoID, Document[Resource[struct{}]]{Data: req}, nil)
}
