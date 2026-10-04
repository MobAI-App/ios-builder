package main

import (
	"encoding/json"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/MobAI-App/ios-builder/internal/build"
)

// jsonKeys marshals v and returns its top-level keys, sorted.
func jsonKeys(t *testing.T, v any) []string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return slices.Sorted(maps.Keys(m))
}

// The documented shapes (README, "Using Builder from agents and CI"). A
// change here is a change to what scripts parse.
func TestBuildJSONShape(t *testing.T) {
	res := newBuildJSON(&build.BuildResult{BuildID: "ab12cd34", IPAPath: "dist/App-ab12cd34.ipa", IPASize: 42, WorkflowURL: "https://github.com/o/r/actions/runs/1", Duration: 61500 * time.Millisecond}, "github", "development")
	want := []string{"build_id", "duration_seconds", "ipa", "ipa_size", "profile", "provider", "workflow_url"}
	if got := jsonKeys(t, res); !slices.Equal(got, want) {
		t.Errorf("ios build --json keys = %v, want %v", got, want)
	}
	if res.Duration != 62 {
		t.Errorf("duration_seconds = %v, want 62", res.Duration)
	}
}

func TestShareJSONShape(t *testing.T) {
	want := []string{"build_id", "cancel_command", "provider", "ready", "run_id", "submitted", "workflow_url"}
	got := jsonKeys(t, shareJSON{BuildID: "b", Provider: "codemagic", WorkflowURL: "u", RunID: "r", Submitted: true, CancelCommand: "c"})
	if !slices.Equal(got, want) {
		t.Errorf("ios share --json keys = %v, want %v", got, want)
	}
}

func TestInitJSONShape(t *testing.T) {
	want := []string{"committed", "files", "ios_path", "project", "pushed", "repository"}
	if got := jsonKeys(t, initResult{Files: []string{}}); !slices.Equal(got, want) {
		t.Errorf("init --json keys = %v, want %v", got, want)
	}
}
