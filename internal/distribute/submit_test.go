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
	res, err := SubmitTestFlight(context.Background(), f.client(t), &TestFlightOptions{
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
	notes := obj(t, f.body("POST /v1/betaBuildLocalizations"), "data", "attributes")
	if notes["locale"] != "de-DE" || notes["whatsNew"] != "Try the new login" {
		t.Errorf("localization body = %v", notes)
	}
	links := arr(t, f.body("POST /v1/builds/build-9/relationships/betaGroups"), "data")
	if len(links) != 2 || obj(t, links[1])["id"] != "g-ext" {
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
	res, err := SubmitTestFlight(context.Background(), f.client(t), &TestFlightOptions{BundleID: "com.example.app", BuildNumber: "7", Groups: []string{"Team"}, Notes: "n", Locale: "en-US"})
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
	var log bytes.Buffer
	res, err := SubmitTestFlight(context.Background(), f.client(t), &TestFlightOptions{BundleID: "com.example.app", Log: &log})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.AvailableGroups) != 2 || len(res.Groups) != 0 || f.called("POST /v1/builds/build-9/relationships/betaGroups") {
		t.Errorf("result = %+v", res)
	}
	if !strings.Contains(log.String(), "Available groups:\n  Team (internal)\n  Beta Testers (external)") {
		t.Errorf("log = %q", log.String())
	}
	f.noGroups = true
	log.Reset()
	if _, err := SubmitTestFlight(context.Background(), f.client(t), &TestFlightOptions{BundleID: "com.example.app", Log: &log}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log.String(), "Available groups: (none)") {
		t.Errorf("log = %q", log.String())
	}
}

func TestSubmitTestFlightCreatesMissingGroup(t *testing.T) {
	f := newFake(t)
	var log bytes.Buffer
	res, err := SubmitTestFlight(context.Background(), f.client(t), &TestFlightOptions{BundleID: "com.example.app", Groups: []string{"Nightly", "team"}, NoEncryption: true, Log: &log})
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	if len(res.Groups) != 2 || !res.Groups[0].Created || !res.Groups[0].Internal || res.Groups[0].ID != "g-new-1" || res.Groups[1].Created || res.Groups[1].ID != "g-int" {
		t.Errorf("groups = %+v", res.Groups)
	}
	create := obj(t, f.body("POST /v1/betaGroups"), "data")
	attrs := obj(t, create, "attributes")
	if attrs["name"] != "Nightly" || attrs["isInternalGroup"] != true || attrs["hasAccessToAllBuilds"] != false || obj(t, create, "relationships", "app", "data")["id"] != "app-1" {
		t.Errorf("create body = %v", create)
	}
	if !strings.Contains(log.String(), "Created TestFlight group Nightly (internal)") {
		t.Errorf("log = %q", log.String())
	}
	links := arr(t, f.body("POST /v1/builds/build-9/relationships/betaGroups"), "data")
	if len(links) != 2 || obj(t, links[0])["id"] != "g-new-1" || obj(t, links[1])["id"] != "g-int" {
		t.Errorf("linkage = %v", links)
	}
	if f.called("POST /v1/betaAppReviewSubmissions") {
		t.Error("internal groups need no beta review")
	}

	// --external creates an external group, which goes through beta review.
	f = newFake(t)
	res, err = SubmitTestFlight(context.Background(), f.client(t), &TestFlightOptions{BundleID: "com.example.app", Groups: []string{"Public"}, External: true, NoEncryption: true})
	if err != nil {
		t.Fatal(err)
	}
	attrs = obj(t, f.body("POST /v1/betaGroups"), "data", "attributes")
	if attrs["isInternalGroup"] != false || res.Groups[0].Internal || res.BetaReview == nil || !f.called("POST /v1/betaAppReviewSubmissions") {
		t.Errorf("attrs = %v, result = %+v", attrs, res)
	}
	if _, has := attrs["hasAccessToAllBuilds"]; has {
		t.Errorf("external groups take no hasAccessToAllBuilds: %v", attrs)
	}
}

func TestSubmitTestFlightSkipsAutomaticDistributionGroups(t *testing.T) {
	f := newFake(t)
	f.autoGroup = true
	var log bytes.Buffer
	res, err := SubmitTestFlight(context.Background(), f.client(t), &TestFlightOptions{BundleID: "com.example.app", Groups: []string{"Everyone"}, NoEncryption: true, Log: &log})
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	if len(res.Groups) != 1 || !res.Groups[0].AutoBuilds || f.called("POST /v1/builds/build-9/relationships/betaGroups") {
		t.Errorf("result = %+v, calls = %v (adding to such a group is a 422)", res, f.calls)
	}
	if !strings.Contains(log.String(), "Everyone is an internal group with automatic distribution: every processed build is already available to its testers") {
		t.Errorf("log = %q", log.String())
	}

	// Mixed with a manual group, only the manual one is linked.
	f = newFake(t)
	f.autoGroup = true
	res, err = SubmitTestFlight(context.Background(), f.client(t), &TestFlightOptions{BundleID: "com.example.app", Groups: []string{"Everyone", "Team"}, NoEncryption: true})
	if err != nil {
		t.Fatal(err)
	}
	links := arr(t, f.body("POST /v1/builds/build-9/relationships/betaGroups"), "data")
	if len(links) != 1 || obj(t, links[0])["id"] != "g-int" || len(res.Groups) != 2 || res.Groups[1].AutoBuilds {
		t.Errorf("linkage = %v, groups = %+v", links, res.Groups)
	}
}

func TestSubmitTestFlightErrors(t *testing.T) {
	f := newFake(t)
	c := f.client(t)
	_, err := SubmitTestFlight(context.Background(), c, &TestFlightOptions{BundleID: "com.example.app", Groups: []string{"Team"}})
	if err == nil || !strings.Contains(err.Error(), "export compliance") {
		t.Errorf("missing compliance: %v", err)
	}
	f.dupGroup = true
	_, err = SubmitTestFlight(context.Background(), c, &TestFlightOptions{BundleID: "com.example.app", Groups: []string{"BETA TESTERS"}, NoEncryption: true})
	if err == nil || !strings.Contains(err.Error(), "2 TestFlight groups match BETA TESTERS") || f.called("POST /v1/betaGroups") || f.called("POST /v1/builds/build-9/relationships/betaGroups") {
		t.Errorf("an ambiguous group name must neither create nor add: %v, calls = %v", err, f.calls)
	}
	f.dupGroup = false
	f.buildState = "PROCESSING"
	_, err = SubmitTestFlight(context.Background(), c, &TestFlightOptions{BundleID: "com.example.app", BuildNumber: "7"})
	if err == nil || !strings.Contains(err.Error(), "PROCESSING") {
		t.Errorf("processing build: %v", err)
	}
}

func TestSubmitAppStoreCreatesVersionAndSubmission(t *testing.T) {
	f := newFake(t)
	var log bytes.Buffer
	res, err := SubmitAppStore(context.Background(), f.client(t), &AppStoreOptions{BundleID: "com.example.app", Version: "2.0.0", ReleaseType: asc.ReleaseTypeAfterApproval, NoEncryption: true, Log: &log})
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	if !res.Version.Created || res.Version.ID != "ver-1" || res.Version.ReleaseType != "AFTER_APPROVAL" || res.Submission.ID != "rs-1" || res.Submission.State != "WAITING_FOR_REVIEW" {
		t.Errorf("result = %+v", res)
	}
	create := obj(t, f.body("POST /v1/appStoreVersions"), "data")
	if attrs := obj(t, create, "attributes"); attrs["versionString"] != "2.0.0" || attrs["platform"] != "IOS" || obj(t, create, "relationships", "app", "data")["id"] != "app-1" {
		t.Errorf("version create = %v", create)
	}
	upd := obj(t, f.body("PATCH /v1/appStoreVersions/ver-1"), "data")
	if obj(t, upd, "attributes")["releaseType"] != "AFTER_APPROVAL" || obj(t, upd, "relationships", "build", "data")["id"] != "build-9" {
		t.Errorf("version update = %v", upd)
	}
	item := obj(t, f.body("POST /v1/reviewSubmissionItems"), "data", "relationships")
	if obj(t, item, "reviewSubmission", "data")["id"] != "rs-1" || obj(t, item, "appStoreVersion", "data")["id"] != "ver-1" {
		t.Errorf("item = %v", item)
	}
	submit := f.body("PATCH /v1/reviewSubmissions/rs-1")
	if obj(t, submit, "data", "attributes")["submitted"] != true {
		t.Errorf("submit = %v", submit)
	}
}

func TestSubmitAppStoreReusesOpenSubmission(t *testing.T) {
	f := newFake(t)
	f.versionExists, f.openSubmission = true, true
	yes := true
	f.buildEncryption = &yes
	res, err := SubmitAppStore(context.Background(), f.client(t), &AppStoreOptions{BundleID: "com.example.app", Version: "2.0.0"})
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
	_, err := SubmitAppStore(context.Background(), f.client(t), &AppStoreOptions{BundleID: "com.example.app", Version: "2.0.0", NoEncryption: true})
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
	_, err := SubmitAppStore(context.Background(), f.client(t), &AppStoreOptions{BundleID: "com.example.app", Version: "2.0.0", NoEncryption: true})
	if err == nil || !strings.Contains(err.Error(), "IN_REVIEW") {
		t.Errorf("err = %v", err)
	}
	if _, err := SubmitAppStore(context.Background(), f.client(t), &AppStoreOptions{BundleID: "com.example.app"}); err == nil {
		t.Error("missing version accepted")
	}
}
