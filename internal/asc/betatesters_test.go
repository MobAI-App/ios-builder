package asc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestListBetaTestersFilters(t *testing.T) {
	var query url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/betaTesters" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		query = r.URL.Query()
		writeJSON(w, 200, map[string]any{"data": []map[string]any{
			{"type": "betaTesters", "id": "t1", "attributes": map[string]any{"email": "a@example.com", "firstName": "Ann", "lastName": "Lee", "inviteType": "EMAIL", "state": "INSTALLED"}},
			{"type": "betaTesters", "id": "t2", "attributes": map[string]any{"email": "b@example.com", "state": "NOT_INVITED"}},
		}})
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	testers, err := c.ListBetaTesters(context.Background(), &BetaTesterFilter{AppID: "app-1", GroupID: "g1", Email: "a@"})
	if err != nil {
		t.Fatal(err)
	}
	if query.Get("filter[apps]") != "app-1" || query.Get("filter[betaGroups]") != "g1" || query.Get("filter[email]") != "a@" || query.Get("sort") != "email" || query.Get("limit") != "200" {
		t.Errorf("query = %v", query)
	}
	if len(testers) != 2 || testers[0].ID != "t1" || testers[0].FirstName != "Ann" || testers[0].State != BetaTesterInstalled || testers[1].State != BetaTesterNotInvited {
		t.Errorf("testers = %+v", testers)
	}
	// FindBetaTester matches the address exactly, since Apple's filter is a substring match.
	found, err := c.FindBetaTester(context.Background(), &BetaTesterFilter{Email: "B@example.com"})
	if err != nil || found == nil || found.ID != "t2" {
		t.Errorf("found = %+v, err = %v", found, err)
	}
	if found, err = c.FindBetaTester(context.Background(), &BetaTesterFilter{Email: "example.com"}); err != nil || found != nil {
		t.Errorf("substring must not match: %+v, %v", found, err)
	}
}

// testerServer fakes the tester routes: POST answers 409 for a known address.
func testerServer(t *testing.T, known map[string]string) (*httptest.Server, *[]string, *map[string]any) {
	t.Helper()
	var calls []string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		body = nil
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch r.Method + " " + r.URL.Path {
		case "POST /v1/betaTesters":
			email, _ := obj(t, body, "data", "attributes")["email"].(string)
			if _, exists := known[email]; exists {
				writeJSON(w, 409, map[string]any{"errors": []map[string]any{{"status": "409", "code": "ENTITY_ERROR.ATTRIBUTE.INVALID.DUPLICATE", "title": "The provided entity includes an attribute with a value that has already been used", "detail": "A beta tester with the email '" + email + "' already exists."}}})
				return
			}
			writeJSON(w, 201, map[string]any{"data": map[string]any{"type": "betaTesters", "id": "t-new", "attributes": map[string]any{"email": email, "state": "INVITED"}}})
		case "GET /v1/betaTesters":
			email := r.URL.Query().Get("filter[email]")
			data := []map[string]any{}
			// An empty ID marks an address that conflicts but has no record to find.
			if id, ok := known[email]; ok && id != "" {
				data = append(data, map[string]any{"type": "betaTesters", "id": id, "attributes": map[string]any{"email": email, "state": "ACCEPTED"}})
			}
			writeJSON(w, 200, map[string]any{"data": data})
		case "POST /v1/betaGroups/g1/relationships/betaTesters", "DELETE /v1/betaTesters/t-old":
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &calls, &body
}

func TestCreateBetaTesterBody(t *testing.T) {
	srv, _, body := testerServer(t, nil)
	c := newTestClient(t, srv)
	tester, created, err := c.AddBetaTester(context.Background(), BetaTesterSpec{Email: "new@example.com", FirstName: "New", LastName: "Tester", GroupIDs: []string{"g1"}})
	if err != nil || !created || tester.ID != "t-new" || tester.State != BetaTesterInvited {
		t.Fatalf("tester = %+v, created = %v, err = %v", tester, created, err)
	}
	data := obj(t, *body, "data")
	attrs := obj(t, data, "attributes")
	if data["type"] != "betaTesters" || attrs["email"] != "new@example.com" || attrs["firstName"] != "New" || attrs["lastName"] != "Tester" {
		t.Errorf("body = %v", *body)
	}
	links := arr(t, data, "relationships", "betaGroups", "data")
	if len(links) != 1 || obj(t, links[0])["id"] != "g1" || obj(t, links[0])["type"] != "betaGroups" {
		t.Errorf("group relationship = %v", data["relationships"])
	}
}

func TestAddBetaTesterAlreadyExists(t *testing.T) {
	srv, calls, body := testerServer(t, map[string]string{"old@example.com": "t-old"})
	c := newTestClient(t, srv)
	tester, created, err := c.AddBetaTester(context.Background(), BetaTesterSpec{Email: "old@example.com", GroupIDs: []string{"g1"}})
	if err != nil || created || tester.ID != "t-old" {
		t.Fatalf("tester = %+v, created = %v, err = %v", tester, created, err)
	}
	want := []string{"POST /v1/betaTesters", "GET /v1/betaTesters", "POST /v1/betaGroups/g1/relationships/betaTesters"}
	if len(*calls) != len(want) {
		t.Fatalf("calls = %v", *calls)
	}
	for i := range want {
		if (*calls)[i] != want[i] {
			t.Errorf("call %d = %s, want %s", i, (*calls)[i], want[i])
		}
	}
	if links := arr(t, *body, "data"); len(links) != 1 || obj(t, links[0])["id"] != "t-old" {
		t.Errorf("linkage body = %v", *body)
	}
	if err := c.DeleteBetaTester(context.Background(), "t-old"); err != nil {
		t.Fatal(err)
	}
}

func TestAddBetaTesterConflictWithoutMatch(t *testing.T) {
	// A 409 for some other reason, with no tester of that address, surfaces as is.
	srv, _, _ := testerServer(t, map[string]string{"ghost@example.com": ""})
	c := newTestClient(t, srv)
	_, _, err := c.AddBetaTester(context.Background(), BetaTesterSpec{Email: "ghost@example.com", GroupIDs: []string{"g1"}})
	if !IsStatus(err, 409) {
		t.Errorf("err = %v, want the 409", err)
	}
}
