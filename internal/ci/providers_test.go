package ci

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/MobAI-App/ios-builder/internal/config"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(r *http.Request, code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}
}

func TestCodemagicContract(t *testing.T) {
	c := NewCodemagic(config.CIConfig{AppID: "app", Branch: "release"}, "secret")
	c.api.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("x-auth-token") != "secret" {
			t.Fatal("missing API auth")
		}
		switch r.Method + " " + r.URL.Path {
		case "POST /builds":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["branch"] != "release" || body["instanceType"] != "mac_mini_m2" || body["workflowId"] != "ios-build" {
				t.Fatalf("bad dispatch: %v", body)
			}
			environment, ok := body["environment"].(map[string]any)
			if !ok {
				t.Fatal("missing environment object")
			}
			env, ok := environment["variables"].(map[string]any)
			if !ok {
				t.Fatal("missing variables object")
			}
			if env["SNAPSHOT_SHA"] != "sha" {
				t.Fatal("missing snapshot SHA")
			}
			return response(r, 200, `{"buildId":"run"}`), nil
		case "GET /api/v3/builds/run":
			return response(r, 200, `{"data":{"status":"finished","artifacts":[{"name":"test.ipa","size_in_bytes":5,"short_lived_download_url":"https://storage.example/test.ipa?signature=private"}]}}`), nil
		case "POST /builds/run/cancel":
			return response(r, 208, ""), nil
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL)
			return nil, nil
		}
	})
	run, err := c.Start(context.Background(), Request{Workflow: "ios-build", Variables: map[string]string{"SNAPSHOT_SHA": "sha"}})
	if err != nil || run.ID != "run" {
		t.Fatalf("start: %+v %v", run, err)
	}
	s, err := c.Status(context.Background(), run)
	if err != nil || !s.Done || !s.Success || len(s.Artifacts) != 1 || s.Artifacts[0].Size != 5 {
		t.Fatalf("status: %+v %v", s, err)
	}
	if err := c.Cancel(context.Background(), run); err != nil {
		t.Fatal(err)
	}
}

func TestCodemagicStates(t *testing.T) {
	for _, tt := range []struct {
		state              string
		done, success, bad bool
	}{
		{"queued", false, false, false}, {"building", false, false, false}, {"finished", true, true, false},
		{"failed", true, false, false}, {"canceled", true, false, false}, {"timeout", true, false, false}, {"skipped", true, false, false}, {"", false, false, true}, {"success", false, false, true},
	} {
		t.Run(tt.state, func(t *testing.T) {
			c := NewCodemagic(config.CIConfig{}, "token")
			c.api.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
				return response(r, 200, `{"data":{"status":"`+tt.state+`"}}`), nil
			})
			s, err := c.Status(context.Background(), Run{ID: "run"})
			if (err != nil) != tt.bad || s.Done != tt.done || s.Success != tt.success {
				t.Fatalf("%+v %v", s, err)
			}
		})
	}
}

func TestBitriseTriggerFormatsAndLiteralInputs(t *testing.T) {
	for _, body := range []string{`{"build_slug":"run","status":"ok"}`, `{"results":[{"build_slug":"run","status":"ok"}]}`} {
		b := NewBitrise(config.CIConfig{AppID: "app", Branch: "main"}, "secret")
		b.api.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
			if r.Header.Get("Authorization") != "secret" || r.URL.Path != "/v0.1/apps/app/builds" {
				t.Fatal("wrong request")
			}
			var data struct {
				Params struct {
					Branch  string `json:"branch"`
					Machine string `json:"machine_type_id"`
					Envs    []struct {
						Key    string `json:"mapped_to"`
						Value  string `json:"value"`
						Expand *bool  `json:"is_expand"`
					} `json:"environments"`
				} `json:"build_params"`
			}
			if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
				t.Fatal(err)
			}
			if data.Params.Branch != "main" || data.Params.Machine != "g2.mac.medium" || len(data.Params.Envs) != 1 || data.Params.Envs[0].Value != "$HOME $(touch bad)" || data.Params.Envs[0].Expand == nil || *data.Params.Envs[0].Expand {
				t.Fatalf("expanded or lost inputs: %+v", data)
			}
			return response(r, 201, body), nil
		})
		run, err := b.Start(context.Background(), Request{Workflow: "ios-build", Variables: map[string]string{"SCHEME": "$HOME $(touch bad)"}})
		if err != nil || run.ID != "run" {
			t.Fatalf("%+v %v", run, err)
		}
	}
}

func TestBitriseArtifactsPaginationAndDownload(t *testing.T) {
	b := NewBitrise(config.CIConfig{AppID: "app"}, "secret")
	calls := 0
	b.api.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host == "storage.example" {
			if r.Header.Get("Authorization") != "" || r.Header.Get("x-auth-token") != "" {
				t.Fatal("credential leaked to storage")
			}
			return response(r, 200, "ipa bytes"), nil
		}
		if r.Header.Get("Authorization") != "secret" {
			t.Fatal("missing API token")
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/artifacts/artifact"):
			return response(r, 200, `{"data":{"expiring_download_url":"https://storage.example/file"}}`), nil
		case strings.HasSuffix(r.URL.Path, "/artifacts"):
			if r.URL.Query().Get("next") == "cursor" {
				return response(r, 200, `{"data":[{"slug":"artifact","title":"app.ipa","file_size_bytes":9}],"paging":{}}`), nil
			}
			return response(r, 200, `{"data":[{"slug":"log","title":"log.txt"}],"paging":{"next":"cursor"}}`), nil
		default:
			return response(r, 200, `{"data":{"status":1,"status_text":"success"}}`), nil
		}
	})
	run := Run{ID: "run"}
	s, err := b.Status(context.Background(), run)
	if err != nil || !s.Success || len(s.Artifacts) != 2 {
		t.Fatalf("%+v %v", s, err)
	}
	var out strings.Builder
	n, err := b.Download(context.Background(), run, s.Artifacts[1], &out)
	if err != nil || n != 9 || out.String() != "ipa bytes" || calls != 5 {
		t.Fatalf("download: %d %q %d %v", n, out.String(), calls, err)
	}
}

func TestBitriseAbortStates(t *testing.T) {
	for _, state := range []string{"0", "1", "2", "3", "4", "null", "5"} {
		t.Run(state, func(t *testing.T) {
			b := NewBitrise(config.CIConfig{AppID: "app"}, "secret")
			b.api.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
				if strings.HasSuffix(r.URL.Path, "/artifacts") {
					return response(r, 200, `{"data":[]}`), nil
				}
				return response(r, 200, `{"data":{"status":`+state+`}}`), nil
			})
			s, err := b.Status(context.Background(), Run{ID: "run"})
			if state == "null" || state == "5" {
				if err == nil {
					t.Fatal("accepted unknown status")
				}
				return
			}
			if err != nil || s.Done != (state != "0") || s.Success != (state == "1") {
				t.Fatalf("%+v %v", s, err)
			}
		})
	}
}

func TestHTTPDoesNotLeakErrorsOrFollowAPIRedirects(t *testing.T) {
	for _, code := range []int{302, 401, 403, 429, 500} {
		a := newAPI("private-token", "x-auth-token")
		calls := 0
		a.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			resp := response(r, code, "private-token private-body")
			resp.Header.Set("Location", "https://untrusted.example")
			return resp, nil
		})
		err := a.request(context.Background(), "POST", "https://api.example/builds", nil, nil)
		if err == nil || strings.Contains(err.Error(), "private") || calls != 1 {
			t.Fatalf("unsafe error/redirect: %v, %d", err, calls)
		}
	}
}

func TestArtifactRedirectsRemainUnauthenticated(t *testing.T) {
	transport := transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("x-auth-token") != "" {
			t.Fatal("auth leaked")
		}
		if r.URL.Host == "first.example" {
			resp := response(r, 302, "")
			resp.Header.Set("Location", "https://second.example/file")
			return resp, nil
		}
		return response(r, 200, "contents"), nil
	})
	n, err := downloadURL(context.Background(), "https://first.example/file", io.Discard, transport)
	if err != nil || n != 8 {
		t.Fatalf("%d %v", n, err)
	}
	for _, url := range []string{"http://first.example/file", "https://user:pass@first.example/file", "not a url"} {
		if _, err := downloadURL(context.Background(), url, io.Discard, transport); err == nil {
			t.Fatal("accepted unsafe URL")
		}
	}
}
