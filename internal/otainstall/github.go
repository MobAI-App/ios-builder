package otainstall

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/MobAI-App/ios-builder/internal/github"
	"github.com/google/uuid"
)

const (
	// TagPrefix starts the tag_name of every draft release Builder creates;
	// drafts never turn it into a git tag.
	TagPrefix = "ios-builder/distribute-"
	// GistMarker is the description of every manifest gist, so cleanup can
	// tell them from the user's own.
	GistMarker   = "ios-builder distribute"
	manifestName = "manifest.plist"
	releaseBody  = "Temporary upload created by builder ios distribute; safe to delete."
)

// GitHub keeps the IPA as a draft-release asset (its download URL is signed
// and needs no auth, but is ~1000 characters) and the manifest as a secret
// gist, whose raw URL is short enough for a QR code.
type GitHub struct {
	client      *github.Client
	owner, repo string
	now         func() time.Time
}

// NewGitHub returns the backend for owner/repo.
func NewGitHub(client *github.Client, owner, repo string) *GitHub {
	return &GitHub{client: client, owner: owner, repo: repo, now: time.Now}
}

type githubUpload struct {
	b       *GitHub
	app     *App
	release *github.Release
	assetID int64
	gistID  string
}

func (g *GitHub) Upload(ctx context.Context, app *App, progress func(done, total int64)) (Upload, error) {
	f, err := os.Open(app.Path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	name := fmt.Sprintf("ios-builder distribute %s %s (%s)", app.Title, app.Version, app.Build)
	rel, err := g.client.CreateDraftRelease(ctx, g.owner, g.repo, TagPrefix+uuid.NewString()[:8], name, releaseBody)
	if err != nil {
		return nil, err
	}
	up := &githubUpload{b: g, app: app, release: rel}
	asset, err := g.client.UploadReleaseAsset(ctx, rel.UploadURL, assetName(app.Title), f, st.Size(), progress)
	if err != nil {
		_ = g.client.DeleteRelease(ctx, g.owner, g.repo, rel.ID)
		return nil, err
	}
	up.assetID = asset.ID
	return up, nil
}

// Cleanup deletes Builder's draft releases and manifest gists.
func (g *GitHub) Cleanup(ctx context.Context) (int, error) {
	n := 0
	releases, err := g.client.ListReleases(ctx, g.owner, g.repo)
	if err != nil {
		return 0, err
	}
	for i := range releases {
		if releases[i].Draft && strings.HasPrefix(releases[i].TagName, TagPrefix) {
			if err := g.client.DeleteRelease(ctx, g.owner, g.repo, releases[i].ID); err != nil {
				return n, err
			}
			n++
		}
	}
	gists, err := g.client.ListGists(ctx)
	if err != nil {
		return n, err
	}
	for i := range gists {
		_, manifest := gists[i].Files[manifestName]
		if gists[i].Description == GistMarker && manifest && len(gists[i].Files) == 1 {
			if err := g.client.DeleteGist(ctx, gists[i].ID); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
}

func (u *githubUpload) Mint(ctx context.Context, build func(ipaURL string) ([]byte, error)) (*Links, error) {
	ipaURL, err := u.b.client.MintAssetURL(ctx, u.b.owner, u.b.repo, u.assetID)
	if err != nil {
		return nil, err
	}
	body, err := build(ipaURL)
	if err != nil {
		return nil, err
	}
	gist, err := u.b.client.CreateSecretGist(ctx, GistMarker, map[string]string{manifestName: string(body)})
	if err != nil {
		return nil, err
	}
	raw := gist.Files[manifestName].RawURL
	if raw == "" {
		_ = u.b.client.DeleteGist(ctx, gist.ID)
		return nil, errors.New("gist created without a raw URL for manifest.plist")
	}
	if u.gistID != "" {
		_ = u.b.client.DeleteGist(ctx, u.gistID)
	}
	u.gistID = gist.ID
	return &Links{
		Link: Link(raw), ManifestURL: raw, IPAURL: ipaURL, ExpiresAt: Expiry(ipaURL, u.b.now()),
		ReleaseID: u.release.ID, GistID: gist.ID,
	}, nil
}

func (u *githubUpload) Close(ctx context.Context) error {
	var errs []error
	if u.gistID != "" {
		if err := u.b.client.DeleteGist(ctx, u.gistID); err != nil {
			errs = append(errs, err)
		}
		u.gistID = ""
	}
	if u.release != nil {
		if err := u.b.client.DeleteRelease(ctx, u.b.owner, u.b.repo, u.release.ID); err != nil {
			errs = append(errs, err)
		}
		u.release = nil
	}
	return errors.Join(errs...)
}

func (u *githubUpload) Leftovers() []string {
	var out []string
	if u.release != nil {
		out = append(out, fmt.Sprintf("draft release %d in %s/%s (%s)", u.release.ID, u.b.owner, u.b.repo, u.release.HTMLURL))
	}
	if u.gistID != "" {
		out = append(out, "gist "+u.gistID)
	}
	return out
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// assetName is the IPA's name on the release: the title with anything a URL
// or a shell would trip over replaced.
func assetName(title string) string {
	name := strings.Trim(unsafeName.ReplaceAllString(title, "-"), "-.")
	if name == "" {
		name = "app"
	}
	return name + ".ipa"
}
