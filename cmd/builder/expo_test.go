package main

import (
	"path/filepath"
	"testing"
)

// TestDetectIOSPathExpo covers the managed Expo case, where package.json
// depends on Expo but no Xcode project is committed anywhere, next to the
// project shapes that must keep detecting exactly as before.
func TestDetectIOSPathExpo(t *testing.T) {
	tests := []struct {
		name          string
		files         map[string]string
		wantPath      string
		wantFramework string
		wantExpo      bool
	}{
		{
			name:          "managed expo without an ios directory",
			files:         map[string]string{"package.json": `{"dependencies":{"expo":"~51.0.0"}}`},
			wantPath:      "ios",
			wantFramework: expoManagedFramework,
			wantExpo:      true,
		},
		{
			name: "ejected expo keeps the react native path",
			files: map[string]string{
				"package.json":                        `{"dependencies":{"expo":"~51.0.0","react-native":"0.74.0"}}`,
				"ios/MyApp.xcodeproj/project.pbxproj": "// project",
			},
			wantPath:      "ios",
			wantFramework: "React Native/Expo",
			wantExpo:      true,
		},
		{
			name:          "plain react native without ios stays undetected",
			files:         map[string]string{"package.json": `{"dependencies":{"react-native":"0.74.0"}}`},
			wantPath:      "",
			wantFramework: "",
			wantExpo:      false,
		},
		{
			name:          "native project at the root",
			files:         map[string]string{"MyApp.xcodeproj/project.pbxproj": "// project"},
			wantPath:      "",
			wantFramework: "Native iOS",
			wantExpo:      false,
		},
		{
			// The runners detect pubspec.yaml before package.json, so a
			// Flutter repo whose ios/ is not committed must not be claimed
			// here either — the runner would never prebuild it.
			name: "flutter wins over an expo dependency",
			files: map[string]string{
				"pubspec.yaml": "name: app\n",
				"package.json": `{"dependencies":{"expo":"~51.0.0"}}`,
			},
			wantPath:      "",
			wantFramework: "",
			wantExpo:      true,
		},
		{
			// KMP keeps its Xcode project in iosApp/, which is matched before
			// the Expo fallback is reached.
			name: "kmp keeps its own path",
			files: map[string]string{
				"iosApp/iosApp.xcodeproj/project.pbxproj": "// project",
				"package.json":                            `{"dependencies":{"expo":"~51.0.0"}}`,
			},
			wantPath:      "iosApp",
			wantFramework: "Kotlin Multiplatform",
			wantExpo:      true,
		},
		{
			// "expo" appears in the file, but not as a dependency.
			name: "unrelated node project naming expo",
			files: map[string]string{
				"package.json": `{"name":"expo","keywords":["expo"],"scripts":{"expo":"echo"},"dependencies":{"expo-server-sdk":"^3.7.0"}}`,
			},
			wantPath:      "",
			wantFramework: "",
			wantExpo:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := chdir(t)
			for name, content := range tt.files {
				writeFile(t, filepath.Join(dir, filepath.FromSlash(name)), content)
			}
			path, framework := detectIOSPath()
			if path != tt.wantPath || framework != tt.wantFramework {
				t.Fatalf("detectIOSPath() = (%q, %q), want (%q, %q)", path, framework, tt.wantPath, tt.wantFramework)
			}
			if got := isExpoProject(); got != tt.wantExpo {
				t.Fatalf("isExpoProject() = %v, want %v", got, tt.wantExpo)
			}
		})
	}
}
