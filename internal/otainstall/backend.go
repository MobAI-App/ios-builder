package otainstall

import (
	"context"
	"time"
)

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
}
