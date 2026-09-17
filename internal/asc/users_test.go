package asc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFindUserMatchesExactly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/users" || r.URL.Query().Get("filter[username]") == "" {
			t.Errorf("unexpected request %s", r.URL)
		}
		// Apple's filter is a substring match; both come back for "dev@example.com".
		writeJSON(w, 200, map[string]any{"data": []map[string]any{
			{"type": "users", "id": "u2", "attributes": map[string]any{"username": "otherdev@example.com", "roles": []string{"DEVELOPER"}}},
			{"type": "users", "id": "u1", "attributes": map[string]any{"username": "Dev@example.com", "firstName": "Dee", "lastName": "Vee", "roles": []string{"APP_MANAGER", "DEVELOPER"}, "allAppsVisible": true}},
		}})
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	u, err := c.FindUser(context.Background(), "dev@example.com")
	if err != nil || u == nil || u.ID != "u1" || u.FirstName != "Dee" || len(u.Roles) != 2 || !u.AllAppsVisible {
		t.Errorf("user = %+v, err = %v", u, err)
	}
	if u, err = c.FindUser(context.Background(), "nobody@example.com"); err != nil || u != nil {
		t.Errorf("user = %+v, err = %v", u, err)
	}
}

func TestInviteUserBody(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/userInvitations" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		attrs := obj(t, body, "data", "attributes")
		attrs["expirationDate"] = "2026-10-01T00:00:00Z"
		writeJSON(w, 201, map[string]any{"data": map[string]any{"type": "userInvitations", "id": "inv-1", "attributes": attrs}})
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	inv, err := c.InviteUser(context.Background(), &UserInvitationSpec{Email: "new@example.com", FirstName: "New", LastName: "Person", Roles: []string{RoleCustomerSupport}, VisibleAppIDs: []string{"app-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if inv.ID != "inv-1" || inv.Email != "new@example.com" || inv.ExpirationDate.IsZero() || len(inv.Roles) != 1 {
		t.Errorf("invitation = %+v", inv)
	}
	data := obj(t, body, "data")
	attrs := obj(t, data, "attributes")
	if data["type"] != "userInvitations" || attrs["email"] != "new@example.com" || attrs["firstName"] != "New" || attrs["lastName"] != "Person" || attrs["allAppsVisible"] != false {
		t.Errorf("attributes = %v", attrs)
	}
	if roles := arr(t, attrs, "roles"); len(roles) != 1 || roles[0] != "CUSTOMER_SUPPORT" {
		t.Errorf("roles = %v", roles)
	}
	if apps := arr(t, data, "relationships", "visibleApps", "data"); len(apps) != 1 || obj(t, apps[0])["id"] != "app-1" || obj(t, apps[0])["type"] != "apps" {
		t.Errorf("visibleApps = %v", data["relationships"])
	}

	if _, err := c.InviteUser(context.Background(), &UserInvitationSpec{Email: "admin@example.com", FirstName: "A", LastName: "D", Roles: []string{"ADMIN"}, AllAppsVisible: true}); err != nil {
		t.Fatal(err)
	}
	if _, has := obj(t, body, "data")["relationships"]; has || obj(t, body, "data", "attributes")["allAppsVisible"] != true {
		t.Errorf("all-apps invitation must carry no visibleApps: %v", body)
	}
}

func TestFindUserInvitation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/userInvitations" || r.URL.Query().Get("filter[email]") != "new@example.com" {
			t.Errorf("unexpected request %s", r.URL)
		}
		writeJSON(w, 200, map[string]any{"data": []map[string]any{
			{"type": "userInvitations", "id": "inv-1", "attributes": map[string]any{"email": "new@example.com", "roles": []string{"CUSTOMER_SUPPORT"}, "expirationDate": "2026-10-01T00:00:00Z"}},
		}})
	}))
	defer srv.Close()
	inv, err := newTestClient(t, srv).FindUserInvitation(context.Background(), "new@example.com")
	if err != nil || inv == nil || inv.ID != "inv-1" || inv.ExpirationDate.Year() != 2026 {
		t.Errorf("invitation = %+v, err = %v", inv, err)
	}
}

func TestListUsers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/users" || r.URL.Query().Get("sort") != "username" {
			t.Errorf("unexpected request %s", r.URL)
		}
		writeJSON(w, 200, map[string]any{"data": []map[string]any{{"type": "users", "id": "u1", "attributes": map[string]any{"username": "a@example.com", "roles": []string{"ADMIN"}}}}})
	}))
	defer srv.Close()
	users, err := newTestClient(t, srv).ListUsers(context.Background())
	if err != nil || len(users) != 1 || users[0].Email != "a@example.com" || users[0].Roles[0] != "ADMIN" {
		t.Errorf("users = %+v, err = %v", users, err)
	}
}
