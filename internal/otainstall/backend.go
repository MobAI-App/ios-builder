package otainstall

import (
	"context"
	"fmt"
	"time"

	"github.com/MobAI-App/ios-builder/internal/config"
)

// Backend names, for --backend and distribute.backend in builder.json.
// github, s3 and azure implement Backend; testflight is not an over-the-air
// install and goes through App Store Connect instead (see Inspect and
// CheckDistribution). A hosted short-link service would be one more Backend:
// Upload stores the IPA, Mint returns its short manifest URL.
const (
	BackendGitHub     = "github"
	BackendS3         = "s3"
	BackendAzure      = "azure"
	BackendTestFlight = "testflight"
)

// BackendName is flag, else distribute.backend in builder.json, else github.
func BackendName(flag string, cfg *config.Config) (string, error) {
	name := flag
	if name == "" && cfg != nil && cfg.Distribute != nil {
		name = cfg.Distribute.Backend
	}
	switch name {
	case "":
		return BackendGitHub, nil
	case BackendGitHub, BackendS3, BackendAzure, BackendTestFlight:
		return name, nil
	}
	return "", fmt.Errorf("unknown distribute backend %q (choose github, s3, azure or testflight)", name)
}

// Backend stores the IPA and the manifest where an iPhone can fetch them.
type Backend interface {
	// Upload stores the IPA once for the session.
	Upload(ctx context.Context, app *App, progress func(done, total int64)) (Upload, error)
	// Cleanup removes what earlier sessions left behind and says how many.
	Cleanup(ctx context.Context) (int, error)
}

// Upload is a stored IPA that can be linked to any number of times.
type Upload interface {
	// Mint publishes a fresh manifest for a fresh IPA URL; the previous one
	// is retired. build turns the IPA URL into the manifest bytes.
	Mint(ctx context.Context, build func(ipaURL string) ([]byte, error)) (*Links, error)
	// Close removes the IPA and the current manifest.
	Close(ctx context.Context) error
	// Leftovers names what Close would remove, for a session that skips it.
	Leftovers() []string
}

// Links is one minted install link and where its parts live.
type Links struct {
	Link        string    `json:"link"`
	ManifestURL string    `json:"manifest_url"`
	IPAURL      string    `json:"ipa_url"`
	ExpiresAt   time.Time `json:"expires_at"`
	ReleaseID   int64     `json:"release_id,omitempty"`
	GistID      string    `json:"gist_id,omitempty"`
	// Objects names the IPA and the manifest in a bucket backend (s3://…, azure://…).
	Objects []string `json:"objects,omitempty"`
}
