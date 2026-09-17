package signing

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// EncodeExtensionProfiles is the IOS_EXTENSION_PROFILES_<SET> secret: a JSON
// object of extension bundle id to base64 .mobileprovision, {} for none.
func EncodeExtensionProfiles(profiles map[string][]byte) string {
	encoded := make(map[string]string, len(profiles))
	for id, data := range profiles {
		encoded[id] = base64.StdEncoding.EncodeToString(data)
	}
	out, _ := json.Marshal(encoded) // a map of strings always marshals
	return string(out)
}

// DecodeExtensionProfiles reads what EncodeExtensionProfiles wrote; an empty
// value is no extensions.
func DecodeExtensionProfiles(secret string) (map[string][]byte, error) {
	if strings.TrimSpace(secret) == "" {
		return map[string][]byte{}, nil
	}
	var encoded map[string]string
	if err := json.Unmarshal([]byte(secret), &encoded); err != nil {
		return nil, fmt.Errorf("extension profiles must be a JSON object of bundle id to base64 .mobileprovision: %w", err)
	}
	profiles := make(map[string][]byte, len(encoded))
	for id, value := range encoded {
		data, err := base64.StdEncoding.DecodeString(value)
		if err != nil {
			return nil, fmt.Errorf("extension profile %s: %w", id, err)
		}
		profiles[id] = data
	}
	return profiles, nil
}

// ProfileBundleID reads the app id a .mobileprovision covers, without the
// team prefix; a wildcard profile ends in "*".
func ProfileBundleID(data []byte) (string, error) {
	dict, err := profilePlist(data)
	if err != nil {
		return "", err
	}
	entitlements, _ := dict["Entitlements"].(map[string]any)
	appID, _ := entitlements["application-identifier"].(string)
	if appID == "" {
		return "", errors.New("provisioning profile has no application-identifier entitlement")
	}
	// The team prefix is the first element of TeamIdentifier, or whatever
	// precedes the first dot when the profile does not list one.
	if teams, _ := dict["TeamIdentifier"].([]any); len(teams) > 0 {
		if team, _ := teams[0].(string); team != "" && strings.HasPrefix(appID, team+".") {
			return strings.TrimPrefix(appID, team+"."), nil
		}
	}
	_, id, found := strings.Cut(appID, ".")
	if !found {
		return "", fmt.Errorf("application-identifier %q has no team prefix", appID)
	}
	return id, nil
}

// Covers reports whether a profile's app id (exact, or a "*" wildcard) covers
// a bundle id, the way the runner matches targets to profiles.
func Covers(appID, bundleID string) bool {
	if prefix, ok := strings.CutSuffix(appID, "*"); ok {
		return strings.HasPrefix(bundleID, prefix)
	}
	return appID == bundleID
}
