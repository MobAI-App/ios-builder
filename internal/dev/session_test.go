package dev

import (
	"testing"

	"github.com/MobAI-App/ios-builder/internal/mobai"
)

func TestGuessBundleID(t *testing.T) {
	response := func(teamID string) *mobai.InstallAppResponse {
		resp := &mobai.InstallAppResponse{}
		resp.Data.TeamID = teamID
		return resp
	}

	tests := []struct {
		name     string
		resp     *mobai.InstallAppResponse
		ipaID    string
		resigned bool
		want     string
	}{
		{"as-is install keeps IPA ID", response(""), "com.example.app", false, "com.example.app"},
		{"as-is install ignores team ID", response("TEAM"), "com.example.app", false, "com.example.app"},
		{"re-sign appends team ID", response("TEAM"), "com.example.app", true, "com.example.app.TEAM"},
		{"re-sign without team ID keeps IPA ID", response(""), "com.example.app", true, "com.example.app"},
		{"unreadable IPA has no guess", response("TEAM"), "", true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := guessBundleID(tt.resp, tt.ipaID, tt.resigned); got != tt.want {
				t.Errorf("guessBundleID = %q, want %q", got, tt.want)
			}
		})
	}
}
