package github

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Release is a repository release; drafts have no tag in git.
type Release struct {
	ID        int64  `json:"id"`
	TagName   string `json:"tag_name"`
	Name      string `json:"name"`
	Draft     bool   `json:"draft"`
	HTMLURL   string `json:"html_url"`
	UploadURL string `json:"upload_url"` // templated: .../assets{?name,label}
}

// ReleaseAsset is a file attached to a release.
type ReleaseAsset struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// Gist is a gist as the API returns it.
type Gist struct {
	ID          string              `json:"id"`
	Description string              `json:"description"`
	HTMLURL     string              `json:"html_url"`
	Public      bool                `json:"public"`
	Files       map[string]GistFile `json:"files"`
}

// GistFile is one file of a gist; RawURL is pinned to the gist's commit.
type GistFile struct {
	RawURL string `json:"raw_url"`
}

type createReleaseRequest struct {
	TagName string `json:"tag_name"`
	Name    string `json:"name"`
	Body    string `json:"body"`
	Draft   bool   `json:"draft"`
}

// CreateDraftRelease creates a draft release. No tag is created for a draft,
// it is invisible on the repository page and sends no notifications.
func (c *Client) CreateDraftRelease(ctx context.Context, owner, repo, tag, name, body string) (*Release, error) {
	var rel Release
	req := createReleaseRequest{TagName: tag, Name: name, Body: body, Draft: true}
	if err := c.call(ctx, "POST", fmt.Sprintf("/repos/%s/%s/releases", owner, repo), req, &rel); err != nil {
		return nil, fmt.Errorf("create draft release in %s/%s: %w", owner, repo, err)
	}
	return &rel, nil
}

// ListReleases lists the repository's releases, drafts included for anyone
// with push access, following every page.
func (c *Client) ListReleases(ctx context.Context, owner, repo string) ([]Release, error) {
	var all []Release
	for page := 1; ; page++ {
		var list []Release
		if err := c.do(ctx, fmt.Sprintf("/repos/%s/%s/releases?per_page=100&page=%d", owner, repo, page), &list); err != nil {
			return nil, fmt.Errorf("list releases of %s/%s: %w", owner, repo, err)
		}
		all = append(all, list...)
		if len(list) < 100 {
			return all, nil
		}
	}
}

// DeleteRelease deletes a release and its assets.
func (c *Client) DeleteRelease(ctx context.Context, owner, repo string, id int64) error {
	if err := c.call(ctx, "DELETE", fmt.Sprintf("/repos/%s/%s/releases/%d", owner, repo, id), nil, nil); err != nil {
		return fmt.Errorf("delete release %d of %s/%s: %w", id, owner, repo, err)
	}
	return nil
}

// UploadReleaseAsset attaches size bytes from r to the release as name.
// uploadURL is the release's UploadURL; the URI template is dropped.
func (c *Client) UploadReleaseAsset(ctx context.Context, uploadURL, name string, r io.Reader, size int64, progress ProgressFunc) (*ReleaseAsset, error) {
	base := uploadURL
	if i := strings.Index(base, "{"); i >= 0 {
		base = base[:i]
	}
	body := r
	if progress != nil {
		body = &progressReader{r: r, total: size, progress: progress}
	}
	req, err := http.NewRequestWithContext(ctx, "POST", base+"?name="+url.QueryEscape(name), body)
	if err != nil {
		return nil, err
	}
	req.ContentLength = size
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", APIVersion)
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("upload %s: %w", name, err)
	}
	defer resp.Body.Close()
	var asset ReleaseAsset
	if err := decodeResponse(resp, &asset); err != nil {
		return nil, fmt.Errorf("upload %s: %w", name, err)
	}
	return &asset, nil
}

// MintAssetURL returns a URL for the asset's bytes that needs no
// authentication. GitHub answers the download request with a redirect to a
// signed URL; the redirect is not followed, so the signed URL comes back
// unspent. It expires after about five minutes.
func (c *Client) MintAssetURL(ctx context.Context, owner, repo string, assetID int64) (string, error) {
	path := fmt.Sprintf("/repos/%s/%s/releases/assets/%d", owner, repo, assetID)
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+path, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("X-GitHub-Api-Version", APIVersion)
	noRedirect := &http.Client{Transport: c.httpClient.Transport, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := noRedirect.Do(req)
	if err != nil {
		return "", fmt.Errorf("mint asset URL: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound && resp.StatusCode != http.StatusTemporaryRedirect {
		return "", fmt.Errorf("mint asset URL: expected a redirect, got status %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", errors.New("mint asset URL: redirect without Location")
	}
	return loc, nil
}

// ErrGistScope is returned when the token cannot create gists.
var ErrGistScope = errors.New("the saved GitHub login lacks the gist scope that builder ios distribute needs; run builder auth github again")

type createGistRequest struct {
	Description string                  `json:"description"`
	Public      bool                    `json:"public"`
	Files       map[string]gistFileBody `json:"files"`
}

type gistFileBody struct {
	Content string `json:"content"`
}

// CreateSecretGist creates an unlisted gist: readable by anyone with the URL,
// listed nowhere. GitHub answers 404 (or 403) when the token has no gist scope.
func (c *Client) CreateSecretGist(ctx context.Context, description string, files map[string]string) (*Gist, error) {
	req := createGistRequest{Description: description, Files: map[string]gistFileBody{}}
	for name, content := range files {
		req.Files[name] = gistFileBody{Content: content}
	}
	var gist Gist
	if err := c.call(ctx, "POST", "/gists", req, &gist); err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && (apiErr.Status == "403" || apiErr.Status == "404") {
			return nil, fmt.Errorf("%w (%s)", ErrGistScope, apiErr.Message)
		}
		return nil, fmt.Errorf("create gist: %w", err)
	}
	return &gist, nil
}

// ListGists lists the authenticated user's gists, secret ones included.
func (c *Client) ListGists(ctx context.Context) ([]Gist, error) {
	var all []Gist
	for page := 1; ; page++ {
		var list []Gist
		if err := c.do(ctx, fmt.Sprintf("/gists?per_page=100&page=%d", page), &list); err != nil {
			return nil, fmt.Errorf("list gists: %w", err)
		}
		all = append(all, list...)
		if len(list) < 100 {
			return all, nil
		}
	}
}

// DeleteGist deletes a gist.
func (c *Client) DeleteGist(ctx context.Context, id string) error {
	if err := c.call(ctx, "DELETE", "/gists/"+id, nil, nil); err != nil {
		return fmt.Errorf("delete gist %s: %w", id, err)
	}
	return nil
}

// progressReader reports how much of a body has been read.
type progressReader struct {
	r        io.Reader
	read     int64
	total    int64
	progress ProgressFunc
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.read += int64(n)
		p.progress(p.read, p.total)
	}
	return n, err
}
