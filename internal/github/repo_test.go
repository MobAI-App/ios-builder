package github

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// secretsServer answers the secrets listing of o/r with handler and returns a
// client pointed at it.
func secretsServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/repos/o/r/actions/secrets" || r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("unexpected request: %s %s (auth %q)", r.Method, r.URL, r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	c := NewClient("tok")
	c.baseURL = srv.URL
	return c
}

func TestListSecretNamesFollowsPages(t *testing.T) {
	var queries []string
	c := secretsServer(t, func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		switch r.URL.Query().Get("page") {
		case "1":
			fmt.Fprint(w, `{"total_count":3,"secrets":[{"name":"IOS_CERTIFICATE_STORE","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"},{"name":"IOS_CERTIFICATE_PASSWORD_STORE","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}]}`)
		case "2":
			fmt.Fprint(w, `{"total_count":3,"secrets":[{"name":"IOS_PROVISIONING_PROFILE_STORE","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}]}`)
		default:
			t.Errorf("page %q requested after the last one", r.URL.Query().Get("page"))
			fmt.Fprint(w, `{"total_count":3,"secrets":[]}`)
		}
	})
	names, err := c.ListSecretNames(context.Background(), "o", "r")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"IOS_CERTIFICATE_STORE", "IOS_CERTIFICATE_PASSWORD_STORE", "IOS_PROVISIONING_PROFILE_STORE"}; !slices.Equal(names, want) {
		t.Errorf("names = %v, want %v", names, want)
	}
	if want := []string{"per_page=100&page=1", "per_page=100&page=2"}; !slices.Equal(queries, want) {
		t.Errorf("pages requested: %v, want %v", queries, want)
	}
}

func TestListSecretNamesEmpty(t *testing.T) {
	c := secretsServer(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"total_count":0,"secrets":[]}`)
	})
	if names, err := c.ListSecretNames(context.Background(), "o", "r"); err != nil || len(names) != 0 {
		t.Fatalf("empty repository: %v, %v", names, err)
	}
}

// A token without the repo scope gets 404, a non-admin 403; neither is an
// empty list, and the error says what the token lacks. The 403 body carries
// no status field, so the HTTP status must fill it in.
func TestListSecretNamesNeedsRepoAccess(t *testing.T) {
	for status, body := range map[int]string{
		404: `{"message":"Not Found","documentation_url":"https://docs.github.com/rest/actions/secrets#list-repository-secrets","status":"404"}`,
		403: `{"message":"Must have admin rights to Repository.","documentation_url":"https://docs.github.com/rest"}`,
	} {
		c := secretsServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			fmt.Fprint(w, body)
		})
		names, err := c.ListSecretNames(context.Background(), "o", "r")
		if err == nil || names != nil {
			t.Fatalf("%d: %v, %v", status, names, err)
		}
		for _, want := range []string{"o/r", "repo scope", "admin access", "builder auth github"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%d: error lacks %q: %v", status, want, err)
			}
		}
	}
	// Other failures are passed through as they are.
	c := secretsServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"message":"boom"}`)
	})
	if _, err := c.ListSecretNames(context.Background(), "o", "r"); err == nil || !strings.Contains(err.Error(), "boom") || strings.Contains(err.Error(), "repo scope") {
		t.Fatalf("500: %v", err)
	}
}
