package distribute

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MobAI-App/ios-builder/internal/asc"
)

func TestSubmitTestFlightExternalGroup(t *testing.T) {
	f := newFake(t)
	var log bytes.Buffer
	res, err := SubmitTestFlight(context.Background(), f.client(t), TestFlightOptions{
		BundleID: "com.example.app", Groups: []string{"team", "Beta Testers"}, Notes: "Try the new login", NoEncryption: true, Wait: true, PollInterval: time.Millisecond, Log: &log,
	})
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	if res.Build.ID != "build-9" || res.Compliance != "set_exempt" || len(res.Groups) != 2 || res.Groups[0].Name != "Team" || !res.Groups[0].Internal || res.Groups[1].Internal {
		t.Errorf("result = %+v", res)
	}
	if res.BetaReview == nil || res.BetaReview.ID != "bar-1" || res.BetaReview.State != "APPROVED" {
		t.Errorf("beta review = %+v", res.BetaReview)
	}
	// Notes go to the app's primary locale, which has no localization yet, so it is created.
	notes := f.body("POST /v1/betaBuildLocalizations")["data"].(map[string]any)
	if notes["attributes"].(map[string]any)["locale"] != "de-DE" || notes["attributes"].(map[string]any)["whatsNew"] != "Try the new login" {
		t.Errorf("localization body = %v", notes)
	}
	links := f.body("POST /v1/builds/build-9/relationships/betaGroups")["data"].([]any)
	if len(links) != 2 || links[1].(map[string]any)["id"] != "g-ext" {
		t.Errorf("group linkage = %v", links)
	}
	// Review must be requested before the build lands in the external group.
	var reviewAt, groupAt int
	for i, c := range f.calls {
		switch c {
		case "POST /v1/betaAppReviewSubmissions":
			reviewAt = i
		case "POST /v1/builds/build-9/relationships/betaGroups":
			groupAt = i
		}
	}
	if reviewAt == 0 || groupAt < reviewAt {
		t.Errorf("order: %v", f.calls)
	}
}

func TestSubmitTestFlightUpdatesExistingNotesAndSkipsReviewForInternal(t *testing.T) {
	f := newFake(t)
	yes := false
	f.buildEncryption = &yes
	res, err := SubmitTestFlight(context.Background(), f.client(t), TestFlightOptions{BundleID: "com.example.app", BuildNumber: "7", Groups: []string{"Team"}, Notes: "n", Locale: "en-US"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Compliance != "already_set" || res.BetaReview != nil || f.called("POST /v1/betaAppReviewSubmissions") || f.called("PATCH /v1/builds/build-9") {
		t.Errorf("result = %+v, calls = %v", res, f.calls)
	}
	if !f.called("PATCH /v1/betaBuildLocalizations/loc-en") || f.called("POST /v1/betaBuildLocalizations") {
		t.Errorf("existing locale must be updated: %v", f.calls)
	}
}

func TestSubmitTestFlightListsGroupsWithoutGroupFlag(t *testing.T) {
	f := newFake(t)
	res, err := SubmitTestFlight(context.Background(), f.client(t), TestFlightOptions{BundleID: "com.example.app"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.AvailableGroups) != 2 || len(res.Groups) != 0 || f.called("POST /v1/builds/build-9/relationships/betaGroups") {
		t.Errorf("result = %+v", res)
	}
}

func TestSubmitTestFlightErrors(t *testing.T) {
	f := newFake(t)
	c := f.client(t)
	_, err := SubmitTestFlight(context.Background(), c, TestFlightOptions{BundleID: "com.example.app", Groups: []string{"Nobody"}, NoEncryption: true})
	if err == nil || !strings.Contains(err.Error(), "Nobody") || !strings.Contains(err.Error(), "Beta Testers") {
		t.Errorf("unknown group: %v", err)
	}
	f.mu.Lock()
	f.buildEncryption = nil // the call above answered it
	f.mu.Unlock()
	_, err = SubmitTestFlight(context.Background(), c, TestFlightOptions{BundleID: "com.example.app", Groups: []string{"Team"}})
	if err == nil || !strings.Contains(err.Error(), "export compliance") {
		t.Errorf("missing compliance: %v", err)
	}
	f.buildState = "PROCESSING"
	_, err = SubmitTestFlight(context.Background(), c, TestFlightOptions{BundleID: "com.example.app", BuildNumber: "7"})
	if err == nil || !strings.Contains(err.Error(), "PROCESSING") {
		t.Errorf("processing build: %v", err)
	}
}

func TestSubmitAppStoreCreatesVersionAndSubmission(t *testing.T) {
	f := newFake(t)
	var log bytes.Buffer
	res, err := SubmitAppStore(context.Background(), f.client(t), AppStoreOptions{BundleID: "com.example.app", Version: "2.0.0", ReleaseType: asc.ReleaseTypeAfterApproval, NoEncryption: true, Log: &log})
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	if !res.Version.Created || res.Version.ID != "ver-1" || res.Version.ReleaseType != "AFTER_APPROVAL" || res.Submission.ID != "rs-1" || res.Submission.State != "WAITING_FOR_REVIEW" {
		t.Errorf("result = %+v", res)
	}
	create := f.body("POST /v1/appStoreVersions")["data"].(map[string]any)
	if create["attributes"].(map[string]any)["versionString"] != "2.0.0" || create["attributes"].(map[string]any)["platform"] != "IOS" || create["relationships"].(map[string]any)["app"].(map[string]any)["data"].(map[string]any)["id"] != "app-1" {
		t.Errorf("version create = %v", create)
	}
	upd := f.body("PATCH /v1/appStoreVersions/ver-1")["data"].(map[string]any)
	if upd["attributes"].(map[string]any)["releaseType"] != "AFTER_APPROVAL" || upd["relationships"].(map[string]any)["build"].(map[string]any)["data"].(map[string]any)["id"] != "build-9" {
		t.Errorf("version update = %v", upd)
	}
	item := f.body("POST /v1/reviewSubmissionItems")["data"].(map[string]any)["relationships"].(map[string]any)
	if item["reviewSubmission"].(map[string]any)["data"].(map[string]any)["id"] != "rs-1" || item["appStoreVersion"].(map[string]any)["data"].(map[string]any)["id"] != "ver-1" {
		t.Errorf("item = %v", item)
	}
	submit := f.body("PATCH /v1/reviewSubmissions/rs-1")["data"].(map[string]any)
	if submit["attributes"].(map[string]any)["submitted"] != true {
		t.Errorf("submit = %v", submit)
	}
}

func TestSubmitAppStoreReusesOpenSubmission(t *testing.T) {
	f := newFake(t)
	f.versionExists, f.openSubmission = true, true
	yes := true
	f.buildEncryption = &yes
	res, err := SubmitAppStore(context.Background(), f.client(t), AppStoreOptions{BundleID: "com.example.app", Version: "2.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Version.Created || res.Submission.ID != "rs-0" || f.called("POST /v1/appStoreVersions") || f.called("POST /v1/reviewSubmissions") || f.called("POST /v1/reviewSubmissionItems") {
		t.Errorf("result = %+v, calls = %v", res, f.calls)
	}
	if !f.called("PATCH /v1/reviewSubmissions/rs-0") {
		t.Errorf("not submitted: %v", f.calls)
	}
}

func TestSubmitAppStoreMetadataConflict(t *testing.T) {
	f := newFake(t)
	f.submitStatus = 409
	_, err := SubmitAppStore(context.Background(), f.client(t), AppStoreOptions{BundleID: "com.example.app", Version: "2.0.0", NoEncryption: true})
	var apiErr *asc.Error
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 409 {
		t.Fatalf("err = %v", err)
	}
	msg := err.Error()
	for _, want := range []string{"screenshot for iPhone", "metadata", "asc-cli"} {
		if !strings.Contains(msg, want) {
			t.Errorf("%q lacks %q", msg, want)
		}
	}
	if strings.Contains(msg, "\n") {
		t.Error("error spans lines")
	}
}

func TestSubmitAppStoreRefusesVersionInReview(t *testing.T) {
	f := newFake(t)
	f.versionExists, f.versionState = true, "IN_REVIEW"
	_, err := SubmitAppStore(context.Background(), f.client(t), AppStoreOptions{BundleID: "com.example.app", Version: "2.0.0", NoEncryption: true})
	if err == nil || !strings.Contains(err.Error(), "IN_REVIEW") {
		t.Errorf("err = %v", err)
	}
	if _, err := SubmitAppStore(context.Background(), f.client(t), AppStoreOptions{BundleID: "com.example.app"}); err == nil {
		t.Error("missing version accepted")
	}
}
