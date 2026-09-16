package main

import (
	"os"
	"path/filepath"
	"testing"
)

const flutterPbxproj = `
		97C147061CF9000F007C117D /* Debug */ = {
			buildSettings = {
				PRODUCT_BUNDLE_IDENTIFIER = com.example.myApp;
				PRODUCT_NAME = "$(TARGET_NAME)";
			};
		};
		97C147071CF9000F007C117D /* Release */ = {
			buildSettings = {
				PRODUCT_BUNDLE_IDENTIFIER = com.example.myApp;
			};
		};
		331C8088294A63A400263BE5 /* Debug */ = {
			buildSettings = {
				PRODUCT_BUNDLE_IDENTIFIER = com.example.myApp.RunnerTests;
			};
		};
`

func TestBundleIDsFromPbxproj(t *testing.T) {
	if got := bundleIDsFromPbxproj(flutterPbxproj); len(got) != 1 || got[0] != "com.example.myApp" {
		t.Errorf("bundleIDsFromPbxproj = %v, want [com.example.myApp]", got)
	}
	quoted := `PRODUCT_BUNDLE_IDENTIFIER = "com.example.my-app"; PRODUCT_BUNDLE_IDENTIFIER = "$(BUNDLE_ID_PREFIX).app";`
	if got := bundleIDsFromPbxproj(quoted); len(got) != 1 || got[0] != "com.example.my-app" {
		t.Errorf("bundleIDsFromPbxproj(quoted) = %v", got)
	}
	two := `PRODUCT_BUNDLE_IDENTIFIER = com.example.free; PRODUCT_BUNDLE_IDENTIFIER = com.example.pro;`
	if got := bundleIDsFromPbxproj(two); len(got) != 2 {
		t.Errorf("bundleIDsFromPbxproj(two apps) = %v", got)
	}
}

func TestDetectBundleID(t *testing.T) {
	dir := t.TempDir()
	proj := filepath.Join(dir, "ios", "Runner.xcodeproj")
	if err := os.MkdirAll(proj, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "project.pbxproj"), []byte(flutterPbxproj), 0644); err != nil {
		t.Fatal(err)
	}
	if got := detectBundleID(filepath.Join(dir, "ios")); got != "com.example.myApp" {
		t.Errorf("detectBundleID = %q", got)
	}
	if got := detectBundleID(filepath.Join(dir, "missing")); got != "" {
		t.Errorf("detectBundleID(missing) = %q, want empty", got)
	}
	// Two app targets: ambiguous, leave it to signing setup.
	two := filepath.Join(dir, "two", "App.xcodeproj")
	if err := os.MkdirAll(two, 0755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(two, "project.pbxproj"), []byte(`PRODUCT_BUNDLE_IDENTIFIER = com.example.free; PRODUCT_BUNDLE_IDENTIFIER = com.example.pro;`), 0644)
	if got := detectBundleID(filepath.Join(dir, "two")); got != "" {
		t.Errorf("detectBundleID(two apps) = %q, want empty", got)
	}
}
