package distribute

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/MobAI-App/ios-builder/internal/asc"
)

// TestFlightOptions configures SubmitTestFlight.
type TestFlightOptions struct {
	BundleID string
	// Version and BuildNumber narrow the build; empty picks the newest VALID build.
	Version     string
	BuildNumber string
	// Groups are TestFlight group names (case-insensitive); an unknown name is
	// created, internal or external with External. Empty adds the build
	// nowhere and reports the available groups instead.
	Groups   []string
	External bool
	// Notes is the "What to Test" text; Locale defaults to the app's primary locale.
	Notes  string
	Locale string
	// NoEncryption answers export compliance with "no" when still unanswered.
	NoEncryption bool
	// Wait follows the external beta review until it is decided.
	Wait         bool
	PollInterval time.Duration
	Log          io.Writer
}

// GroupRef describes a TestFlight group.
type GroupRef struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Internal bool   `json:"internal"`
	// Created is set when the group did not exist and was made for this submit.
	Created bool `json:"created,omitempty"`
	// AutoBuilds marks an internal group with automatic distribution; the
	// build was not added to it because every build already reaches it.
	AutoBuilds bool `json:"auto_builds,omitempty"`
}

// ReviewRef describes a review's state.
type ReviewRef struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

// TestFlightResult is what SubmitTestFlight reports.
type TestFlightResult struct {
	App             AppRef     `json:"app"`
	Build           BuildRef   `json:"build"`
	Compliance      string     `json:"encryption_compliance"`
	Notes           string     `json:"notes,omitempty"`
	Groups          []GroupRef `json:"groups"`
	AvailableGroups []GroupRef `json:"available_groups,omitempty"`
	// BetaReview is set when an external group required App Review.
	BetaReview *ReviewRef `json:"beta_review,omitempty"`
	Link       string     `json:"link"`
}

// SubmitTestFlight hands a processed build to TestFlight groups.
func SubmitTestFlight(ctx context.Context, client *asc.Client, opts *TestFlightOptions) (*TestFlightResult, error) {
	app, err := client.AppByBundleID(ctx, opts.BundleID)
	if err != nil {
		return nil, err
	}
	build, err := pickBuild(ctx, client, app.ID, opts.Version, opts.BuildNumber)
	if err != nil {
		return nil, err
	}
	res := &TestFlightResult{App: appRef(app), Build: buildRef(app.ID, opts.Version, build), Groups: []GroupRef{}, Link: buildLink(app.ID, build.ID)}
	logf(opts.Log, "Using build %s (%s, uploaded %s)", build.BuildNumber, build.ID, build.UploadedDate.Local().Format("2006-01-02 15:04"))

	res.Compliance, err = setCompliance(ctx, client, opts.Log, build, opts.NoEncryption)
	if err != nil {
		return res, err
	}
	res.Build.UsesNonExemptEncryption = build.UsesNonExemptEncryption

	if opts.Notes != "" {
		locale := opts.Locale
		if locale == "" {
			locale = app.PrimaryLocale
		}
		if locale == "" {
			locale = "en-US"
		}
		if _, err := client.SetWhatsNew(ctx, build.ID, locale, opts.Notes); err != nil {
			return res, fmt.Errorf("set test notes: %w", err)
		}
		res.Notes = opts.Notes
		logf(opts.Log, "Set What to Test (%s)", locale)
	}

	groups, err := client.ListBetaGroups(ctx, app.ID)
	if err != nil {
		return res, err
	}
	if len(opts.Groups) == 0 {
		for _, g := range groups {
			res.AvailableGroups = append(res.AvailableGroups, GroupRef{ID: g.ID, Name: g.Name, Internal: g.Internal})
		}
		logf(opts.Log, "No --group given; the build was added to no TestFlight group.")
		if len(groups) == 0 {
			logf(opts.Log, "Available groups: (none)")
		} else {
			logf(opts.Log, "Available groups:")
		}
		for _, g := range groups {
			logf(opts.Log, "  %s (%s)", g.Name, groupKind(g.Internal))
		}
		if res.Compliance == "pending" {
			logf(opts.Log, "Export compliance is unanswered (Missing Compliance); pass --no-encryption if the app uses no non-exempt encryption.")
		}
		return res, nil
	}

	var ids, names []string
	var external bool
	for _, name := range opts.Groups {
		g, err := findOrCreateGroup(ctx, client, opts.Log, app.ID, groups, name, !opts.External)
		if err != nil {
			return res, err
		}
		res.Groups = append(res.Groups, *g)
		if g.AutoBuilds {
			logf(opts.Log, "%s is an internal group with automatic distribution: every processed build is already available to its testers", g.Name)
			continue
		}
		ids = append(ids, g.ID)
		names = append(names, g.Name)
		external = external || !g.Internal
	}
	if res.Compliance == "pending" {
		return res, fmt.Errorf("build %s has no export compliance answer, so TestFlight cannot distribute it; pass --no-encryption if the app uses no non-exempt encryption, or answer in App Store Connect", build.BuildNumber)
	}
	if len(ids) == 0 {
		logf(opts.Log, "TestFlight: %s", res.Link)
		return res, nil
	}

	if external {
		review, err := client.GetBuildBetaAppReviewSubmission(ctx, build.ID)
		if err != nil {
			return res, err
		}
		if review == nil {
			logf(opts.Log, "Submitting build for external TestFlight review...")
			review, err = client.SubmitBuildForBetaReview(ctx, build.ID)
			if err != nil {
				return res, stateErrorHint(err, "submit for beta review")
			}
		} else {
			logf(opts.Log, "Beta review already %s", review.State)
		}
		res.BetaReview = &ReviewRef{ID: review.ID, State: review.State}
	}

	if err := client.AddBuildToBetaGroups(ctx, build.ID, ids); err != nil {
		return res, fmt.Errorf("add build to groups: %w", err)
	}
	logf(opts.Log, "Added build %s to %s", build.BuildNumber, strings.Join(names, ", "))

	if opts.Wait && res.BetaReview != nil {
		review, err := client.WaitForBetaAppReview(ctx, res.BetaReview.ID, pollInterval(opts.PollInterval), func(r *asc.BetaAppReviewSubmission) {
			if r.State != res.BetaReview.State {
				logf(opts.Log, "  beta review: %s", r.State)
			}
			res.BetaReview.State = r.State
		})
		if err != nil {
			return res, fmt.Errorf("wait for beta review: %w", err)
		}
		if review.State == asc.BetaReviewRejected {
			return res, fmt.Errorf("beta review rejected build %s; see the resolution center in App Store Connect", build.BuildNumber)
		}
	}
	logf(opts.Log, "TestFlight: %s", res.Link)
	return res, nil
}

func groupKind(internal bool) string {
	if internal {
		return "internal"
	}
	return "external"
}

// findOrCreateGroup matches name against the app's groups (case-insensitive)
// and creates it when none matches. Existing groups keep their type.
func findOrCreateGroup(ctx context.Context, client *asc.Client, log io.Writer, appID string, groups []asc.BetaGroup, name string, internal bool) (*GroupRef, error) {
	if g, err := asc.MatchBetaGroup(groups, name); err != nil {
		return nil, err
	} else if g != nil {
		return &GroupRef{ID: g.ID, Name: g.Name, Internal: g.Internal, AutoBuilds: g.Internal && g.HasAccessToAllBuilds}, nil
	}
	g, err := client.CreateBetaGroup(ctx, asc.BetaGroupSpec{AppID: appID, Name: name, Internal: internal})
	if err != nil {
		return nil, fmt.Errorf("create TestFlight group %s: %w", name, err)
	}
	logf(log, "Created TestFlight group %s (%s)", g.Name, groupKind(g.Internal))
	return &GroupRef{ID: g.ID, Name: g.Name, Internal: g.Internal, Created: true}, nil
}
