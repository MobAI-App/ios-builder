package main

import "testing"

func TestParseReleaseType(t *testing.T) {
	for flag, want := range map[string]string{"": "", "manual": "MANUAL", "after-approval": "AFTER_APPROVAL"} {
		if got, err := parseReleaseType(flag); err != nil || got != want {
			t.Errorf("%q: %q %v", flag, got, err)
		}
	}
	if _, err := parseReleaseType("scheduled"); err == nil {
		t.Error("unknown release type accepted")
	}
}
