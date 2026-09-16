package asc

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newTestClient returns a client pointed at srv with fast retries.
func newTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	creds, _ := testCredentials(t)
	c, err := NewClient(creds, WithBaseURL(srv.URL), WithRetryDelay(time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func TestGetSendsBearerTokenAndDecodes(t *testing.T) {
	var authz string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authz = r.Header.Get("Authorization")
		if r.URL.Path != "/v1/apps" || r.URL.Query().Get("filter[bundleId]") != "com.example.app" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		writeJSON(w, 200, map[string]any{"data": []map[string]any{{
			"type": "apps", "id": "app-1",
			"attributes": map[string]any{"bundleId": "com.example.app", "name": "Example", "primaryLocale": "en-US"},
		}}})
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	app, err := c.AppByBundleID(context.Background(), "com.example.app")
	if err != nil {
		t.Fatal(err)
	}
	if app.ID != "app-1" || app.Name != "Example" || app.PrimaryLocale != "en-US" {
		t.Errorf("app = %+v", app)
	}
	if !strings.HasPrefix(authz, "Bearer ") || strings.Count(authz, ".") != 2 {
		t.Errorf("Authorization = %q", authz)
	}
}

func TestAppByBundleIDNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"data": []any{}})
	}))
	defer srv.Close()
	_, err := newTestClient(t, srv).AppByBundleID(context.Background(), "com.missing")
	if err == nil || !strings.Contains(err.Error(), "com.missing") {
		t.Errorf("err = %v", err)
	}
}

func TestErrorDecoding(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 409, map[string]any{"errors": []map[string]any{
			{"id": "x", "status": "409", "code": "STATE_ERROR.ENTITY_STATE_INVALID", "title": "Invalid state", "detail": "Metadata is missing.", "source": map[string]string{"pointer": "/data/relationships/build"}},
			{"status": "409", "code": "ENTITY_ERROR.ATTRIBUTE.REQUIRED", "title": "Attribute required", "detail": "Attribute required"},
		}})
	}))
	defer srv.Close()
	err := newTestClient(t, srv).Post(context.Background(), "/v1/reviewSubmissions", map[string]any{}, nil)
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %T %v", err, err)
	}
	if apiErr.StatusCode != 409 || len(apiErr.Errors) != 2 || !apiErr.HasCode("STATE_ERROR") || !IsStatus(err, 409) {
		t.Errorf("apiErr = %+v", apiErr)
	}
	msg := err.Error()
	for _, want := range []string{"HTTP 409", "STATE_ERROR.ENTITY_STATE_INVALID: Invalid state (Metadata is missing.) [/data/relationships/build]", "; ENTITY_ERROR.ATTRIBUTE.REQUIRED: Attribute required"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q lacks %q", msg, want)
		}
	}
	if strings.Contains(msg, "\n") {
		t.Errorf("message is not one line: %q", msg)
	}
}

func TestNonJSONErrorBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(502)
		_, _ = w.Write([]byte("<html>Bad Gateway</html>"))
	}))
	defer srv.Close()
	err := newTestClient(t, srv).Post(context.Background(), "/v1/x", nil, nil)
	if !IsStatus(err, 502) || !strings.Contains(err.Error(), "Bad Gateway") {
		t.Errorf("err = %v", err)
	}
}

func TestPaginationFollowsNextLink(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("limit") != "200" || q.Get("filter[app]") != "app-1" {
			t.Errorf("query = %v", q)
		}
		switch q.Get("cursor") {
		case "":
			writeJSON(w, 200, map[string]any{
				"data":  []map[string]any{{"type": "betaGroups", "id": "g1", "attributes": map[string]any{"name": "Internal", "isInternalGroup": true}}},
				"links": map[string]string{"next": srv.URL + "/v1/betaGroups?filter%5Bapp%5D=app-1&limit=200&cursor=abc"},
			})
		case "abc":
			writeJSON(w, 200, map[string]any{
				"data": []map[string]any{{"type": "betaGroups", "id": "g2", "attributes": map[string]any{"name": "External", "isInternalGroup": false, "publicLinkEnabled": true}}},
			})
		default:
			t.Errorf("unexpected cursor %q", q.Get("cursor"))
		}
	}))
	defer srv.Close()
	groups, err := newTestClient(t, srv).ListBetaGroups(context.Background(), "app-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 || groups[0].Name != "Internal" || !groups[0].Internal || groups[1].Name != "External" || groups[1].Internal || !groups[1].PublicLinkEnabled {
		t.Errorf("groups = %+v", groups)
	}
}

func TestRetryOn429HonorsRetryAfter(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			writeJSON(w, 429, map[string]any{"errors": []map[string]any{{"code": "RATE_LIMIT_EXCEEDED", "title": "Rate limit"}}})
			return
		}
		writeJSON(w, 201, map[string]any{"data": map[string]any{"type": "reviewSubmissions", "id": "rs-1", "attributes": map[string]any{"state": "READY_FOR_REVIEW"}}})
	}))
	defer srv.Close()
	start := time.Now()
	sub, err := newTestClient(t, srv).CreateReviewSubmission(context.Background(), "app-1", PlatformIOS)
	if err != nil {
		t.Fatal(err)
	}
	if sub.ID != "rs-1" || calls.Load() != 2 {
		t.Errorf("sub = %+v, calls = %d", sub, calls.Load())
	}
	if time.Since(start) < time.Second {
		t.Error("Retry-After was not honored")
	}
}

func TestRetryOn5xxOnlyForIdempotentMethods(t *testing.T) {
	var gets, posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if gets.Add(1) < 3 {
				writeJSON(w, 503, map[string]any{"errors": []map[string]any{{"title": "unavailable"}}})
				return
			}
			writeJSON(w, 200, map[string]any{"data": map[string]any{"type": "builds", "id": "b1", "attributes": map[string]any{"version": "7", "processingState": "VALID"}}})
		case http.MethodPost:
			posts.Add(1)
			writeJSON(w, 500, map[string]any{"errors": []map[string]any{{"title": "boom"}}})
		}
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	b, err := c.GetBuild(context.Background(), "b1")
	if err != nil || b.BuildNumber != "7" || b.ProcessingState != ProcessingStateValid {
		t.Errorf("build = %+v, err = %v", b, err)
	}
	if gets.Load() != 3 {
		t.Errorf("GET attempts = %d, want 3", gets.Load())
	}
	if err := c.Post(context.Background(), "/v1/things", map[string]any{}, nil); !IsStatus(err, 500) {
		t.Errorf("POST err = %v", err)
	}
	if posts.Load() != 1 {
		t.Errorf("POST attempts = %d, want 1 (a 5xx POST may have created the resource)", posts.Load())
	}
}

func TestRetryGivesUp(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeJSON(w, 503, map[string]any{"errors": []map[string]any{{"title": "unavailable"}}})
	}))
	defer srv.Close()
	err := newTestClient(t, srv).Get(context.Background(), "/v1/apps", url.Values{"limit": {"1"}}, nil)
	if !IsStatus(err, 503) || calls.Load() != 4 {
		t.Errorf("err = %v, calls = %d (want 1 + 3 retries)", err, calls.Load())
	}
}

func TestRequestBodiesAreJSONAPI(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %q", r.Header.Get("Content-Type"))
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch r.URL.Path {
		case "/v1/builds/b1":
			writeJSON(w, 200, map[string]any{"data": map[string]any{"type": "builds", "id": "b1", "attributes": map[string]any{"usesNonExemptEncryption": false}}})
		case "/v1/builds/b1/relationships/betaGroups":
			w.WriteHeader(204)
		case "/v1/betaAppReviewSubmissions":
			writeJSON(w, 201, map[string]any{"data": map[string]any{"type": "betaAppReviewSubmissions", "id": "bar-1", "attributes": map[string]any{"betaReviewState": "WAITING_FOR_REVIEW"}}})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	ctx := context.Background()

	b, err := c.SetUsesNonExemptEncryption(ctx, "b1", false)
	if err != nil || b.UsesNonExemptEncryption == nil || *b.UsesNonExemptEncryption {
		t.Fatalf("build = %+v, err = %v", b, err)
	}
	data := body["data"].(map[string]any)
	if data["type"] != "builds" || data["id"] != "b1" || data["attributes"].(map[string]any)["usesNonExemptEncryption"] != false {
		t.Errorf("PATCH body = %v", body)
	}

	if err := c.AddBuildToBetaGroups(ctx, "b1", []string{"g1", "g2"}); err != nil {
		t.Fatal(err)
	}
	linkages := body["data"].([]any)
	if len(linkages) != 2 || linkages[1].(map[string]any)["id"] != "g2" || linkages[0].(map[string]any)["type"] != "betaGroups" {
		t.Errorf("relationship body = %v", body)
	}

	sub, err := c.SubmitBuildForBetaReview(ctx, "b1")
	if err != nil || sub.ID != "bar-1" || sub.State != BetaReviewWaiting {
		t.Fatalf("sub = %+v, err = %v", sub, err)
	}
	data = body["data"].(map[string]any)
	if _, has := data["attributes"]; has {
		t.Errorf("empty attributes must be omitted: %v", body)
	}
	if data["relationships"].(map[string]any)["build"].(map[string]any)["data"].(map[string]any)["id"] != "b1" {
		t.Errorf("POST body = %v", body)
	}
}

func TestBetaAppReviewSubmissionAbsent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/builds/none/betaAppReviewSubmission":
			writeJSON(w, 200, map[string]any{"data": nil})
		default:
			writeJSON(w, 404, map[string]any{"errors": []map[string]any{{"code": "NOT_FOUND", "title": "not found"}}})
		}
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	for _, id := range []string{"none", "missing"} {
		sub, err := c.GetBuildBetaAppReviewSubmission(context.Background(), id)
		if err != nil || sub != nil {
			t.Errorf("%s: sub = %+v, err = %v", id, sub, err)
		}
	}
}
