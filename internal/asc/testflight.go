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
}

type betaGroupAttributes struct {
	Name                 string `json:"name,omitempty"`
	IsInternalGroup      *bool  `json:"isInternalGroup,omitempty"`
	PublicLinkEnabled    *bool  `json:"publicLinkEnabled,omitempty"`
	HasAccessToAllBuilds *bool  `json:"hasAccessToAllBuilds,omitempty"`
}

// ListBetaGroups lists the app's TestFlight groups.
func (c *Client) ListBetaGroups(ctx context.Context, appID string) ([]BetaGroup, error) {
	rs, err := getAll[betaGroupAttributes](ctx, c, "/v1/betaGroups", url.Values{"filter[app]": {appID}})
	if err != nil {
		return nil, err
	}
	groups := make([]BetaGroup, 0, len(rs))
	for _, r := range rs {
		g := BetaGroup{ID: r.ID, Name: r.Attributes.Name}
		if r.Attributes.IsInternalGroup != nil {
			g.Internal = *r.Attributes.IsInternalGroup
		}
		if r.Attributes.PublicLinkEnabled != nil {
			g.PublicLinkEnabled = *r.Attributes.PublicLinkEnabled
		}
		groups = append(groups, g)
	}
	return groups, nil
}

// BetaBuildLocalization is the "What to Test" text of a build in one locale.
type BetaBuildLocalization struct {
	ID       string
	Locale   string
	WhatsNew string
}

type betaBuildLocalizationAttributes struct {
	WhatsNew string `json:"whatsNew,omitempty"`
	Locale   string `json:"locale,omitempty"`
}

func toBetaBuildLocalization(r Resource[betaBuildLocalizationAttributes]) BetaBuildLocalization {
	return BetaBuildLocalization{ID: r.ID, Locale: r.Attributes.Locale, WhatsNew: r.Attributes.WhatsNew}
}

// ListBetaBuildLocalizations lists the build's test notes per locale.
func (c *Client) ListBetaBuildLocalizations(ctx context.Context, buildID string) ([]BetaBuildLocalization, error) {
	rs, err := getAll[betaBuildLocalizationAttributes](ctx, c, "/v1/builds/"+buildID+"/betaBuildLocalizations", nil)
	if err != nil {
		return nil, err
	}
	out := make([]BetaBuildLocalization, 0, len(rs))
	for _, r := range rs {
		out = append(out, toBetaBuildLocalization(r))
	}
	return out, nil
}

// CreateBetaBuildLocalization adds test notes for a locale.
func (c *Client) CreateBetaBuildLocalization(ctx context.Context, buildID, locale, whatsNew string) (*BetaBuildLocalization, error) {
	req := Resource[betaBuildLocalizationAttributes]{
		Type:          "betaBuildLocalizations",
		Attributes:    betaBuildLocalizationAttributes{Locale: locale, WhatsNew: whatsNew},
		Relationships: Relationships{"build": ToOne("builds", buildID)},
	}
	r, err := post[betaBuildLocalizationAttributes, betaBuildLocalizationAttributes](ctx, c, "/v1/betaBuildLocalizations", req)
	if err != nil {
		return nil, err
	}
	l := toBetaBuildLocalization(*r)
	return &l, nil
}

// UpdateBetaBuildLocalization replaces the test notes of an existing locale.
func (c *Client) UpdateBetaBuildLocalization(ctx context.Context, id, whatsNew string) (*BetaBuildLocalization, error) {
	req := Resource[betaBuildLocalizationAttributes]{Type: "betaBuildLocalizations", ID: id, Attributes: betaBuildLocalizationAttributes{WhatsNew: whatsNew}}
	r, err := patch[betaBuildLocalizationAttributes, betaBuildLocalizationAttributes](ctx, c, "/v1/betaBuildLocalizations/"+id, req)
	if err != nil {
		return nil, err
	}
	l := toBetaBuildLocalization(*r)
	return &l, nil
}

// SetWhatsNew creates or updates the build's test notes for the locale.
func (c *Client) SetWhatsNew(ctx context.Context, buildID, locale, whatsNew string) (*BetaBuildLocalization, error) {
	existing, err := c.ListBetaBuildLocalizations(ctx, buildID)
	if err != nil {
		return nil, err
	}
	for _, l := range existing {
		if l.Locale == locale {
			return c.UpdateBetaBuildLocalization(ctx, l.ID, whatsNew)
		}
	}
	return c.CreateBetaBuildLocalization(ctx, buildID, locale, whatsNew)
}

// Beta review states.
const (
	BetaReviewWaiting  = "WAITING_FOR_REVIEW"
	BetaReviewInReview = "IN_REVIEW"
	BetaReviewRejected = "REJECTED"
	BetaReviewApproved = "APPROVED"
)

// BetaAppReviewSubmission is a build's external TestFlight review.
type BetaAppReviewSubmission struct {
	ID    string
	State string
}

type betaAppReviewSubmissionAttributes struct {
	BetaReviewState string `json:"betaReviewState,omitempty"`
}

// GetBuildBetaAppReviewSubmission returns the build's beta review, or nil when
// the build was never submitted.
func (c *Client) GetBuildBetaAppReviewSubmission(ctx context.Context, buildID string) (*BetaAppReviewSubmission, error) {
	r, err := getOne[betaAppReviewSubmissionAttributes](ctx, c, "/v1/builds/"+buildID+"/betaAppReviewSubmission", nil)
	if err != nil {
		if IsStatus(err, 404) {
			return nil, nil
		}
		return nil, err
	}
	if r.ID == "" {
		return nil, nil
	}
	return &BetaAppReviewSubmission{ID: r.ID, State: r.Attributes.BetaReviewState}, nil
}

// GetBetaAppReviewSubmission fetches a beta review by ID.
func (c *Client) GetBetaAppReviewSubmission(ctx context.Context, id string) (*BetaAppReviewSubmission, error) {
	r, err := getOne[betaAppReviewSubmissionAttributes](ctx, c, "/v1/betaAppReviewSubmissions/"+id, nil)
	if err != nil {
		return nil, err
	}
	return &BetaAppReviewSubmission{ID: r.ID, State: r.Attributes.BetaReviewState}, nil
}

// SubmitBuildForBetaReview submits the build for external TestFlight review.
func (c *Client) SubmitBuildForBetaReview(ctx context.Context, buildID string) (*BetaAppReviewSubmission, error) {
	req := Resource[struct{}]{Type: "betaAppReviewSubmissions", Relationships: Relationships{"build": ToOne("builds", buildID)}}
	r, err := post[struct{}, betaAppReviewSubmissionAttributes](ctx, c, "/v1/betaAppReviewSubmissions", req)
	if err != nil {
		return nil, err
	}
	return &BetaAppReviewSubmission{ID: r.ID, State: r.Attributes.BetaReviewState}, nil
}
