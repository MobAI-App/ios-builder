package asc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCreateBetaGroupBodies(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/betaGroups" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		attrs := obj(t, body, "data", "attributes")
		attrs["publicLink"] = "https://testflight.apple.com/join/abc"
		writeJSON(w, 201, map[string]any{"data": map[string]any{"type": "betaGroups", "id": "g-new", "attributes": attrs}})
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	ctx := context.Background()

	g, err := c.CreateBetaGroup(ctx, BetaGroupSpec{AppID: "app-1", Name: "Team", Internal: true, HasAccessToAllBuilds: true})
	if err != nil {
		t.Fatal(err)
	}
	if g.ID != "g-new" || g.Name != "Team" || !g.Internal || !g.HasAccessToAllBuilds || g.PublicLinkEnabled {
		t.Errorf("group = %+v", g)
	}
	data := obj(t, body, "data")
	attrs := obj(t, data, "attributes")
	if attrs["name"] != "Team" || attrs["isInternalGroup"] != true || attrs["hasAccessToAllBuilds"] != true {
		t.Errorf("internal attributes = %v", attrs)
	}
	if _, has := attrs["publicLinkEnabled"]; has {
		t.Errorf("publicLinkEnabled must be omitted unless asked: %v", attrs)
	}
	if obj(t, data, "relationships", "app", "data")["id"] != "app-1" {
		t.Errorf("app relationship = %v", data["relationships"])
	}

	g, err = c.CreateBetaGroup(ctx, BetaGroupSpec{AppID: "app-1", Name: "Public", PublicLinkEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if g.Internal || !g.PublicLinkEnabled || g.PublicLink != "https://testflight.apple.com/join/abc" {
		t.Errorf("group = %+v", g)
	}
	attrs = obj(t, body, "data", "attributes")
	if attrs["isInternalGroup"] != false || attrs["publicLinkEnabled"] != true {
		t.Errorf("external attributes = %v", attrs)
	}
	if _, has := attrs["hasAccessToAllBuilds"]; has {
		t.Errorf("hasAccessToAllBuilds is internal-only: %v", attrs)
	}
}

func TestMatchBetaGroup(t *testing.T) {
	groups := []BetaGroup{{ID: "g1", Name: "Team", Internal: true}, {ID: "g2", Name: "Beta Testers"}, {ID: "g3", Name: "beta testers"}}
	if g, err := MatchBetaGroup(groups, "team"); err != nil || g == nil || g.ID != "g1" {
		t.Errorf("case-insensitive match = %+v, err = %v", g, err)
	}
	if g, err := MatchBetaGroup(groups, "Nightly"); err != nil || g != nil {
		t.Errorf("no match = %+v, err = %v", g, err)
	}
	g, err := MatchBetaGroup(groups, "Beta Testers")
	if g != nil || err == nil || !strings.Contains(err.Error(), "Beta Testers (g2)") || !strings.Contains(err.Error(), "beta testers (g3)") {
		t.Errorf("duplicates must be refused and listed: %+v, %v", g, err)
	}
}

func TestBetaGroupDeleteAndTesterLinkages(t *testing.T) {
	var calls []string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		body = nil
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(204)
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	ctx := context.Background()

	if err := c.AddBetaTestersToGroup(ctx, "g1", []string{"t1", "t2"}); err != nil {
		t.Fatal(err)
	}
	links := arr(t, body, "data")
	if len(links) != 2 || obj(t, links[0])["type"] != "betaTesters" || obj(t, links[1])["id"] != "t2" {
		t.Errorf("add body = %v", body)
	}
	if err := c.RemoveBetaTestersFromGroup(ctx, "g1", []string{"t1"}); err != nil {
		t.Fatal(err)
	}
	if links = arr(t, body, "data"); len(links) != 1 || obj(t, links[0])["id"] != "t1" {
		t.Errorf("remove body = %v", body)
	}
	if err := c.DeleteBetaGroup(ctx, "g1"); err != nil {
		t.Fatal(err)
	}
	want := []string{"POST /v1/betaGroups/g1/relationships/betaTesters", "DELETE /v1/betaGroups/g1/relationships/betaTesters", "DELETE /v1/betaGroups/g1"}
	if len(calls) != len(want) {
		t.Fatalf("calls = %v", calls)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Errorf("call %d = %s, want %s", i, calls[i], want[i])
		}
	}
}
