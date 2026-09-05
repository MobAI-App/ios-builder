package ci

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"

	"github.com/MobAI-App/ios-builder/internal/config"
)

type Bitrise struct {
	api     apiClient
	config  config.CIConfig
	baseURL string
}

func NewBitrise(cfg config.CIConfig, token string) *Bitrise {
	return &Bitrise{api: newAPI(token, "Authorization"), config: cfg, baseURL: "https://api.bitrise.io/v0.1"}
}
func (*Bitrise) Name() string { return "bitrise" }
func (b *Bitrise) buildsURL() string {
	return b.baseURL + "/apps/" + url.PathEscape(b.config.AppID) + "/builds"
}
func (b *Bitrise) runURL(run Run) string { return b.buildsURL() + "/" + url.PathEscape(run.ID) }
func (b *Bitrise) Start(ctx context.Context, req Request) (Run, error) {
	type env struct {
		Key    string `json:"mapped_to"`
		Value  string `json:"value"`
		Expand bool   `json:"is_expand"`
	}
	keys := make([]string, 0, len(req.Variables))
	for k := range req.Variables {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	envs := make([]env, 0, len(keys))
	for _, k := range keys {
		envs = append(envs, env{Key: k, Value: req.Variables[k]})
	}
	body := map[string]any{"hook_info": map[string]string{"type": "bitrise"}, "build_params": map[string]any{
		"branch": b.config.Branch, "workflow_id": req.Workflow, "environments": envs, "machine_type_id": "g2.mac.medium"}}
	type item struct {
		ID     string `json:"build_slug"`
		URL    string `json:"build_url"`
		Status string `json:"status"`
	}
	var result struct {
		item
		Results []item `json:"results"`
	}
	if err := b.api.request(ctx, "POST", b.buildsURL(), body, &result); err != nil {
		return Run{}, err
	}
	i := result.item
	if len(result.Results) == 1 {
		i = result.Results[0]
	}
	if len(result.Results) > 1 || i.ID == "" || (i.Status != "" && i.Status != "ok") {
		return Run{}, fmt.Errorf("Bitrise dispatch did not identify one build; check the dashboard before retrying")
	}
	return Run{ID: i.ID, URL: "https://app.bitrise.io/build/" + url.PathEscape(i.ID)}, nil
}
func (b *Bitrise) Status(ctx context.Context, run Run) (Status, error) {
	var result struct {
		Data struct {
			Status *int   `json:"status"`
			Text   string `json:"status_text"`
		} `json:"data"`
	}
	if err := b.api.request(ctx, "GET", b.runURL(run), nil, &result); err != nil {
		return Status{}, err
	}
	if result.Data.Status == nil || *result.Data.Status < 0 || *result.Data.Status > 4 {
		return Status{}, fmt.Errorf("Bitrise returned an unknown build status")
	}
	code := *result.Data.Status
	s := Status{State: result.Data.Text, Done: code != 0, Success: code == 1}
	if s.State == "" {
		s.State = fmt.Sprintf("status %d", code)
	}
	// Bitrise artifacts may be uploaded while the build is running, but the IPA
	// flow waits for successful completion before fetching its artifact list.
	if !s.Success {
		return s, nil
	}
	next := ""
	seen := map[string]bool{}
	for {
		var artifacts struct {
			Data []struct {
				ID   string `json:"slug"`
				Name string `json:"title"`
				Size int64  `json:"file_size_bytes"`
			} `json:"data"`
			Paging struct {
				Next string `json:"next"`
			} `json:"paging"`
		}
		endpoint := b.runURL(run) + "/artifacts?limit=50"
		if next != "" {
			endpoint += "&next=" + url.QueryEscape(next)
		}
		if err := b.api.request(ctx, "GET", endpoint, nil, &artifacts); err != nil {
			return Status{}, err
		}
		for _, a := range artifacts.Data {
			// Unsigned IPAs are uploaded as generic ZIPs to avoid Bitrise's
			// provisioning-profile parser. The file is already the IPA, without
			// an extra ZIP wrapper; normal IPA validation still checks its bytes.
			if strings.HasSuffix(a.Name, ".ipa.zip") {
				a.Name = strings.TrimSuffix(a.Name, ".zip")
			}
			s.Artifacts = append(s.Artifacts, Artifact{ID: a.ID, Name: a.Name, Size: a.Size})
		}
		next = artifacts.Paging.Next
		if next == "" {
			break
		}
		if seen[next] {
			return Status{}, fmt.Errorf("Bitrise artifact pagination repeated a cursor")
		}
		seen[next] = true
	}
	return s, nil
}
func (b *Bitrise) Download(ctx context.Context, run Run, a Artifact, w io.Writer) (int64, error) {
	var result struct {
		Data struct {
			URL string `json:"expiring_download_url"`
		} `json:"data"`
	}
	if err := b.api.request(ctx, "GET", b.runURL(run)+"/artifacts/"+url.PathEscape(a.ID), nil, &result); err != nil {
		return 0, err
	}
	return downloadURL(ctx, result.Data.URL, w, b.api.http.Transport)
}
func (b *Bitrise) Cancel(ctx context.Context, run Run) error {
	// Aborting an already completed build is an API error, so check first.
	s, err := b.Status(ctx, run)
	if err == nil && s.Done {
		return nil
	}
	body := map[string]any{"abort_reason": "Cancelled by Builder", "abort_with_success": false, "skip_git_status_report": false, "skip_notifications": true}
	err = b.api.request(ctx, "POST", b.runURL(run)+"/abort", body, nil)
	if err != nil {
		if s, serr := b.Status(ctx, run); serr == nil && s.Done {
			return nil
		}
	}
	return err
}
