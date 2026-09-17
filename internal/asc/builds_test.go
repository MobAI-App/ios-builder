package asc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestListBuildsDetailsUsesIncluded(t *testing.T) {
	var query url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		writeJSON(w, 200, map[string]any{
			"data": []map[string]any{
				{"type": "builds", "id": "b2", "attributes": map[string]any{"version": "8", "processingState": "VALID", "uploadedDate": "2026-09-16T10:00:00Z", "expired": false},
					"relationships": map[string]any{
						"preReleaseVersion": map[string]any{"data": map[string]string{"type": "preReleaseVersions", "id": "pre-1"}},
						"betaGroups":        map[string]any{"data": []map[string]string{{"type": "betaGroups", "id": "g1"}, {"type": "betaGroups", "id": "g-gone"}}},
					}},
				{"type": "builds", "id": "b1", "attributes": map[string]any{"version": "7", "processingState": "VALID", "expired": true},
					"relationships": map[string]any{
						"preReleaseVersion": map[string]any{"data": map[string]string{"type": "preReleaseVersions", "id": "pre-1"}},
						"betaGroups":        map[string]any{"data": []any{}},
					}},
			},
			"included": []map[string]any{
				{"type": "preReleaseVersions", "id": "pre-1", "attributes": map[string]any{"version": "2.0.0", "platform": "IOS"}},
				{"type": "betaGroups", "id": "g1", "attributes": map[string]any{"name": "Team", "isInternalGroup": true}},
			},
		})
	}))
	defer srv.Close()
	builds, err := newTestClient(t, srv).ListBuilds(context.Background(), &BuildFilter{AppID: "app-1", Platform: PlatformIOS, Details: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if query.Get("include") != "preReleaseVersion,betaGroups" || query.Get("limit[betaGroups]") != "50" || query.Get("limit") != "10" || query.Get("filter[app]") != "app-1" {
		t.Errorf("query = %v", query)
	}
	if len(builds) != 2 || builds[0].Version != "2.0.0" || builds[0].BuildNumber != "8" || builds[1].Version != "2.0.0" || !builds[1].Expired {
		t.Errorf("builds = %+v", builds)
	}
	if g := builds[0].BetaGroups; len(g) != 2 || g[0] != "Team" || g[1] != "g-gone" {
		t.Errorf("groups = %v (unknown included IDs fall back to the ID)", g)
	}
	if builds[1].BetaGroups == nil || len(builds[1].BetaGroups) != 0 {
		t.Errorf("no groups must be an empty list, got %#v", builds[1].BetaGroups)
	}
}

func TestListBuildsLargeLimitFollowsPagesAndCuts(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("limit") != "200" {
			t.Errorf("limit = %q, want the page maximum", q.Get("limit"))
		}
		build := func(id string) map[string]any {
			return map[string]any{"type": "builds", "id": id, "attributes": map[string]any{"version": id}}
		}
		if q.Get("cursor") == "" {
			writeJSON(w, 200, map[string]any{"data": []map[string]any{build("1"), build("2")}, "links": map[string]string{"next": srv.URL + "/v1/builds?limit=200&cursor=x"}})
			return
		}
		writeJSON(w, 200, map[string]any{"data": []map[string]any{build("3"), build("4")}})
	}))
	defer srv.Close()
	builds, err := newTestClient(t, srv).ListBuilds(context.Background(), &BuildFilter{AppID: "app-1", Limit: 300})
	if err != nil || len(builds) != 4 {
		t.Fatalf("builds = %+v, err = %v", builds, err)
	}
	builds, err = newTestClient(t, srv).ListBuilds(context.Background(), &BuildFilter{AppID: "app-1", Limit: 201})
	if err != nil || len(builds) != 4 {
		t.Fatalf("builds = %+v, err = %v", builds, err)
	}
}

func TestExpireBuild(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/v1/builds/b1" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		writeJSON(w, 200, map[string]any{"data": map[string]any{"type": "builds", "id": "b1", "attributes": map[string]any{"version": "7", "expired": true, "processingState": "VALID"}}})
	}))
	defer srv.Close()
	b, err := newTestClient(t, srv).ExpireBuild(context.Background(), "b1")
	if err != nil || !b.Expired || b.BuildNumber != "7" {
		t.Fatalf("build = %+v, err = %v", b, err)
	}
	data := obj(t, body, "data")
	if data["type"] != "builds" || data["id"] != "b1" || obj(t, data, "attributes")["expired"] != true {
		t.Errorf("PATCH body = %v", body)
	}
	if _, has := obj(t, data, "attributes")["usesNonExemptEncryption"]; has {
		t.Errorf("expire must not touch compliance: %v", body)
	}
}

func TestListApps(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/v1/apps" || q.Get("sort") != "name" {
			t.Errorf("unexpected request %s", r.URL)
		}
		if q.Get("cursor") == "" {
			writeJSON(w, 200, map[string]any{
				"data":  []map[string]any{{"type": "apps", "id": "app-1", "attributes": map[string]any{"name": "Alpha", "bundleId": "com.example.alpha", "sku": "ALPHA1"}}},
				"links": map[string]string{"next": srv.URL + "/v1/apps?sort=name&limit=200&cursor=n"},
			})
			return
		}
		writeJSON(w, 200, map[string]any{"data": []map[string]any{{"type": "apps", "id": "app-2", "attributes": map[string]any{"name": "Beta", "bundleId": "com.example.beta"}}}})
	}))
	defer srv.Close()
	apps, err := newTestClient(t, srv).ListApps(context.Background())
	if err != nil || len(apps) != 2 || apps[0].SKU != "ALPHA1" || apps[1].BundleID != "com.example.beta" {
		t.Errorf("apps = %+v, err = %v", apps, err)
	}
}
