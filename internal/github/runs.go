package github

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// ListWorkflowRunsPage lists one page (1-based, newest first) of a workflow
// file's runs, perPage at most 100.
func (c *Client) ListWorkflowRunsPage(ctx context.Context, owner, repo, workflowFile string, perPage, page int) ([]WorkflowRun, error) {
	q := url.Values{}
	q.Set("per_page", strconv.Itoa(perPage))
	q.Set("page", strconv.Itoa(page))
	path := fmt.Sprintf("/repos/%s/%s/actions/workflows/%s/runs?%s", owner, repo, workflowFile, q.Encode())

	var resp WorkflowRunsResponse
	if err := c.do(ctx, path, &resp); err != nil {
		return nil, fmt.Errorf("failed to list workflow runs: %w", err)
	}
	return resp.WorkflowRuns, nil
}

// JobLogs reads a job's plain-text log. GitHub answers with a redirect to a
// short-lived storage URL, which is fetched without the token. The log exists
// once the job has completed; before that GitHub answers 404.
func (c *Client) JobLogs(ctx context.Context, owner, repo string, jobID int64) ([]byte, error) {
	path := fmt.Sprintf("/repos/%s/%s/actions/jobs/%d/logs", owner, repo, jobID)

	resp, err := c.request(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusFound {
		resp.Body.Close()
		location := resp.Header.Get("Location")
		if location == "" {
			return nil, fmt.Errorf("job log redirect missing Location header")
		}
		req, err := http.NewRequestWithContext(ctx, "GET", location, nil)
		if err != nil {
			return nil, err
		}
		resp, err = c.httpClient.Do(req)
		if err != nil {
			return nil, err
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to read job log: status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read job log: %w", err)
	}
	return data, nil
}
