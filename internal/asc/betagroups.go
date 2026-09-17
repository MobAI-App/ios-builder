package asc

import (
	"context"
	"net/url"
)

// BetaGroup is a TestFlight tester group.
type BetaGroup struct {
	ID                string
	Name              string
	Internal          bool
	PublicLinkEnabled bool
	// PublicLink is the invitation URL, set when the public link is enabled.
	PublicLink string
	// HasAccessToAllBuilds marks an internal group with automatic distribution:
	// every processed build reaches it, and adding one by hand is refused (422).
	HasAccessToAllBuilds bool
}

type betaGroupAttributes struct {
	Name                 string `json:"name,omitempty"`
	IsInternalGroup      *bool  `json:"isInternalGroup,omitempty"`
	PublicLinkEnabled    *bool  `json:"publicLinkEnabled,omitempty"`
	PublicLink           string `json:"publicLink,omitempty"`
	HasAccessToAllBuilds *bool  `json:"hasAccessToAllBuilds,omitempty"`
}

func toBetaGroup(r Resource[betaGroupAttributes]) BetaGroup {
	g := BetaGroup{ID: r.ID, Name: r.Attributes.Name, PublicLink: r.Attributes.PublicLink}
	if r.Attributes.IsInternalGroup != nil {
		g.Internal = *r.Attributes.IsInternalGroup
	}
	if r.Attributes.PublicLinkEnabled != nil {
		g.PublicLinkEnabled = *r.Attributes.PublicLinkEnabled
	}
	if r.Attributes.HasAccessToAllBuilds != nil {
		g.HasAccessToAllBuilds = *r.Attributes.HasAccessToAllBuilds
	}
	return g
}

// ListBetaGroups lists the app's TestFlight groups.
func (c *Client) ListBetaGroups(ctx context.Context, appID string) ([]BetaGroup, error) {
	rs, err := getAll[betaGroupAttributes](ctx, c, "/v1/betaGroups", url.Values{"filter[app]": {appID}})
	if err != nil {
		return nil, err
	}
	groups := make([]BetaGroup, 0, len(rs))
	for _, r := range rs {
		groups = append(groups, toBetaGroup(r))
	}
	return groups, nil
}

// BetaGroupSpec describes a TestFlight group to create.
type BetaGroupSpec struct {
	AppID    string
	Name     string
	Internal bool
	// PublicLinkEnabled turns on the public invitation link (external groups only).
	PublicLinkEnabled bool
	// HasAccessToAllBuilds gives an internal group every build automatically.
	HasAccessToAllBuilds bool
}

// CreateBetaGroup creates a TestFlight group for the app.
func (c *Client) CreateBetaGroup(ctx context.Context, spec BetaGroupSpec) (*BetaGroup, error) {
	attrs := betaGroupAttributes{Name: spec.Name, IsInternalGroup: &spec.Internal}
	if spec.PublicLinkEnabled {
		attrs.PublicLinkEnabled = &spec.PublicLinkEnabled
	}
	if spec.Internal {
		attrs.HasAccessToAllBuilds = &spec.HasAccessToAllBuilds
	}
	req := Resource[betaGroupAttributes]{
		Type:          "betaGroups",
		Attributes:    attrs,
		Relationships: Relationships{"app": ToOne("apps", spec.AppID)},
	}
	r, err := post[betaGroupAttributes, betaGroupAttributes](ctx, c, "/v1/betaGroups", req)
	if err != nil {
		return nil, err
	}
	g := toBetaGroup(*r)
	return &g, nil
}

// DeleteBetaGroup deletes a TestFlight group; its testers stay on the team.
func (c *Client) DeleteBetaGroup(ctx context.Context, groupID string) error {
	return c.Delete(ctx, "/v1/betaGroups/"+groupID, nil)
}

// AddBetaTestersToGroup puts existing testers into the group.
func (c *Client) AddBetaTestersToGroup(ctx context.Context, groupID string, testerIDs []string) error {
	return c.Post(ctx, "/v1/betaGroups/"+groupID+"/relationships/betaTesters", ToMany("betaTesters", testerIDs), nil)
}

// RemoveBetaTestersFromGroup takes testers out of the group without deleting them.
func (c *Client) RemoveBetaTestersFromGroup(ctx context.Context, groupID string, testerIDs []string) error {
	return c.Delete(ctx, "/v1/betaGroups/"+groupID+"/relationships/betaTesters", ToMany("betaTesters", testerIDs))
}
