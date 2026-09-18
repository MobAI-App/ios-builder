package github

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMintAssetURLDoesNotFollowTheRedirect(t *testing.T) {
	var fetched bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r/releases/assets/7":
			if r.Header.Get("Accept") != "application/octet-stream" || r.Header.Get("Authorization") != "Bearer tok" {
				t.Errorf("headers: %v", r.Header)
			}
			http.Redirect(w, r, "https://release-assets.githubusercontent.com/x?jwt=a.b.c", http.StatusFound)
		default:
			fetched = true
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	c := NewClientWithBaseURL("tok", srv.URL)
	got, err := c.MintAssetURL(context.Background(), "o", "r", 7)
	if err != nil || got != "https://release-assets.githubusercontent.com/x?jwt=a.b.c" || fetched {
		t.Errorf("MintAssetURL = %q, %v (fetched %v)", got, err, fetched)
	}
	if _, err := c.MintAssetURL(context.Background(), "o", "r", 8); err == nil {
		t.Error("a non-redirect was accepted")
	}
}

func TestUploadReleaseAssetDropsTheURITemplate(t *testing.T) {
	var progress []int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/upload/1/assets" || r.URL.Query().Get("name") != "My App.ipa" || r.Header.Get("Content-Type") != "application/octet-stream" {
			t.Errorf("request: %s %s %v", r.Method, r.URL, r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != "ipa bytes" || r.ContentLength != 9 {
			t.Errorf("body %q length %d", body, r.ContentLength)
		}
		w.WriteHeader(201)
		fmt.Fprint(w, `{"id":42,"name":"My.App.ipa","size":9}`)
	}))
	defer srv.Close()
	c := NewClient("tok")
	asset, err := c.UploadReleaseAsset(context.Background(), srv.URL+"/upload/1/assets{?name,label}", "My App.ipa", strings.NewReader("ipa bytes"), 9, func(done, _ int64) { progress = append(progress, done) })
	if err != nil || asset.ID != 42 {
		t.Fatalf("asset = %+v, %v", asset, err)
	}
	if len(progress) == 0 || progress[len(progress)-1] != 9 {
		t.Errorf("progress = %v", progress)
	}
}

func TestCreateSecretGistNamesTheMissingScope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Method != "POST" || r.URL.Path != "/gists" || !strings.Contains(string(body), `"public":false`) {
			t.Errorf("request: %s %s %s", r.Method, r.URL, body)
		}
		w.WriteHeader(404)
		fmt.Fprint(w, `{"message":"Not Found","status":"404"}`)
	}))
	defer srv.Close()
	c := NewClientWithBaseURL("tok", srv.URL)
	_, err := c.CreateSecretGist(context.Background(), "d", map[string]string{"manifest.plist": "x"})
	if !errors.Is(err, ErrGistScope) || !strings.Contains(err.Error(), "builder auth github") {
		t.Errorf("err = %v", err)
	}
}

func TestListReleasesAndGistsFollowPages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		if r.URL.Query().Get("per_page") != "100" {
			t.Errorf("per_page missing on %s", r.URL)
		}
		switch {
		case r.URL.Path == "/repos/o/r/releases" && page == "1":
			fmt.Fprint(w, "["+strings.Repeat(`{"id":1,"tag_name":"t","draft":true},`, 99)+`{"id":2,"tag_name":"u","draft":false}]`)
		case r.URL.Path == "/repos/o/r/releases":
			fmt.Fprint(w, `[{"id":3,"tag_name":"v","draft":true}]`)
		case r.URL.Path == "/gists" && page == "1":
			fmt.Fprint(w, `[{"id":"g1","description":"d","files":{"manifest.plist":{"raw_url":"u"}}}]`)
		default:
			t.Errorf("unexpected %s", r.URL)
		}
	}))
	defer srv.Close()
	c := NewClientWithBaseURL("tok", srv.URL)
	releases, err := c.ListReleases(context.Background(), "o", "r")
	if err != nil || len(releases) != 101 || releases[100].ID != 3 {
		t.Errorf("ListReleases = %d, %v", len(releases), err)
	}
	gists, err := c.ListGists(context.Background())
	if err != nil || len(gists) != 1 || gists[0].Files["manifest.plist"].RawURL != "u" {
		t.Errorf("ListGists = %+v, %v", gists, err)
	}
}
