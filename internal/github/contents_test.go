package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

// apiServer answers requests with handler and returns a client pointed at it.
func apiServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("unexpected auth %q on %s %s", r.Header.Get("Authorization"), r.Method, r.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return NewClientWithBaseURL("tok", srv.URL)
}

func TestListRepositoriesFollowsPages(t *testing.T) {
	var queries []string
	c := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user/repos" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		queries = append(queries, r.URL.RawQuery)
		var repos []Repository
		if r.URL.Query().Get("page") == "1" {
			for i := range 100 {
				repos = append(repos, Repository{FullName: fmt.Sprintf("o/r%d", i), DefaultRef: "main"})
			}
		} else {
			repos = []Repository{{FullName: "o/last", DefaultRef: "main", Private: true}}
		}
		if err := json.NewEncoder(w).Encode(repos); err != nil {
			t.Error(err)
		}
	})
	repos, err := c.ListRepositories(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 101 || repos[100].FullName != "o/last" || !repos[100].Private {
		t.Fatalf("repos: %d, last %+v", len(repos), repos[len(repos)-1])
	}
	want := []string{
		"per_page=100&page=1&sort=pushed&affiliation=owner,collaborator,organization_member",
		"per_page=100&page=2&sort=pushed&affiliation=owner,collaborator,organization_member",
	}
	if !slices.Equal(queries, want) {
		t.Errorf("pages requested: %v, want %v", queries, want)
	}
}

func TestListBranches(t *testing.T) {
	c := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/branches" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		fmt.Fprint(w, `[{"name":"main","protected":true},{"name":"dev"}]`)
	})
	names, err := c.ListBranches(context.Background(), "o", "r")
	if err != nil || !slices.Equal(names, []string{"main", "dev"}) {
		t.Fatalf("branches: %v, %v", names, err)
	}
}

// GitHub returns the content base64-wrapped at 60 columns; GetFile hands
// back the bytes and the blob SHA a later PutFile needs.
func TestGetFileDecodesContent(t *testing.T) {
	c := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/contents/dir/builder.json" || r.URL.Query().Get("ref") != "main" {
			t.Errorf("unexpected request %s", r.URL)
		}
		content := base64.StdEncoding.EncodeToString([]byte(`{"project": "App"}`))
		fmt.Fprintf(w, `{"path":"dir/builder.json","sha":"blob1","encoding":"base64","content":"%s\n%s\n"}`, content[:10], content[10:])
	})
	file, err := c.GetFile(context.Background(), "o", "r", "dir/builder.json", "main")
	if err != nil {
		t.Fatal(err)
	}
	if string(file.Content) != `{"project": "App"}` || file.SHA != "blob1" || file.Path != "dir/builder.json" {
		t.Errorf("file = %+v (%q)", file, file.Content)
	}
}

func TestGetFileNotFound(t *testing.T) {
	c := apiServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message":"Not Found","status":"404"}`)
	})
	_, err := c.GetFile(context.Background(), "o", "r", "builder.json", "")
	if !errors.Is(err, ErrFileNotFound) {
		t.Fatalf("missing file: %v", err)
	}
	// Other failures are not "not found".
	c = apiServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"message":"forbidden"}`)
	})
	if _, err := c.GetFile(context.Background(), "o", "r", "builder.json", ""); err == nil || errors.Is(err, ErrFileNotFound) {
		t.Fatalf("403: %v", err)
	}
}

func TestPutFileCommits(t *testing.T) {
	var body map[string]string
	c := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/repos/o/r/contents/builder.json" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"content":{"sha":"blob2"},"commit":{"sha":"abc123","html_url":"https://github.com/o/r/commit/abc123"}}`)
	})
	commit, err := c.PutFile(context.Background(), "o", "r", "builder.json", &PutFileRequest{
		Message: "signing profile", Content: []byte("{}"), Branch: "main", SHA: "blob1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if commit.SHA != "abc123" || commit.HTMLURL != "https://github.com/o/r/commit/abc123" {
		t.Errorf("commit = %+v", commit)
	}
	want := map[string]string{"message": "signing profile", "content": base64.StdEncoding.EncodeToString([]byte("{}")), "branch": "main", "sha": "blob1"}
	if len(body) != len(want) {
		t.Errorf("body = %v, want %v", body, want)
	}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("body[%s] = %q, want %q", k, body[k], v)
		}
	}
}

// A new file carries no sha, and a path with spaces is escaped per segment.
func TestPutFileNewFileEscapesPath(t *testing.T) {
	c := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/repos/o/r/contents/ios%20app/builder.json" {
			t.Errorf("unexpected path %s", r.URL.EscapedPath())
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if _, ok := body["sha"]; ok {
			t.Errorf("sha sent for a new file: %v", body)
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"commit":{"sha":"new"}}`)
	})
	commit, err := c.PutFile(context.Background(), "o", "r", "ios app/builder.json", &PutFileRequest{Message: "m", Content: []byte("{}")})
	if err != nil || commit.SHA != "new" {
		t.Fatalf("new file: %+v, %v", commit, err)
	}
}
