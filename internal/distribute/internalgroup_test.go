package distribute

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestToInternalGroupUploadsWaitsAndAdds(t *testing.T) {
	f := newFake(t)
	var log bytes.Buffer
	res, err := ToInternalGroup(context.Background(), f.client(t), &InternalGroupOptions{IPAPath: writeIPA(t, plistExempt), Group: "team", Notes: "try it", PollInterval: time.Millisecond, Log: &log})
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	if res.Upload == nil || res.Upload.Build == nil || res.Upload.Build.ID != "build-9" {
		t.Errorf("upload = %+v", res.Upload)
	}
	tf := res.TestFlight
	if tf == nil || len(tf.Groups) != 1 || tf.Groups[0].ID != "g-int" || !tf.Groups[0].Internal || tf.BetaReview != nil || tf.Notes != "try it" {
		t.Fatalf("testflight = %+v", tf)
	}
	links := arr(t, f.body("POST /v1/builds/build-9/relationships/betaGroups"), "data")
	if len(links) != 1 || obj(t, links[0])["id"] != "g-int" {
		t.Errorf("linkage = %v", links)
	}
	if f.called("POST /v1/betaAppReviewSubmissions") {
		t.Error("an internal group needs no beta review")
	}
}

func TestToInternalGroupCreatesMissingGroupInternal(t *testing.T) {
	f := newFake(t)
	res, err := ToInternalGroup(context.Background(), f.client(t), &InternalGroupOptions{IPAPath: writeIPA(t, plistExempt), Group: "Phones", PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if attrs := obj(t, f.body("POST /v1/betaGroups"), "data", "attributes"); attrs["isInternalGroup"] != true || !res.TestFlight.Groups[0].Created {
		t.Errorf("attrs = %v, groups = %+v", attrs, res.TestFlight.Groups)
	}
}

// An external group is refused before the IPA goes anywhere.
func TestToInternalGroupRefusesExternalBeforeUpload(t *testing.T) {
	f := newFake(t)
	_, err := ToInternalGroup(context.Background(), f.client(t), &InternalGroupOptions{IPAPath: writeIPA(t, plistExempt), Group: "Beta Testers", PollInterval: time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "is external") {
		t.Fatalf("err = %v", err)
	}
	if f.called("POST /v1/buildUploads") {
		t.Errorf("uploaded anyway: %v", f.calls)
	}
}

// SubmitTestFlight with Internal refuses an external group before compliance,
// notes or group changes, and creates missing groups internal even with External.
func TestSubmitTestFlightInternalOnly(t *testing.T) {
	f := newFake(t)
	_, err := SubmitTestFlight(context.Background(), f.client(t), &TestFlightOptions{BundleID: "com.example.app", Groups: []string{"Team", "beta testers"}, Internal: true, Notes: "n", NoEncryption: true})
	if err == nil || !strings.Contains(err.Error(), "Beta Testers is external") {
		t.Fatalf("err = %v", err)
	}
	for _, c := range f.calls {
		if !strings.HasPrefix(c, "GET ") {
			t.Errorf("changed something before refusing: %s", c)
		}
	}
	f = newFake(t)
	res, err := SubmitTestFlight(context.Background(), f.client(t), &TestFlightOptions{BundleID: "com.example.app", Groups: []string{"New"}, Internal: true, External: true, NoEncryption: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Groups[0].Internal || res.BetaReview != nil {
		t.Errorf("groups = %+v", res.Groups)
	}
}
