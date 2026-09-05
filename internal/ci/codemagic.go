package ci

import (
	"context"
	"fmt"
	"io"
	"net/url"

	"github.com/MobAI-App/ios-builder/internal/config"
)

type Codemagic struct {
	api                    apiClient
	config                 config.CIConfig
	dispatchURL, statusURL string
}

func NewCodemagic(cfg config.CIConfig, token string) *Codemagic {
	return &Codemagic{api: newAPI(token, "x-auth-token"), config: cfg, dispatchURL: "https://api.codemagic.io", statusURL: "https://codemagic.io/api/v3"}
}
func (*Codemagic) Name() string { return "codemagic" }
func (c *Codemagic) Start(ctx context.Context, req Request) (Run, error) {
	var result struct {
		BuildID string `json:"buildId"`
	}
	body := map[string]any{"appId": c.config.AppID, "workflowId": req.Workflow, "branch": c.config.Branch,
		"environment": map[string]any{"variables": req.Variables}, "instanceType": "mac_mini_m2"}
	if err := c.api.request(ctx, "POST", c.dispatchURL+"/builds", body, &result); err != nil {
		return Run{}, err
	}
	if result.BuildID == "" {
		return Run{}, fmt.Errorf("Codemagic dispatch returned no build ID; check the dashboard before retrying")
	}
	return Run{ID: result.BuildID, URL: "https://codemagic.io/app/" + url.PathEscape(c.config.AppID) + "/build/" + url.PathEscape(result.BuildID)}, nil
}
func (c *Codemagic) Status(ctx context.Context, run Run) (Status, error) {
	var result struct {
		Data struct {
			Status    string `json:"status"`
			Artifacts []struct {
				Name string `json:"name"`
				URL  string `json:"short_lived_download_url"`
				Size int64  `json:"size_in_bytes"`
			} `json:"artifacts"`
		} `json:"data"`
	}
	if err := c.api.request(ctx, "GET", c.statusURL+"/builds/"+url.PathEscape(run.ID), nil, &result); err != nil {
		return Status{}, err
	}
	s := Status{State: result.Data.Status}
	switch s.State {
	case "finished":
		s.Done, s.Success = true, true
	case "failed", "canceled", "timeout", "skipped":
		s.Done = true
	case "initializing", "queued", "preparing", "fetching", "testing", "building", "publishing", "finishing":
	default:
		return Status{}, fmt.Errorf("Codemagic returned an unknown build status")
	}
	for _, a := range result.Data.Artifacts {
		s.Artifacts = append(s.Artifacts, Artifact{Name: a.Name, URL: a.URL, Size: a.Size})
	}
	return s, nil
}
func (c *Codemagic) Download(ctx context.Context, _ Run, a Artifact, w io.Writer) (int64, error) {
	return downloadURL(ctx, a.URL, w, c.api.http.Transport)
}
func (c *Codemagic) Cancel(ctx context.Context, run Run) error {
	return c.api.request(ctx, "POST", c.dispatchURL+"/builds/"+url.PathEscape(run.ID)+"/cancel", nil, nil)
}
