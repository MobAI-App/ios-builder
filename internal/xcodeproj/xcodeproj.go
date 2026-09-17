// Package xcodeproj reads what signing needs out of a project.pbxproj.
package xcodeproj

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"howett.net/plist"
)

// ExtensionProductTypes are the target types that ship inside the app with a
// bundle id and profile of their own. The runner's apply_signing_to_app_target
// carries the same list and must agree.
var ExtensionProductTypes = []string{
	"com.apple.product-type.app-extension",
	"com.apple.product-type.app-extension.messages",
	"com.apple.product-type.extensionkit-extension",
	"com.apple.product-type.application.watchapp2",
	"com.apple.product-type.watchkit2-extension",
	"com.apple.product-type.application.on-demand-install-capable",
}

// ExtensionBundleIDs returns the distinct bundle identifiers of the extension
// targets of every *.xcodeproj directly under dir, sorted. Values built from
// other settings ($(...)) are skipped, and a dir without a project yields nil.
func ExtensionBundleIDs(dir string) ([]string, error) {
	if dir == "" {
		dir = "."
	}
	projects, _ := filepath.Glob(filepath.Join(dir, "*.xcodeproj", "project.pbxproj"))
	var ids []string
	for _, path := range projects {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		found, err := extensionBundleIDs(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		for _, id := range found {
			if !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
	}
	slices.Sort(ids)
	return ids, nil
}

// extensionBundleIDs parses one project.pbxproj (OpenStep text as Xcode
// writes it, or the XML plist a rewrite leaves).
func extensionBundleIDs(data []byte) ([]string, error) {
	var project struct {
		Objects map[string]struct {
			Isa                    string   `plist:"isa"`
			ProductType            string   `plist:"productType"`
			BuildConfigurationList string   `plist:"buildConfigurationList"`
			BuildConfigurations    []string `plist:"buildConfigurations"`
			BuildSettings          struct {
				BundleID string `plist:"PRODUCT_BUNDLE_IDENTIFIER"`
			} `plist:"buildSettings"`
		} `plist:"objects"`
	}
	if _, err := plist.Unmarshal(data, &project); err != nil {
		return nil, fmt.Errorf("parse project.pbxproj: %w", err)
	}
	var ids []string
	for _, target := range project.Objects {
		if target.Isa != "PBXNativeTarget" || !slices.Contains(ExtensionProductTypes, target.ProductType) {
			continue
		}
		for _, configID := range project.Objects[target.BuildConfigurationList].BuildConfigurations {
			id := project.Objects[configID].BuildSettings.BundleID
			if id == "" || strings.Contains(id, "$") || slices.Contains(ids, id) {
				continue
			}
			ids = append(ids, id)
		}
	}
	return ids, nil
}
