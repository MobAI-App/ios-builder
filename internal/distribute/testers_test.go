package distribute

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/MobAI-App/ios-builder/internal/asc"
)

var (
	externalGroup = asc.BetaGroup{ID: "g-ext", Name: "Beta Testers"}
	internalGroup = asc.BetaGroup{ID: "g-int", Name: "Team", Internal: true}
)

func TestAddTesterExternalGroup(t *testing.T) {
	f := newFake(t)
	f.testers["old@example.com"] = "t-old"
	var log bytes.Buffer
	c := f.client(t)

	res, err := AddTester(context.Background(), c, &TesterOptions{AppID: "app-1", Group: externalGroup, Email: "new@example.com", FirstName: "New", LastName: "One", Log: &log})
	if err != nil || res.Status != TesterInvited || res.ID == "" || res.Group != "Beta Testers" {
		t.Fatalf("result = %+v, err = %v", res, err)
	}
	body := obj(t, f.body("POST /v1/betaTesters"), "data")
	if obj(t, body, "attributes")["firstName"] != "New" || obj(t, arr(t, body, "relationships", "betaGroups", "data")[0])["id"] != "g-ext" {
		t.Errorf("create body = %v", body)
	}

	// The team already has the address: 409, then the existing record joins the group.
	res, err = AddTester(context.Background(), c, &TesterOptions{AppID: "app-1", Group: externalGroup, Email: "old@example.com", Log: &log})
	if err != nil || res.Status != TesterAdded || res.ID != "t-old" {
		t.Fatalf("result = %+v, err = %v", res, err)
	}
	if links := arr(t, f.body("POST /v1/betaGroups/g-ext/relationships/betaTesters"), "data"); len(links) != 1 || obj(t, links[0])["id"] != "t-old" {
		t.Errorf("linkage = %v", links)
	}
	if f.called("GET /v1/users") || f.called("POST /v1/userInvitations") {
		t.Errorf("external groups never touch the team: %v", f.calls)
	}
	if !strings.Contains(log.String(), "Invited new@example.com to Beta Testers") || !strings.Contains(log.String(), "Added existing tester old@example.com to Beta Testers") {
		t.Errorf("log = %q", log.String())
	}
}

func TestAddTesterInternalGroupMember(t *testing.T) {
	f := newFake(t)
	f.users["dev@example.com"] = true
	f.testers["dev@example.com"] = "t-dev"
	res, err := AddTester(context.Background(), f.client(t), &TesterOptions{AppID: "app-1", Group: internalGroup, Email: "dev@example.com"})
	if err != nil || res.Status != TesterAdded || res.ID != "t-dev" {
		t.Fatalf("result = %+v, err = %v", res, err)
	}
	if links := arr(t, f.body("POST /v1/betaGroups/g-int/relationships/betaTesters"), "data"); len(links) != 1 || obj(t, links[0])["id"] != "t-dev" {
		t.Errorf("linkage = %v", links)
	}
	if f.called("POST /v1/betaTesters") || f.called("POST /v1/userInvitations") || f.called("GET /v1/userInvitations") {
		t.Errorf("a member with a tester record needs neither a new record nor an invitation: %v", f.calls)
	}

	// A member without a tester record gets one created in the group.
	f = newFake(t)
	f.users["fresh@example.com"] = true
	res, err = AddTester(context.Background(), f.client(t), &TesterOptions{AppID: "app-1", Group: internalGroup, Email: "fresh@example.com"})
	if err != nil || res.Status != TesterInvited || res.ID != "t-new-1" {
		t.Fatalf("result = %+v, err = %v", res, err)
	}
	attrs := obj(t, f.body("POST /v1/betaTesters"), "data", "attributes")
	if attrs["firstName"] != "Team" || attrs["lastName"] != "Member" {
		t.Errorf("names must come from the team record: %v", attrs)
	}
}

func TestAddTesterInternalGroupInvitesToTeam(t *testing.T) {
	f := newFake(t)
	var log bytes.Buffer
	c := f.client(t)
	_, err := AddTester(context.Background(), c, &TesterOptions{AppID: "app-1", Group: internalGroup, Email: "new@example.com", Log: &log})
	if err == nil || !strings.Contains(err.Error(), "--first") {
		t.Errorf("names are required for a team invitation: %v", err)
	}
	if f.called("POST /v1/userInvitations") || f.called("POST /v1/betaTesters") {
		t.Errorf("nothing may be sent without names: %v", f.calls)
	}

	res, err := AddTester(context.Background(), c, &TesterOptions{AppID: "app-1", Group: internalGroup, Email: "new@example.com", FirstName: "New", LastName: "Person", TeamRole: "DEVELOPER", Log: &log})
	if err != nil || res.Status != TesterTeamInviteSent || res.ID != "inv-1" {
		t.Fatalf("result = %+v, err = %v", res, err)
	}
	invite := obj(t, f.body("POST /v1/userInvitations"), "data")
	attrs := obj(t, invite, "attributes")
	if attrs["email"] != "new@example.com" || attrs["firstName"] != "New" || attrs["lastName"] != "Person" || attrs["allAppsVisible"] != false || arr(t, attrs, "roles")[0] != "DEVELOPER" {
		t.Errorf("invitation = %v", attrs)
	}
	if apps := arr(t, invite, "relationships", "visibleApps", "data"); len(apps) != 1 || obj(t, apps[0])["id"] != "app-1" {
		t.Errorf("visibleApps = %v", invite["relationships"])
	}
	if f.called("POST /v1/betaTesters") || f.called("POST /v1/betaGroups/g-int/relationships/betaTesters") {
		t.Errorf("no tester record exists before the invitation is accepted: %v", f.calls)
	}
	if !strings.Contains(log.String(), "Invited new@example.com to the team as DEVELOPER; they must accept the email") {
		t.Errorf("log = %q", log.String())
	}

	// A second run before they accept finds the pending invitation and sends nothing.
	f = newFake(t)
	f.pendingInvite = true
	log.Reset()
	res, err = AddTester(context.Background(), f.client(t), &TesterOptions{AppID: "app-1", Group: internalGroup, Email: "new@example.com", Log: &log})
	if err != nil || res.Status != TesterTeamInvitePending || res.ID != "inv-0" || f.called("POST /v1/userInvitations") {
		t.Fatalf("result = %+v, err = %v, calls = %v", res, err, f.calls)
	}
	if !strings.Contains(log.String(), "already has a pending team invitation") {
		t.Errorf("log = %q", log.String())
	}
}
