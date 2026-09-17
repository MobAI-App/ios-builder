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
}

// AddTester puts a person into a TestFlight group. External groups take
// anyone: a tester record is created (invitation sent) or the existing one
// added. Internal groups take team members only: a member's tester record is
// added to the group, and a stranger is invited to the team first; the build
// shows up once they accept and AddTester runs again.
func AddTester(ctx context.Context, client *asc.Client, opts *TesterOptions) (*TesterResult, error) {
	g := opts.Group
	res := &TesterResult{Email: opts.Email, Group: g.Name}
	if !g.Internal {
		tester, created, err := client.AddBetaTester(ctx, asc.BetaTesterSpec{Email: opts.Email, FirstName: opts.FirstName, LastName: opts.LastName, GroupIDs: []string{g.ID}})
		if err != nil {
			return nil, err
		}
		return testerOutcome(opts.Log, res, tester, created), nil
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
		return testerOutcome(opts.Log, res, tester, true), nil
	}
	if err := client.AddBetaTestersToGroup(ctx, g.ID, []string{tester.ID}); err != nil {
		return nil, err
	}
	return testerOutcome(opts.Log, res, tester, false), nil
}

func testerOutcome(log io.Writer, res *TesterResult, tester *asc.BetaTester, created bool) *TesterResult {
	res.ID, res.Email = tester.ID, tester.Email
	if created {
		res.Status = TesterInvited
		logf(log, "Invited %s to %s", tester.Email, res.Group)
	} else {
		res.Status = TesterAdded
		logf(log, "Added existing tester %s to %s", tester.Email, res.Group)
	}
	return res
}

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
