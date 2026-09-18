package signing

import (
	"bytes"
	"errors"
	"fmt"

	"howett.net/plist"
)

// ProfileType reads the type of a .mobileprovision. The file is a CMS
// signature wrapping an XML plist; the plist alone decides the type, by the
// rules detect_export_method applies on the runner: ProvisionsAllDevices is
// enterprise, ProvisionedDevices with get-task-allow is development and
// without it ad-hoc, and a profile with neither is App Store (store).
func ProfileType(data []byte) (Type, error) {
	dict, err := profilePlist(data)
	if err != nil {
		return "", err
	}
	if all, _ := dict["ProvisionsAllDevices"].(bool); all {
		return TypeEnterprise, nil
	}
	if _, ok := dict["ProvisionedDevices"]; ok {
		entitlements, _ := dict["Entitlements"].(map[string]any)
		if allow, _ := entitlements["get-task-allow"].(bool); allow {
			return TypeDevelopment, nil
		}
		return TypeAdHoc, nil
	}
	return TypeStore, nil
}

// ProfileDevices counts the UDIDs a .mobileprovision lists; 0 for App Store
// and enterprise profiles, which install on any device.
func ProfileDevices(data []byte) (int, error) {
	dict, err := profilePlist(data)
	if err != nil {
		return 0, err
	}
	devices, _ := dict["ProvisionedDevices"].([]any)
	return len(devices), nil
}

// profilePlist is the plist inside a .mobileprovision's CMS signature.
func profilePlist(data []byte) (map[string]any, error) {
	start := bytes.Index(data, []byte("<?xml"))
	end := bytes.LastIndex(data, []byte("</plist>"))
	if start < 0 || end < start {
		return nil, errors.New("not a provisioning profile: no plist inside")
	}
	var dict map[string]any
	if _, err := plist.Unmarshal(data[start:end+len("</plist>")], &dict); err != nil {
		return nil, fmt.Errorf("parse provisioning profile: %w", err)
	}
	return dict, nil
}
