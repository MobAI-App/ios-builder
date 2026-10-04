package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/MobAI-App/ios-builder/internal/exitcode"
)

func fakeGitHub(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	prev := githubAPI
	githubAPI = srv.URL
	t.Cleanup(func() { githubAPI = prev })
}

func TestCheckGitHubToken(t *testing.T) {
	fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "Bearer classic":
			w.Header().Set("X-OAuth-Scopes", "repo, workflow")
		case "Bearer fine":
		default:
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"login":"octo"}`))
	})

	id, err := CheckGitHubToken(context.Background(), "classic")
	if err != nil {
		t.Fatal(err)
	}
	if id.Login != "octo" || !id.ScopesKnown || !slices.Equal(id.Scopes, []string{"repo", "workflow"}) {
		t.Fatalf("classic token = %+v", id)
	}
	if got := MissingScopes(id.Scopes); !slices.Equal(got, []string{"gist"}) {
		t.Errorf("missing = %v, want [gist]", got)
	}

	id, err = CheckGitHubToken(context.Background(), "fine")
	if err != nil || id.ScopesKnown {
		t.Fatalf("fine-grained token = %+v, %v; scopes must be unknown", id, err)
	}

	_, err = CheckGitHubToken(context.Background(), "wrong")
	if exitcode.Code(err) != exitcode.Auth {
		t.Fatalf("rejected token: err = %v, want an auth error", err)
	}
}

func TestParseScopes(t *testing.T) {
	if got := ParseScopes("repo,workflow gist"); !slices.Equal(got, []string{"repo", "workflow", "gist"}) {
		t.Errorf("ParseScopes = %v", got)
	}
	if got := MissingScopes(ParseScopes("")); !slices.Equal(got, RequiredScopes) {
		t.Errorf("MissingScopes(none) = %v", got)
	}
}
