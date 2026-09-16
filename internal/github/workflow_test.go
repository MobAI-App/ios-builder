package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// pollServer fakes the two endpoints PollForArtifact hits: the artifact list,
// empty until artifactAfter run-status calls have happened, and the run status,
// which answers 503 for the first statusErrors calls.
func pollServer(t *testing.T, statusErrors int, artifactAfter int) (*Client, *int32) {
	t.Helper()
	var statusCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/artifacts"):
			if artifactAfter >= 0 && int(atomic.LoadInt32(&statusCalls)) >= artifactAfter {
				w.Write([]byte(`{"artifacts":[{"id":7,"name":"ipa"}]}`))
				return
			}
			w.Write([]byte(`{"artifacts":[]}`))
		default:
			n := atomic.AddInt32(&statusCalls, 1)
			if int(n) <= statusErrors {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.Write([]byte(`{"id":1,"status":"in_progress","conclusion":""}`))
		}
	}))
	t.Cleanup(srv.Close)
	c := NewClient("token")
	c.baseURL = srv.URL
	return c, &statusCalls
}

func TestPollForArtifactSurvivesTransientStatusErrors(t *testing.T) {
	old := artifactPollInterval
	artifactPollInterval = time.Millisecond
	t.Cleanup(func() { artifactPollInterval = old })

	// Two 503s in a row, then the artifact appears: the poll must not give up.
	c, calls := pollServer(t, 2, 3)
	artifact, err := c.PollForArtifact(context.Background(), "o", "r", 1, "ipa", time.Second, nil)
	if err != nil {
		t.Fatalf("poll gave up on a transient error: %v", err)
	}
	if artifact.ID != 7 || atomic.LoadInt32(calls) < 3 {
		t.Fatalf("artifact %+v after %d status calls", artifact, *calls)
	}
}

func TestPollForArtifactGivesUpWhenStatusKeepsFailing(t *testing.T) {
	old := artifactPollInterval
	artifactPollInterval = time.Millisecond
	t.Cleanup(func() { artifactPollInterval = old })

	c, calls := pollServer(t, 1000, -1)
	_, err := c.PollForArtifact(context.Background(), "o", "r", 1, "ipa", time.Second, nil)
	if err == nil || !strings.Contains(err.Error(), "failed to check workflow status") {
		t.Fatalf("expected a status error, got %v", err)
	}
	if n := atomic.LoadInt32(calls); n != maxConsecutiveStatusErrors {
		t.Fatalf("gave up after %d status calls, want %d", n, maxConsecutiveStatusErrors)
	}
}
