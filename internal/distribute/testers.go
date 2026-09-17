package distribute

import (
	"context"
	"fmt"
	"io"

	"github.com/MobAI-App/ios-builder/internal/asc"
)

// Tester statuses reported by AddTester.
const (
	// TesterInvited: a tester record was created in the group, which sends the TestFlight invitation.
	TesterInvited = "invited"
	// TesterAdded: the existing tester record was added to the group.
	TesterAdded = "added"
	// TesterTeamInviteSent: the person is not on the team; a team invitation was sent.
	TesterTeamInviteSent = "team_invite_sent"
	// TesterTeamInvitePending: the person already has an unaccepted team invitation.
	TesterTeamInvitePending = "team_invite_pending"
)

// TesterOptions describes one person to put into a TestFlight group.
type TesterOptions struct {
	AppID     string
	Group     asc.BetaGroup
	Email     string
	FirstName string
	LastName  string
	// TeamRole is the role a person not yet on the team is invited with;
	// internal groups only take team members. Empty means CUSTOMER_SUPPORT.
	TeamRole string
	Log      io.Writer
}

// TesterResult is what AddTester reports for one person.
type TesterResult struct {
	ID     string `json:"id,omitempty"`
	Email  string `json:"email"`
	Group  string `json:"group"`
	Status string `json:"status"`
	// State is the tester record's TestFlight state afterwards (INVITED,
	// ACCEPTED, ...); empty when only a team invitation went out.
	State string `json:"state,omitempty"`
}

// AddTester puts a person into a TestFlight group, creating or reusing their
// tester record. Internal groups take team members only, so a stranger is
// invited to the team first and reaches the build once they accept and
// AddTester runs again.
func AddTester(ctx context.Context, client *asc.Client, opts *TesterOptions) (*TesterResult, error) {
	g := opts.Group
	res := &TesterResult{Email: opts.Email, Group: g.Name}
	if !g.Internal {
		tester, created, err := client.AddBetaTester(ctx, asc.BetaTesterSpec{Email: opts.Email, FirstName: opts.FirstName, LastName: opts.LastName, GroupIDs: []string{g.ID}})
		if err != nil {
			return nil, err
		}
		return finishTester(ctx, client, opts, res, tester, created)
	}

	user, err := client.FindUser(ctx, opts.Email)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return inviteToTeam(ctx, client, opts, res)
	}
	tester, err := client.FindBetaTester(ctx, &asc.BetaTesterFilter{Email: opts.Email})
	if err != nil {
		return nil, err
	}
	if tester == nil {
		tester, _, err = client.AddBetaTester(ctx, asc.BetaTesterSpec{Email: opts.Email, FirstName: user.FirstName, LastName: user.LastName, GroupIDs: []string{g.ID}})
		if err != nil {
			return nil, fmt.Errorf("%s is on the team but has no TestFlight tester record and App Store Connect refused to create one: %w; enable TestFlight for them under Users and Access", opts.Email, err)
		}
		return finishTester(ctx, client, opts, res, tester, true)
	}
	if err := client.AddBetaTestersToGroup(ctx, g.ID, []string{tester.ID}); err != nil {
		return nil, err
	}
	return finishTester(ctx, client, opts, res, tester, false)
}

// finishTester records the outcome and makes sure an email went out: a
// created record is INVITED by App Store Connect itself, but a record added
// to an internal group stays NOT_INVITED until an invitation is sent.
func finishTester(ctx context.Context, client *asc.Client, opts *TesterOptions, res *TesterResult, tester *asc.BetaTester, created bool) (*TesterResult, error) {
	res.Status = TesterInvited
	if !created {
		res.Status = TesterAdded
		logf(opts.Log, "Added existing tester %s to %s", tester.Email, res.Group)
		// The group add itself may have moved the state; read it back.
		updated, err := client.GetBetaTester(ctx, tester.ID)
		if err != nil {
			return nil, err
		}
		tester = updated
	}
	if tester.State != asc.BetaTesterNotInvited {
		if created {
			logf(opts.Log, "Invited %s to %s", tester.Email, res.Group)
		}
	} else if invited, err := InviteTester(ctx, client, opts.Log, opts.AppID, tester); asc.HasCode(err, asc.CodeNoInstallableBuilds) {
		// A group without a build: App Store Connect keeps the record
		// NOT_INVITED and sends the email itself once a build is added.
		res.Status = TesterAdded
		logf(opts.Log, "Added %s to %s (invite goes out once the group has a build)", tester.Email, res.Group)
	} else if err != nil {
		return nil, err
	} else {
		tester = invited
	}
	res.ID, res.Email, res.State = tester.ID, tester.Email, tester.State
	return res, nil
}

// InviteTester sends, or resends, the app's TestFlight invitation to a tester
// and returns the record with its new state.
func InviteTester(ctx context.Context, client *asc.Client, log io.Writer, appID string, tester *asc.BetaTester) (*asc.BetaTester, error) {
	if err := client.InviteBetaTester(ctx, appID, tester.ID); err != nil {
		if asc.HasCode(err, asc.CodeNoInstallableBuilds) {
			return nil, &noBuildError{email: tester.Email, err: err}
		}
		return nil, fmt.Errorf("invite %s: %w", tester.Email, err)
	}
	updated, err := client.GetBetaTester(ctx, tester.ID)
	if err != nil {
		return nil, err
	}
	logf(log, "Sent TestFlight invitation to %s (%s)", updated.Email, updated.State)
	return updated, nil
}

// noBuildError is App Store Connect's refusal to invite a tester whose groups
// have no build, said in terms of what to do; the ASC error stays unwrappable.
type noBuildError struct {
	email string
	err   error
}

func (e *noBuildError) Error() string {
	return fmt.Sprintf("%s has no installable build yet: add one to the group first (builder asc groups add-build <group>); external groups also need the build to pass Beta App Review", e.email)
}

func (e *noBuildError) Unwrap() error { return e.err }

func inviteToTeam(ctx context.Context, client *asc.Client, opts *TesterOptions, res *TesterResult) (*TesterResult, error) {
	if inv, err := client.FindUserInvitation(ctx, opts.Email); err != nil {
		return nil, err
	} else if inv != nil {
		res.ID, res.Status = inv.ID, TesterTeamInvitePending
		logf(opts.Log, "%s already has a pending team invitation; once they accept the email, rerun to add them to %s", opts.Email, res.Group)
		return res, nil
	}
	if opts.FirstName == "" || opts.LastName == "" {
		return nil, fmt.Errorf("%s is not on the App Store Connect team, which an internal group requires; pass --first and --last to invite them", opts.Email)
	}
	role := opts.TeamRole
	if role == "" {
		role = asc.RoleCustomerSupport
	}
	inv, err := client.InviteUser(ctx, &asc.UserInvitationSpec{Email: opts.Email, FirstName: opts.FirstName, LastName: opts.LastName, Roles: []string{role}, VisibleAppIDs: []string{opts.AppID}})
	if err != nil {
		return nil, fmt.Errorf("invite %s to the team: %w", opts.Email, err)
	}
	res.ID, res.Status = inv.ID, TesterTeamInviteSent
	logf(opts.Log, "Invited %s to the team as %s; they must accept the email, then rerun to add them to %s", opts.Email, role, res.Group)
	return res, nil
}
