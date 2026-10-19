package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfig_Validate(t *testing.T) {
	tests := []struct {
		name     string
		config   Config
		wantErr  bool
		errField string
	}{
		{
			name: "valid config",
			config: Config{
				Project:  "MyApp",
				Platform: "ios",
				GitHub:   GitHubConfig{Owner: "user", Repo: "builder-myapp"},
			},
			wantErr: false,
		},
		{
			name: "missing project",
			config: Config{
				Platform: "ios",
				GitHub:   GitHubConfig{Owner: "user", Repo: "builder-myapp"},
			},
			wantErr:  true,
			errField: "project",
		},
		{
			name: "missing github owner",
			config: Config{
				Project:  "MyApp",
				Platform: "ios",
				GitHub:   GitHubConfig{Repo: "builder-myapp"},
			},
			wantErr:  true,
			errField: "github.owner",
		},
		{
			name: "missing github repo",
			config: Config{
				Project:  "MyApp",
				Platform: "ios",
				GitHub:   GitHubConfig{Owner: "user"},
			},
			wantErr:  true,
			errField: "github.repo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && err != nil {
				valErr, ok := err.(*ValidationError)
				if !ok {
					t.Errorf("Validate() error type = %T, want *ValidationError", err)
					return
				}
				if valErr.Field != tt.errField {
					t.Errorf("Validate() error field = %q, want %q", valErr.Field, tt.errField)
				}
			}
		})
	}
}

func TestManager_SaveAndLoad(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "builder.json")

	mgr := &Manager{path: configPath}

	// Create config
	cfg := &Config{
		Project:  "TestApp",
		Platform: "ios",
		GitHub: GitHubConfig{
			Owner: "testuser",
			Repo:  "builder-testapp",
		},
	}

	// Save
	if err := mgr.Save(cfg); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Error("Config file not created after Save()")
	}

	// Load
	loaded, err := mgr.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	// Verify fields
	if loaded.Project != cfg.Project {
		t.Errorf("Project = %q, want %q", loaded.Project, cfg.Project)
	}
	if loaded.Platform != cfg.Platform {
		t.Errorf("Platform = %q, want %q", loaded.Platform, cfg.Platform)
	}
	if loaded.GitHub.Owner != cfg.GitHub.Owner {
		t.Errorf("GitHub.Owner = %q, want %q", loaded.GitHub.Owner, cfg.GitHub.Owner)
	}
	if loaded.GitHub.Repo != cfg.GitHub.Repo {
		t.Errorf("GitHub.Repo = %q, want %q", loaded.GitHub.Repo, cfg.GitHub.Repo)
	}
}

// TestManager_KeepsCacheSwitch: the runners read cache.ccache from builder.json,
// so a command that rewrites the file must not drop it.
func TestManager_KeepsCacheSwitch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "builder.json")
	if err := os.WriteFile(path, []byte(`{"project":"App","platform":"ios","github":{"owner":"o","repo":"r"},"cache":{"ccache":true}}`), 0644); err != nil {
		t.Fatal(err)
	}
	mgr := &Manager{path: path}
	cfg, err := mgr.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Cache == nil || !cfg.Cache.CCache {
		t.Fatalf("cache.ccache not loaded: %+v", cfg.Cache)
	}
	if err := mgr.Save(cfg); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"ccache": true`) {
		t.Fatalf("cache.ccache lost on save:\n%s", data)
	}
	if err := mgr.Save(&Config{Project: "App"}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); strings.Contains(string(data), `"cache"`) {
		t.Fatalf("an unset cache block is written:\n%s", data)
	}
}

func TestManager_Load_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "nonexistent.json")

	mgr := &Manager{path: configPath}

	_, err := mgr.Load()
	if err != ErrConfigNotFound {
		t.Errorf("Load() error = %v, want ErrConfigNotFound", err)
	}
}

func TestNewManager(t *testing.T) {
	mgr := NewManager()
	if mgr.path != ConfigFileName {
		t.Errorf("NewManager().path = %q, want %q", mgr.path, ConfigFileName)
	}
}
