package github

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// ErrFileNotFound is GetFile's answer for a path the branch does not have.
var ErrFileNotFound = errors.New("file not found")

// ListRepositories lists the repositories the token's user can push to, the
// most recently pushed first, following every page. A program choosing a
// repository to set signing up for starts here.
func (c *Client) ListRepositories(ctx context.Context) ([]Repository, error) {
	var all []Repository
	for page := 1; ; page++ {
		var list []Repository
		if err := c.do(ctx, fmt.Sprintf("/user/repos?per_page=100&page=%d&sort=pushed&affiliation=owner,collaborator,organization_member", page), &list); err != nil {
			return nil, fmt.Errorf("list repositories: %w", err)
		}
		all = append(all, list...)
		if len(list) < 100 {
			return all, nil
		}
	}
}

// ListBranches lists the repository's branch names, following every page.
func (c *Client) ListBranches(ctx context.Context, owner, repo string) ([]string, error) {
	var names []string
	for page := 1; ; page++ {
		var list []struct {
			Name string `json:"name"`
		}
		if err := c.do(ctx, fmt.Sprintf("/repos/%s/%s/branches?per_page=100&page=%d", owner, repo, page), &list); err != nil {
			return nil, fmt.Errorf("list branches of %s/%s: %w", owner, repo, err)
		}
		for _, b := range list {
			names = append(names, b.Name)
		}
		if len(list) < 100 {
			return names, nil
		}
	}
}

// GetFile reads one file of the repository at ref (a branch, tag or commit;
// empty means the default branch). A missing file is ErrFileNotFound, so a
// caller can tell "no builder.json yet" from a failure.
func (c *Client) GetFile(ctx context.Context, owner, repo, path, ref string) (*FileContent, error) {
	query := ""
	if ref != "" {
		query = "?ref=" + url.QueryEscape(ref)
	}
	var file struct {
		FileContent
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
	}
	if err := c.do(ctx, fmt.Sprintf("/repos/%s/%s/contents/%s%s", owner, repo, escapePath(path), query), &file); err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Status == "404" {
			return nil, fmt.Errorf("%s in %s/%s: %w", path, owner, repo, ErrFileNotFound)
		}
		return nil, fmt.Errorf("read %s of %s/%s: %w", path, owner, repo, err)
	}
	if file.Encoding != "base64" {
		return nil, fmt.Errorf("read %s of %s/%s: unexpected encoding %q", path, owner, repo, file.Encoding)
	}
	// GitHub wraps the base64 at 60 columns.
	data, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(file.Content, "\n", ""))
	if err != nil {
		return nil, fmt.Errorf("read %s of %s/%s: decode: %w", path, owner, repo, err)
	}
	file.FileContent.Content = data
	return &file.FileContent, nil
}

// PutFile creates or updates one file with a commit on req.Branch. Updating
// needs req.SHA, the blob GetFile reported, so a file changed since it was
// read is refused by GitHub (409) rather than overwritten.
func (c *Client) PutFile(ctx context.Context, owner, repo, path string, req *PutFileRequest) (*FileCommit, error) {
	body := struct {
		Message string `json:"message"`
		Content string `json:"content"`
		Branch  string `json:"branch,omitempty"`
		SHA     string `json:"sha,omitempty"`
	}{Message: req.Message, Content: base64.StdEncoding.EncodeToString(req.Content), Branch: req.Branch, SHA: req.SHA}
	var result struct {
		Commit FileCommit `json:"commit"`
	}
	if err := c.call(ctx, "PUT", fmt.Sprintf("/repos/%s/%s/contents/%s", owner, repo, escapePath(path)), body, &result); err != nil {
		return nil, fmt.Errorf("write %s of %s/%s: %w", path, owner, repo, err)
	}
	return &result.Commit, nil
}

// escapePath escapes each segment of a repository path, keeping the slashes.
func escapePath(path string) string {
	parts := strings.Split(path, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}
