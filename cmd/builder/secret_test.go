package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/MobAI-App/ios-builder/internal/ci"
	"github.com/MobAI-App/ios-builder/internal/config"
	"github.com/MobAI-App/ios-builder/internal/github"
	"golang.org/x/crypto/nacl/box"
)

// providerFakes serves the three secrets APIs from one in-memory store per
// provider, holding what each was sent (GitHub values decrypted).
type providerFakes struct {
	mu     sync.Mutex
	stored map[string]map[string]string // provider -> name -> value
	flags  map[string]map[string]any    // bitrise name -> create body
	calls  int
}

func (f *providerFakes) put(provider, name, value string) {
	if f.stored[provider] == nil {
		f.stored[provider] = map[string]string{}
	}
	f.stored[provider][name] = value
}

func (f *providerFakes) names(provider string) []string {
	var out []string
	for n := range f.stored[provider] {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func newProviderFakes(t *testing.T) *providerFakes {
	t.Helper()
	f := &providerFakes{stored: map[string]map[string]string{}, flags: map[string]map[string]any{}}
	pub, priv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls++
		name, _ := strings.CutPrefix(r.URL.Path, "/repos/o/r/actions/secrets/")
		switch {
		case r.URL.Path == "/repos/o/r/actions/secrets/public-key":
			fmt.Fprintf(w, `{"key_id":"kid","key":%q}`, base64.StdEncoding.EncodeToString(pub[:]))
		case r.Method == "GET" && r.URL.Path == "/repos/o/r/actions/secrets":
			var list []map[string]string
			for _, n := range f.names("github") {
				list = append(list, map[string]string{"name": n})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"total_count": len(list), "secrets": list})
		case r.Method == "PUT":
			var body github.CreateSecretRequest
			_ = json.NewDecoder(r.Body).Decode(&body)
			sealed, _ := base64.StdEncoding.DecodeString(body.EncryptedValue)
			plain, ok := box.OpenAnonymous(nil, sealed, pub, priv)
			if !ok {
				t.Errorf("%s not sealed with the repository key", name)
			}
			f.put("github", name, string(plain))
			w.WriteHeader(http.StatusCreated)
		case r.Method == "DELETE":
			delete(f.stored["github"], name)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("GitHub: unexpected %s %s", r.Method, r.URL)
		}
	}))
	t.Cleanup(gh.Close)

	// Codemagic: one builder group "g1" that exists from the start; variable
	// IDs are the names.
	cm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls++
		if r.Header.Get("x-auth-token") != "cm-token" {
			t.Errorf("Codemagic: no token")
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/apps/cm-app/variable-groups":
			fmt.Fprint(w, `{"data":[{"id":"g1","name":"builder"}],"current_page":1,"page_size":100,"total_pages":1}`)
		case r.Method == "GET" && r.URL.Path == "/variable-groups/g1/variables":
			var data []map[string]any
			for _, n := range f.names("codemagic") {
				data = append(data, map[string]any{"id": n, "name": n, "value": nil, "secure": true})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "current_page": 1, "page_size": 100, "total_pages": 1})
		case r.Method == "POST" && r.URL.Path == "/variable-groups/g1/variables":
			var body struct {
				Secure    bool                `json:"secure"`
				Variables []map[string]string `json:"variables"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if !body.Secure {
				t.Errorf("Codemagic variable not secure")
			}
			for _, v := range body.Variables {
				f.put("codemagic", v["name"], v["value"])
			}
			w.WriteHeader(201)
		case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/variable-groups/g1/variables/"):
			delete(f.stored["codemagic"], strings.TrimPrefix(r.URL.Path, "/variable-groups/g1/variables/"))
			w.WriteHeader(204)
		default:
			t.Errorf("Codemagic: unexpected %s %s", r.Method, r.URL)
		}
	}))
	t.Cleanup(cm.Close)

	br := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls++
		if r.Header.Get("Authorization") != "br-token" {
			t.Errorf("Bitrise: no token")
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/apps/br-app/secrets":
			var list []map[string]string
			for _, n := range f.names("bitrise") {
				list = append(list, map[string]string{"name": n})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"secrets": list})
		case r.Method == "POST" && r.URL.Path == "/apps/br-app/secrets":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			name, _ := body["name"].(string)
			value, _ := body["value"].(string)
			f.put("bitrise", name, value)
			f.flags[name] = body
			w.WriteHeader(201)
			fmt.Fprint(w, `{}`)
		case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/apps/br-app/secrets/"):
			delete(f.stored["bitrise"], strings.TrimPrefix(r.URL.Path, "/apps/br-app/secrets/"))
			w.WriteHeader(204)
		default:
			t.Errorf("Bitrise: unexpected %s %s", r.Method, r.URL)
		}
	}))
	t.Cleanup(br.Close)

	prev := secretStoreFor
	secretStoreFor = func(cfg *config.Config, provider string) (ci.SecretStore, string, error) {
		switch provider {
		case "github":
			return githubSecrets{github.NewClientWithBaseURL("tok", gh.URL), cfg.GitHub.Owner, cfg.GitHub.Repo}, "GitHub (fake)", nil
		case "codemagic":
			return ci.NewCodemagicSecretsAt(cm.URL, cfg.Codemagic.AppID, "cm-token"), "Codemagic (fake)", nil
		case "bitrise":
			return ci.NewBitriseSecretsAt(br.URL, cfg.Bitrise.AppID, "br-token"), "Bitrise (fake)", nil
		}
		return nil, "", fmt.Errorf("unknown provider %q", provider)
	}
	t.Cleanup(func() { secretStoreFor = prev })
	return f
}

// runWithStdin is run with stdin set for the command.
func runWithStdin(t *testing.T, stdin string, args ...string) (string, string, error) {
	t.Helper()
	rootCmd.SetIn(strings.NewReader(stdin))
	defer rootCmd.SetIn(nil)
	return run(t, args...)
}

func TestSecretSetStoresOnTheProviderAndListsTheName(t *testing.T) {
	writeSecretProject(t)
	f := newProviderFakes(t)

	// Top level, GitHub (no provider configured anywhere).
	out, errOut, err := runWithStdin(t, "s3cret-top\n", "secret", "set", "SENTRY_TOKEN", "--value-stdin")
	if err != nil {
		t.Fatalf("%v\n%s", err, errOut)
	}
	if f.stored["github"]["SENTRY_TOKEN"] != "s3cret-top" {
		t.Fatalf("GitHub got %v", f.stored["github"])
	}
	if strings.Contains(out+errOut, "s3cret") {
		t.Fatalf("value echoed:\n%s%s", out, errOut)
	}
	if got := loadSaved(t).Secrets; !reflect.DeepEqual(got, []string{"SENTRY_TOKEN"}) {
		t.Fatalf("builder.json secrets = %v", got)
	}

	// A profile whose provider is Codemagic: stored with the profile suffix,
	// listed under the profile only.
	if _, errOut, err = runWithStdin(t, "s3cret-prod", "secret", "set", "SENTRY_TOKEN", "--profile", "production", "--value-stdin"); err != nil {
		t.Fatalf("%v\n%s", err, errOut)
	}
	if f.stored["codemagic"]["SENTRY_TOKEN__PRODUCTION"] != "s3cret-prod" || len(f.stored["github"]) != 1 {
		t.Fatalf("Codemagic got %v, GitHub %v", f.stored["codemagic"], f.stored["github"])
	}
	saved := loadSaved(t)
	if !reflect.DeepEqual(saved.Profiles["production"].Secrets, []string{"SENTRY_TOKEN"}) || saved.Profiles["production"].Distribution != "store" {
		t.Fatalf("production profile = %+v", saved.Profiles["production"])
	}

	// --provider beats the rest.
	if _, errOut, err = runWithStdin(t, "maps\r\n", "secret", "set", "MAPS_KEY", "--provider", "bitrise", "--value-stdin"); err != nil {
		t.Fatalf("%v\n%s", err, errOut)
	}
	if f.stored["bitrise"]["MAPS_KEY"] != "maps" || f.flags["MAPS_KEY"]["is_protected"] != true || f.flags["MAPS_KEY"]["expand_in_step_inputs"] != false {
		t.Fatalf("Bitrise got %v %v", f.stored["bitrise"], f.flags)
	}

	// list: names and where they are, never values.
	out, _, err = run(t, "secret", "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var entries []secretEntry
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	// MAPS_KEY sits at the top level, whose provider is GitHub: stored on
	// Bitrise only, so a GitHub build would miss it.
	want := []secretEntry{
		{Name: "MAPS_KEY", StoredAs: "MAPS_KEY", Provider: "github"},
		{Name: "SENTRY_TOKEN", StoredAs: "SENTRY_TOKEN", Provider: "github", Present: true},
		{Name: "SENTRY_TOKEN", Profile: "production", StoredAs: "SENTRY_TOKEN__PRODUCTION", Provider: "codemagic", Present: true},
	}
	if !reflect.DeepEqual(entries, want) {
		t.Fatalf("list:\n got %+v\nwant %+v", entries, want)
	}
	if strings.Contains(out, "s3cret") || strings.Contains(out, "maps\"") {
		t.Fatalf("list printed a value:\n%s", out)
	}
	out, _, err = run(t, "secret", "list", "--provider", "bitrise")
	if err != nil || !strings.Contains(out, "MAPS_KEY") || !strings.Contains(out, "missing") {
		t.Fatalf("table: %v\n%s", err, out)
	}

	// unset deletes the stored value and the name.
	if _, errOut, err = run(t, "secret", "unset", "SENTRY_TOKEN", "--profile", "production"); err != nil {
		t.Fatalf("%v\n%s", err, errOut)
	}
	if _, ok := f.stored["codemagic"]["SENTRY_TOKEN__PRODUCTION"]; ok || loadSaved(t).Profiles["production"].Secrets != nil {
		t.Fatalf("unset left %v / %+v", f.stored["codemagic"], loadSaved(t).Profiles["production"])
	}
	if _, _, err = run(t, "secret", "unset", "SENTRY_TOKEN", "--profile", "production"); err == nil {
		t.Fatal("second unset succeeded")
	}
	if _, _, err = run(t, "secret", "unset", "MAPS_KEY", "--provider", "bitrise"); err != nil || len(f.stored["bitrise"]) != 0 || len(loadSaved(t).Secrets) != 1 {
		t.Fatalf("bitrise unset: %v %v %v", err, f.stored["bitrise"], loadSaved(t).Secrets)
	}
}

func TestSecretSetRefusesBeforeAskingForTheValue(t *testing.T) {
	writeSecretProject(t)
	f := newProviderFakes(t)
	for name, args := range map[string][]string{
		"signing secret":  {"secret", "set", "IOS_CERTIFICATE_STORE", "--value-stdin"},
		"MobAI key":       {"secret", "set", "MOBAI_API_KEY", "--value-stdin"},
		"lower case":      {"secret", "set", "sentry_token", "--value-stdin"},
		"shadows env":     {"secret", "set", "API_URL", "--value-stdin"},
		"unknown profile": {"secret", "set", "A", "--profile", "nightly", "--value-stdin"},
		"value as arg":    {"secret", "set", "A", "value"},
		"empty value":     {"secret", "set", "EMPTY", "--value-stdin"},
	} {
		stdin := "v"
		if name == "empty value" {
			stdin = "\n"
		}
		if _, _, err := runWithStdin(t, stdin, args...); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// No terminal and no --value-stdin: the hidden prompt says how to pipe.
	_, _, err := runWithStdin(t, "v", "secret", "set", "A")
	if err == nil || !strings.Contains(err.Error(), "--value-stdin") {
		t.Fatalf("non-terminal stdin: %v", err)
	}
	if len(f.stored) != 0 {
		t.Fatalf("something reached a provider: %v", f.stored)
	}
	if cfg := loadSaved(t); cfg.Secrets != nil {
		t.Fatalf("builder.json changed: %v", cfg.Secrets)
	}
}
