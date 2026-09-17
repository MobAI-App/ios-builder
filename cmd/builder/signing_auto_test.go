package main

import (
	"testing"

	"github.com/MobAI-App/ios-builder/internal/mobai"
)

func TestMobaiSigningDevicesKeepsPhysicalIOSOnly(t *testing.T) {
	connected := []mobai.Device{
		{ID: "00008030-000A1B2C3D4E5F60", Name: "Jane's iPhone", Platform: "ios"},
		{ID: "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678", Name: "Old iPad", Platform: "iOS"},
		{ID: "86906E11-6B70-499D-8257-16C95EE2BAF5", Name: "Simulator", Platform: "ios", Virtual: true},
		{ID: "awsdevicefarm:Apple_iPhone_16:26.0", Name: "Farm iPhone", Platform: "ios", Cloud: true},
		{ID: "R58M12345AB", Name: "Pixel", Platform: "android"},
		{ID: "not-a-udid", Name: "Unknown", Platform: "ios"},
	}
	got := mobaiSigningDevices(connected)
	if len(got) != 2 || got[0].UDID != "00008030-000A1B2C3D4E5F60" || got[0].Name != "Jane's iPhone" || got[1].UDID != connected[1].ID {
		t.Errorf("mobaiSigningDevices = %+v", got)
	}
}

func TestUDIDRe(t *testing.T) {
	for udid, want := range map[string]bool{
		"00008030-000A1B2C3D4E5F60":                true,
		"00008030-000a1b2c3d4e5f60":                true,
		"a1b2c3d4e5f60718293a4b5c6d7e8f9012345678": true,
		"00008030-000A1B2C3D4E5F6":                 false,
		"browserstack:iPhone_14:26":                false,
		"":                                         false,
	} {
		if got := udidRe.MatchString(udid); got != want {
			t.Errorf("udidRe(%q) = %v, want %v", udid, got, want)
		}
	}
}
