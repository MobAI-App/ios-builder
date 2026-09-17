package github

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// failedRunServer is a run that completed with conclusion failure, whose
// build job failed in "Build IPA" and left one error and one warning
// annotation; annotationsStatus is what the annotations endpoint answers.
func failedRunServer(t *testing.T, annotationsStatus int) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/o/r/actions/runs/7/artifacts":
			fmt.Fprint(w, `{"total_count":0,"artifacts":[]}`)
		case "/repos/o/r/actions/runs/7":
			fmt.Fprint(w, `{"id":7,"status":"completed","conclusion":"failure","html_url":"https://github.com/o/r/actions/runs/7"}`)
		case "/repos/o/r/actions/runs/7/jobs":
			fmt.Fprint(w, `{"total_count":1,"jobs":[{"id":99,"name":"build","status":"completed","conclusion":"failure","steps":[
				{"name":"Checkout","status":"completed","conclusion":"success","number":1},
				{"name":"Build IPA","status":"completed","conclusion":"failure","number":2},
				{"name":"Upload IPA","status":"completed","conclusion":"skipped","number":3}]}]}`)
		case "/repos/o/r/check-runs/99/annotations":
			if annotationsStatus != http.StatusOK {
				w.WriteHeader(annotationsStatus)
				fmt.Fprint(w, `{"message":"Not Found"}`)
				return
			}
			fmt.Fprint(w, `[
				{"path":".github","start_line":1,"annotation_level":"warning","title":"","message":"Node.js 16 actions are deprecated"},
				{"path":".github","start_line":1,"annotation_level":"failure","title":"","message":"No profile for team 'ABC' matching 'Builder store com.example.app' found"},
				{"path":".github","start_line":1,"annotation_level":"failure","title":"","message":"Process completed with exit code 65."}]`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	c := NewClient("tok")
	c.baseURL = srv.URL
	return c
}

func TestPollForArtifactReportsTheFailedStepAndErrors(t *testing.T) {
	c := failedRunServer(t, http.StatusOK)
	_, err := c.PollForArtifact(context.Background(), "o", "r", 7, "ipa", time.Minute, nil)
	if err == nil {
		t.Fatal("a failed run must end the wait")
	}
	want := "workflow failed with conclusion: failure\n" +
		"   Failed step: Build IPA (job build)\n" +
		"   No profile for team 'ABC' matching 'Builder store com.example.app' found\n" +
		"   Process completed with exit code 65."
	if err.Error() != want {
		t.Errorf("error:\n%v\nwant:\n%s", err, want)
	}
	if strings.Contains(err.Error(), "deprecated") {
		t.Errorf("warnings must not be listed: %v", err)
	}
}

func TestPollForArtifactWithoutAnnotations(t *testing.T) {
	// The annotations endpoint failing (a token without checks:read, an old
	// GHES) still leaves the conclusion and the step.
	c := failedRunServer(t, http.StatusNotFound)
	_, err := c.PollForArtifact(context.Background(), "o", "r", 7, "ipa", time.Minute, nil)
	if err == nil || err.Error() != "workflow failed with conclusion: failure\n   Failed step: Build IPA (job build)" {
		t.Errorf("error: %v", err)
	}
}
