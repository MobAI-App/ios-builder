package ci

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
)

// codemagicFake is the v3 variable-group API of one app, two variables per
// page so paging is exercised.
type codemagicFake struct {
	mu     sync.Mutex
	groups map[string]string            // id -> name
	vars   map[string]map[string]string // group id -> variable id -> name
	values map[string]string            // variable id -> value
	secure map[string]bool
	calls  []string
	nextID int
}

func newCodemagicFake(t *testing.T) (*codemagicFake, *CodemagicSecrets) {
	f := &codemagicFake{groups: map[string]string{"g-other": "other"}, vars: map[string]map[string]string{}, values: map[string]string{}, secure: map[string]bool{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Header.Get("x-auth-token") != "cm-token" {
			t.Errorf("missing token on %s %s", r.Method, r.URL)
		}
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		page := 1
		_, _ = fmt.Sscan(r.URL.Query().Get("page"), &page)
		switch {
		case r.Method == "GET" && r.URL.Path == "/apps/app-1/variable-groups":
			var data []map[string]string
			for id, name := range f.groups {
				data = append(data, map[string]string{"id": id, "name": name})
			}
			writeJSONPage(w, data, page, 100)
		case r.Method == "POST" && r.URL.Path == "/apps/app-1/variable-groups":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.nextID++
			id := fmt.Sprintf("g%d", f.nextID)
			f.groups[id] = body["name"]
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"id": id, "name": body["name"]}})
		case len(parts) == 3 && parts[0] == "variable-groups" && parts[2] == "variables" && r.Method == "GET":
			var data []map[string]any
			ids := make([]string, 0)
			for id := range f.vars[parts[1]] {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			for _, id := range ids {
				data = append(data, map[string]any{"id": id, "name": f.vars[parts[1]][id], "value": nil, "secure": true})
			}
			writeJSONPage(w, data, page, 2)
		case len(parts) == 3 && parts[0] == "variable-groups" && parts[2] == "variables" && r.Method == "POST":
			var body struct {
				Secure    bool                `json:"secure"`
				Variables []map[string]string `json:"variables"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if f.vars[parts[1]] == nil {
				f.vars[parts[1]] = map[string]string{}
			}
			for _, v := range body.Variables {
				f.nextID++
				id := fmt.Sprintf("v%d", f.nextID)
				f.vars[parts[1]][id] = v["name"]
				f.values[id] = v["value"]
				f.secure[id] = body.Secure
			}
			w.WriteHeader(201)
		case len(parts) == 4 && parts[0] == "variable-groups" && r.Method == "PATCH":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.values[parts[3]], _ = body["value"].(string)
			f.secure[parts[3]] = body["secure"] == true
			w.WriteHeader(204)
		case len(parts) == 4 && parts[0] == "variable-groups" && r.Method == "DELETE":
			delete(f.vars[parts[1]], parts[3])
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return f, NewCodemagicSecretsAt(srv.URL, "app-1", "cm-token")
}

func writeJSONPage[T any](w http.ResponseWriter, all []T, page, size int) {
	total := (len(all) + size - 1) / size
	if total == 0 {
		total = 1
	}
	start := min((page-1)*size, len(all))
	end := min(start+size, len(all))
	_ = json.NewEncoder(w).Encode(map[string]any{"data": all[start:end], "current_page": page, "page_size": size, "total_pages": total})
}

func TestCodemagicSecrets(t *testing.T) {
	ctx := context.Background()
	f, s := newCodemagicFake(t)

	// No builder group yet: nothing listed, and set creates it.
	if names, err := s.List(ctx); err != nil || len(names) != 0 {
		t.Fatalf("empty list: %v %v", names, err)
	}
	for _, n := range []string{"SENTRY_TOKEN", "MAPS_KEY", "THIRD"} {
		if err := s.Set(ctx, n, "value of "+n); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.groups) != 2 {
		t.Fatalf("builder group not created exactly once: %v", f.groups)
	}
	names, err := s.List(ctx)
	if err != nil || !reflect.DeepEqual(names, []string{"MAPS_KEY", "SENTRY_TOKEN", "THIRD"}) {
		t.Fatalf("list across pages: %v %v", names, err)
	}
	for id, secure := range f.secure {
		if !secure {
			t.Errorf("variable %s not secure", id)
		}
	}

	// A second set updates in place.
	if err := s.Set(ctx, "SENTRY_TOKEN", "rotated"); err != nil {
		t.Fatal(err)
	}
	found := 0
	for id, v := range f.values {
		for _, vars := range f.vars {
			if vars[id] == "SENTRY_TOKEN" {
				found++
				if v != "rotated" {
					t.Errorf("value not updated: %q", v)
				}
			}
		}
	}
	if found != 1 {
		t.Fatalf("SENTRY_TOKEN stored %d times", found)
	}

	if ok, err := s.Delete(ctx, "MAPS_KEY"); !ok || err != nil {
		t.Fatalf("delete: %v %v", ok, err)
	}
	if ok, err := s.Delete(ctx, "MAPS_KEY"); ok || err != nil {
		t.Fatalf("second delete: %v %v", ok, err)
	}
	if names, _ := s.List(ctx); !reflect.DeepEqual(names, []string{"SENTRY_TOKEN", "THIRD"}) {
		t.Fatalf("after delete: %v", names)
	}
}

func TestCodemagicSecretErrorsHideValues(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		fmt.Fprint(w, `{"detail":"forbidden","status_code":403,"errors":[]}`)
	}))
	defer srv.Close()
	err := NewCodemagicSecretsAt(srv.URL, "app-1", "cm-token").Set(context.Background(), "A", "super-secret-value")
	if err == nil || strings.Contains(err.Error(), "super-secret-value") || strings.Contains(err.Error(), "cm-token") {
		t.Fatalf("error: %v", err)
	}
}

type bitriseFake struct {
	mu      sync.Mutex
	secrets map[string]map[string]any
	calls   []string
}

func newBitriseFake(t *testing.T) (*bitriseFake, *BitriseSecrets) {
	f := &bitriseFake{secrets: map[string]map[string]any{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Header.Get("Authorization") != "br-token" {
			t.Errorf("missing token on %s %s", r.Method, r.URL)
		}
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		name, isItem := strings.CutPrefix(r.URL.Path, "/apps/slug/secrets/")
		switch {
		case r.Method == "GET" && r.URL.Path == "/apps/slug/secrets":
			var list []map[string]any
			for n, s := range f.secrets {
				list = append(list, map[string]any{"name": n, "is_protected": s["is_protected"]})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"secrets": list})
		case r.Method == "POST" && r.URL.Path == "/apps/slug/secrets":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			name, _ := body["name"].(string)
			f.secrets[name] = body
			w.WriteHeader(201)
			fmt.Fprint(w, `{}`)
		case r.Method == "PATCH" && isItem:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			for k, v := range body {
				f.secrets[name][k] = v
			}
			fmt.Fprint(w, `{}`)
		case r.Method == "DELETE" && isItem:
			delete(f.secrets, name)
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return f, NewBitriseSecretsAt(srv.URL, "slug", "br-token")
}

func TestBitriseSecrets(t *testing.T) {
	ctx := context.Background()
	f, s := newBitriseFake(t)
	if err := s.Set(ctx, "SENTRY_TOKEN", "a$HOME"); err != nil {
		t.Fatal(err)
	}
	got := f.secrets["SENTRY_TOKEN"]
	if got["value"] != "a$HOME" || got["is_protected"] != true || got["expand_in_step_inputs"] != false || got["is_exposed_for_pull_requests"] != false {
		t.Fatalf("created with %v", got)
	}
	// An update sends only the value: a protected secret accepts nothing else.
	f.calls = nil
	if err := s.Set(ctx, "SENTRY_TOKEN", "rotated"); err != nil {
		t.Fatal(err)
	}
	if f.secrets["SENTRY_TOKEN"]["value"] != "rotated" || !reflect.DeepEqual(f.calls, []string{"GET /apps/slug/secrets", "PATCH /apps/slug/secrets/SENTRY_TOKEN"}) {
		t.Fatalf("update: %v %v", f.secrets, f.calls)
	}
	if names, err := s.List(ctx); err != nil || !reflect.DeepEqual(names, []string{"SENTRY_TOKEN"}) {
		t.Fatalf("list: %v %v", names, err)
	}
	if ok, err := s.Delete(ctx, "SENTRY_TOKEN"); !ok || err != nil || len(f.secrets) != 0 {
		t.Fatalf("delete: %v %v", ok, err)
	}
	if ok, err := s.Delete(ctx, "SENTRY_TOKEN"); ok || err != nil {
		t.Fatalf("delete missing: %v %v", ok, err)
	}
}
