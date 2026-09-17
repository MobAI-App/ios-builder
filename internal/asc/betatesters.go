package asc

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

// Beta tester states.
const (
	BetaTesterNotInvited = "NOT_INVITED"
	BetaTesterInvited    = "INVITED"
	BetaTesterAccepted   = "ACCEPTED"
	BetaTesterInstalled  = "INSTALLED"
	BetaTesterRevoked    = "REVOKED"
)

// BetaTester is a TestFlight tester.
type BetaTester struct {
	ID         string
	Email      string
	FirstName  string
	LastName   string
	InviteType string // EMAIL or PUBLIC_LINK
	State      string
}

type betaTesterAttributes struct {
	FirstName  string `json:"firstName,omitempty"`
	LastName   string `json:"lastName,omitempty"`
	Email      string `json:"email,omitempty"`
	InviteType string `json:"inviteType,omitempty"`
	State      string `json:"state,omitempty"`
}

func toBetaTester(r Resource[betaTesterAttributes]) BetaTester {
	a := r.Attributes
	return BetaTester{ID: r.ID, Email: a.Email, FirstName: a.FirstName, LastName: a.LastName, InviteType: a.InviteType, State: a.State}
}

// BetaTesterFilter narrows ListBetaTesters. Empty fields are not filtered on.
type BetaTesterFilter struct {
	AppID   string
	GroupID string
	Email   string
}

// ListBetaTesters lists testers by email.
func (c *Client) ListBetaTesters(ctx context.Context, f *BetaTesterFilter) ([]BetaTester, error) {
	q := url.Values{"sort": {"email"}}
	if f.AppID != "" {
		q.Set("filter[apps]", f.AppID)
	}
	if f.GroupID != "" {
		q.Set("filter[betaGroups]", f.GroupID)
	}
	if f.Email != "" {
		q.Set("filter[email]", f.Email)
	}
	rs, err := getAll[betaTesterAttributes](ctx, c, "/v1/betaTesters", q)
	if err != nil {
		return nil, err
	}
	testers := make([]BetaTester, 0, len(rs))
	for _, r := range rs {
		testers = append(testers, toBetaTester(r))
	}
	return testers, nil
}

// FindBetaTester returns the tester with that email, or nil. The filter is a
// substring match on Apple's side, so the address is compared exactly here.
func (c *Client) FindBetaTester(ctx context.Context, f *BetaTesterFilter) (*BetaTester, error) {
	testers, err := c.ListBetaTesters(ctx, f)
	if err != nil {
		return nil, err
	}
	for _, t := range testers {
		if strings.EqualFold(t.Email, f.Email) {
			return &t, nil
		}
	}
	return nil, nil
}

// BetaTesterSpec describes a tester to invite.
type BetaTesterSpec struct {
	Email     string
	FirstName string
	LastName  string
	// GroupIDs are the TestFlight groups the tester joins; joining sends the invitation.
	GroupIDs []string
}

// CreateBetaTester creates a tester in the given groups.
func (c *Client) CreateBetaTester(ctx context.Context, spec BetaTesterSpec) (*BetaTester, error) {
	req := Resource[betaTesterAttributes]{
		Type:       "betaTesters",
		Attributes: betaTesterAttributes{Email: spec.Email, FirstName: spec.FirstName, LastName: spec.LastName},
	}
	if len(spec.GroupIDs) > 0 {
		req.Relationships = Relationships{"betaGroups": ToMany("betaGroups", spec.GroupIDs)}
	}
	r, err := post[betaTesterAttributes, betaTesterAttributes](ctx, c, "/v1/betaTesters", req)
	if err != nil {
		return nil, err
	}
	t := toBetaTester(*r)
	return &t, nil
}

// AddBetaTester creates the tester in the groups, or, when the team already
// has a tester with that email (App Store Connect answers 409), adds the
// existing one to them. created reports which happened.
func (c *Client) AddBetaTester(ctx context.Context, spec BetaTesterSpec) (tester *BetaTester, created bool, err error) {
	tester, err = c.CreateBetaTester(ctx, spec)
	if err == nil {
		return tester, true, nil
	}
	if !IsStatus(err, http.StatusConflict) {
		return nil, false, err
	}
	existing, findErr := c.FindBetaTester(ctx, &BetaTesterFilter{Email: spec.Email})
	if findErr != nil {
		return nil, false, findErr
	}
	if existing == nil {
		return nil, false, err
	}
	for _, id := range spec.GroupIDs {
		if err := c.AddBetaTestersToGroup(ctx, id, []string{existing.ID}); err != nil {
			return nil, false, err
		}
	}
	return existing, false, nil
}

// DeleteBetaTester removes the tester from TestFlight for the whole team.
func (c *Client) DeleteBetaTester(ctx context.Context, testerID string) error {
	return c.Delete(ctx, "/v1/betaTesters/"+testerID, nil)
}
