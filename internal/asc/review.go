package asc

import (
	"context"
	"net/url"
	"strings"
	"time"
)

// Review submission states.
const (
	ReviewStateReadyForReview   = "READY_FOR_REVIEW"
	ReviewStateWaitingForReview = "WAITING_FOR_REVIEW"
	ReviewStateInReview         = "IN_REVIEW"
	ReviewStateUnresolvedIssues = "UNRESOLVED_ISSUES"
	ReviewStateCanceling        = "CANCELING"
	ReviewStateCompleting       = "COMPLETING"
	ReviewStateComplete         = "COMPLETE"
)

// ReviewSubmission groups the items submitted to App Review together.
type ReviewSubmission struct {
	ID            string
	Platform      string
	State         string
	SubmittedDate time.Time
}

type reviewSubmissionAttributes struct {
	Platform      string     `json:"platform,omitempty"`
	State         string     `json:"state,omitempty"`
	SubmittedDate *time.Time `json:"submittedDate,omitempty"`
}

type reviewSubmissionUpdate struct {
	Submitted *bool `json:"submitted,omitempty"`
	Canceled  *bool `json:"canceled,omitempty"`
}

func toReviewSubmission(r Resource[reviewSubmissionAttributes]) ReviewSubmission {
	s := ReviewSubmission{ID: r.ID, Platform: r.Attributes.Platform, State: r.Attributes.State}
	if r.Attributes.SubmittedDate != nil {
		s.SubmittedDate = *r.Attributes.SubmittedDate
	}
	return s
}

// ListReviewSubmissions lists the app's submissions on a platform, optionally limited to states.
func (c *Client) ListReviewSubmissions(ctx context.Context, appID, platform string, states []string) ([]ReviewSubmission, error) {
	q := url.Values{"filter[app]": {appID}}
	if platform != "" {
		q.Set("filter[platform]", platform)
	}
	if len(states) > 0 {
		q.Set("filter[state]", strings.Join(states, ","))
	}
	rs, err := getAll[reviewSubmissionAttributes](ctx, c, "/v1/reviewSubmissions", q)
	if err != nil {
		return nil, err
	}
	subs := make([]ReviewSubmission, 0, len(rs))
	for _, r := range rs {
		subs = append(subs, toReviewSubmission(r))
	}
	return subs, nil
}

// CreateReviewSubmission opens a new submission for the app on a platform.
func (c *Client) CreateReviewSubmission(ctx context.Context, appID, platform string) (*ReviewSubmission, error) {
	req := Resource[reviewSubmissionAttributes]{
		Type:          "reviewSubmissions",
		Attributes:    reviewSubmissionAttributes{Platform: platform},
		Relationships: Relationships{"app": ToOne("apps", appID)},
	}
	r, err := post[reviewSubmissionAttributes, reviewSubmissionAttributes](ctx, c, "/v1/reviewSubmissions", req)
	if err != nil {
		return nil, err
	}
	s := toReviewSubmission(*r)
	return &s, nil
}

// ReviewSubmissionItem is one thing under review, here always an App Store version.
type ReviewSubmissionItem struct {
	ID                string
	State             string
	AppStoreVersionID string
}

type reviewSubmissionItemAttributes struct {
	State string `json:"state,omitempty"`
}

func toReviewSubmissionItem(r Resource[reviewSubmissionItemAttributes]) ReviewSubmissionItem {
	item := ReviewSubmissionItem{ID: r.ID, State: r.Attributes.State}
	if l, ok := r.Relationships.One("appStoreVersion"); ok {
		item.AppStoreVersionID = l.ID
	}
	return item
}

// ListReviewSubmissionItems lists what a submission contains.
func (c *Client) ListReviewSubmissionItems(ctx context.Context, submissionID string) ([]ReviewSubmissionItem, error) {
	rs, err := getAll[reviewSubmissionItemAttributes](ctx, c, "/v1/reviewSubmissions/"+submissionID+"/items", url.Values{"include": {"appStoreVersion"}})
	if err != nil {
		return nil, err
	}
	items := make([]ReviewSubmissionItem, 0, len(rs))
	for _, r := range rs {
		items = append(items, toReviewSubmissionItem(r))
	}
	return items, nil
}

// AddAppStoreVersionToReviewSubmission puts a version into the submission.
func (c *Client) AddAppStoreVersionToReviewSubmission(ctx context.Context, submissionID, versionID string) (*ReviewSubmissionItem, error) {
	req := Resource[struct{}]{
		Type: "reviewSubmissionItems",
		Relationships: Relationships{
			"reviewSubmission": ToOne("reviewSubmissions", submissionID),
			"appStoreVersion":  ToOne("appStoreVersions", versionID),
		},
	}
	r, err := post[struct{}, reviewSubmissionItemAttributes](ctx, c, "/v1/reviewSubmissionItems", req)
	if err != nil {
		return nil, err
	}
	item := toReviewSubmissionItem(*r)
	if item.AppStoreVersionID == "" {
		item.AppStoreVersionID = versionID
	}
	return &item, nil
}

// SubmitReviewSubmission sends the submission to App Review.
func (c *Client) SubmitReviewSubmission(ctx context.Context, id string) (*ReviewSubmission, error) {
	submitted := true
	req := Resource[reviewSubmissionUpdate]{Type: "reviewSubmissions", ID: id, Attributes: reviewSubmissionUpdate{Submitted: &submitted}}
	r, err := patch[reviewSubmissionUpdate, reviewSubmissionAttributes](ctx, c, "/v1/reviewSubmissions/"+id, req)
	if err != nil {
		return nil, err
	}
	s := toReviewSubmission(*r)
	return &s, nil
}
