package xcodeproj

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// fixture is an app, a widget, a share extension whose bundle id comes from
// another setting, and a framework, laid out as Xcode writes them.
const fixture = `// !$*UTF8*$!
{
	archiveVersion = 1;
	classes = {
	};
	objectVersion = 56;
	objects = {

/* Begin PBXNativeTarget section */
		A1 /* App */ = {
			isa = PBXNativeTarget;
			buildConfigurationList = LA /* Build configuration list for PBXNativeTarget "App" */;
			buildPhases = (
			);
			name = App;
			productName = App;
			productType = "com.apple.product-type.application";
		};
		W1 /* Widget */ = {
			isa = PBXNativeTarget;
			buildConfigurationList = LW;
			name = WidgetExtension;
			productType = "com.apple.product-type.app-extension";
		};
		S1 /* Share */ = {
			isa = PBXNativeTarget;
			buildConfigurationList = LS;
			name = Share;
			productType = "com.apple.product-type.app-extension";
		};
		K1 /* Kit */ = {
			isa = PBXNativeTarget;
			buildConfigurationList = LK;
			name = Kit;
			productType = "com.apple.product-type.framework";
		};
/* End PBXNativeTarget section */

/* Begin XCBuildConfiguration section */
		AD = { isa = XCBuildConfiguration; buildSettings = { PRODUCT_BUNDLE_IDENTIFIER = com.example.app; SWIFT_VERSION = 5.0; }; name = Debug; };
		AR = { isa = XCBuildConfiguration; buildSettings = { PRODUCT_BUNDLE_IDENTIFIER = com.example.app; }; name = Release; };
		WD = { isa = XCBuildConfiguration; buildSettings = { PRODUCT_BUNDLE_IDENTIFIER = "com.example.app.widget"; }; name = Debug; };
		WR = { isa = XCBuildConfiguration; buildSettings = { PRODUCT_BUNDLE_IDENTIFIER = "com.example.app.widget"; }; name = Release; };
		SD = { isa = XCBuildConfiguration; buildSettings = { PRODUCT_BUNDLE_IDENTIFIER = "$(APP_BUNDLE_ID).share"; }; name = Debug; };
		SR = { isa = XCBuildConfiguration; buildSettings = { PRODUCT_BUNDLE_IDENTIFIER = "$(APP_BUNDLE_ID).share"; }; name = Release; };
		KD = { isa = XCBuildConfiguration; buildSettings = { PRODUCT_BUNDLE_IDENTIFIER = com.example.app.Kit; }; name = Debug; };
		KR = { isa = XCBuildConfiguration; buildSettings = { PRODUCT_BUNDLE_IDENTIFIER = com.example.app.Kit; }; name = Release; };
/* End XCBuildConfiguration section */

/* Begin XCConfigurationList section */
		LA = { isa = XCConfigurationList; buildConfigurations = ( AD, AR, ); defaultConfigurationName = Release; };
		LW = { isa = XCConfigurationList; buildConfigurations = ( WD, WR, ); defaultConfigurationName = Release; };
		LS = { isa = XCConfigurationList; buildConfigurations = ( SD, SR, ); defaultConfigurationName = Release; };
		LK = { isa = XCConfigurationList; buildConfigurations = ( KD, KR, ); defaultConfigurationName = Release; };
/* End XCConfigurationList section */
	};
	rootObject = P0;
}
`

func TestExtensionBundleIDs(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "App.xcodeproj")
	if err := os.MkdirAll(project, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "project.pbxproj"), []byte(fixture), 0644); err != nil {
		t.Fatal(err)
	}
	// The pods project sits a level down and is never read.
	pods := filepath.Join(dir, "Pods", "Pods.xcodeproj")
	if err := os.MkdirAll(pods, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pods, "project.pbxproj"), []byte("not a plist {"), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := ExtensionBundleIDs(dir)
	if err != nil {
		t.Fatal(err)
	}
	// The app and the framework are not extensions; the share extension's
	// id is built from another setting and cannot be provisioned by name.
	if want := []string{"com.example.app.widget"}; !slices.Equal(got, want) {
		t.Errorf("ExtensionBundleIDs = %v, want %v", got, want)
	}
	if got, err := ExtensionBundleIDs(filepath.Join(dir, "missing")); err != nil || got != nil {
		t.Errorf("no project: %v, %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(project, "project.pbxproj"), []byte("{ objects = ( broken"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ExtensionBundleIDs(dir); err == nil {
		t.Error("unparsable project accepted")
	}
}
