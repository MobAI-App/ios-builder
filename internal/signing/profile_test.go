package signing

import (
	"strings"
	"testing"
)

// mobileprovision wraps a plist body in bytes that stand in for the CMS
// signature around the real thing: binary before, binary after, and a
// "<?xml" decoy inside the wrapper so the search has to find the right one.
func mobileprovision(body string) []byte {
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>` + body + `</dict></plist>`
	head := "\x30\x82\x1a\x00\x06\x09\x2a\x86\x48\x86\xf7\x0d\x01\x07\x02\xa0\x82"
	tail := "\x00\x00\x30\x82\x05\xff" + strings.Repeat("\xa1", 64)
	return []byte(head + plist + tail)
}

func TestProfileType(t *testing.T) {
	devices := "<key>ProvisionedDevices</key><array><string>00008030-001</string></array>"
	allow := func(v bool) string {
		if v {
			return "<key>Entitlements</key><dict><key>get-task-allow</key><true/></dict>"
		}
		return "<key>Entitlements</key><dict><key>get-task-allow</key><false/></dict>"
	}
	for _, tc := range []struct {
		name string
		body string
		want Type
	}{
		{"development", devices + allow(true), TypeDevelopment},
		{"ad-hoc", devices + allow(false), TypeAdHoc},
		{"app-store", allow(false), TypeAppStore},
		{"enterprise", "<key>ProvisionsAllDevices</key><true/>" + allow(false), TypeEnterprise},
		{"enterprise with devices", "<key>ProvisionsAllDevices</key><true/>" + devices + allow(false), TypeEnterprise},
		{"empty device list is still ad-hoc", "<key>ProvisionedDevices</key><array/>" + allow(false), TypeAdHoc},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ProfileType(mobileprovision(tc.body))
			if err != nil || got != tc.want {
				t.Errorf("ProfileType = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
	for name, data := range map[string][]byte{
		"no plist":  []byte("\x30\x82\x1a\x00 just bytes"),
		"bad plist": []byte("<?xml version=\"1.0\"?><plist><dict><key>x</key></plist>"),
		"empty":     nil,
	} {
		if _, err := ProfileType(data); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestParseType(t *testing.T) {
	for in, want := range map[string]Type{
		"development": TypeDevelopment, "ad-hoc": TypeAdHoc, "adhoc": TypeAdHoc, " App-Store ": TypeAppStore,
		"appstore": TypeAppStore, "enterprise": TypeEnterprise, "in-house": TypeEnterprise,
	} {
		if got, err := ParseType(in); err != nil || got != want {
			t.Errorf("ParseType(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := ParseType("distribution"); err == nil {
		t.Error("unknown type accepted")
	}
	if TypeAppStore.NeedsDevices() || TypeEnterprise.NeedsDevices() || !TypeDevelopment.NeedsDevices() || !TypeAdHoc.NeedsDevices() {
		t.Error("NeedsDevices: only development and ad-hoc profiles list devices")
	}
	if KeyFileName(TypeAppStore) != "ios-signing-app-store.key" || P12FileName(TypeAdHoc) != "ios-signing-ad-hoc.p12" {
		t.Errorf("file names: %s %s", KeyFileName(TypeAppStore), P12FileName(TypeAdHoc))
	}
}
