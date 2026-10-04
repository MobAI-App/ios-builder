package ci

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/MobAI-App/ios-builder/internal/config"
)

// Record is one provider build as the history commands see it: its state,
// timestamps, and the variables Builder sent with it, which carry the build ID.
type Record struct {
	ID, URL, Workflow string
	State             string // the provider's own word
	Done, Success     bool
	Cancelled         bool
	Reason            string // why it stopped, when the provider says
	Variables         map[string]string
	CreatedAt         time.Time
	StartedAt         time.Time
	FinishedAt        time.Time
	Artifacts         []Artifact
	Steps             []Step
}

// Step is one build step. Only Codemagic reports steps through its API.
type Step struct {
	Name, State        string
	Done, Failed       bool
	StartedAt, EndedAt time.Time
	LogURL             string
}

// History reads past and running builds of a provider.
type History interface {
	Builds(ctx context.Context, workflow string, limit int) ([]Record, error)
	Build(ctx context.Context, id string) (Record, error)
}

// Options points a provider at other hosts and a transport (tests, proxies).
// Empty fields keep the production values.
type Options struct {
	APIURL    string // Codemagic dispatch/legacy API, Bitrise API
	StatusURL string // Codemagic v3 API
	Transport http.RoundTripper
}

// NewCodemagicWith is NewCodemagic with Options applied.
func NewCodemagicWith(cfg config.CIConfig, token string, o Options) *Codemagic {
	c := NewCodemagic(cfg, token)
	if o.APIURL != "" {
		c.dispatchURL = strings.TrimSuffix(o.APIURL, "/")
	}
	if o.StatusURL != "" {
		c.statusURL = strings.TrimSuffix(o.StatusURL, "/")
	}
	if o.Transport != nil {
		c.api.http.Transport = o.Transport
	}
	return c
}

// NewBitriseWith is NewBitrise with Options applied.
func NewBitriseWith(cfg config.CIConfig, token string, o Options) *Bitrise {
	b := NewBitrise(cfg, token)
	if o.APIURL != "" {
		b.baseURL = strings.TrimSuffix(o.APIURL, "/")
	}
	if o.Transport != nil {
		b.api.http.Transport = o.Transport
	}
	return b
}

func sortNewestFirst(records []Record) {
	sort.SliceStable(records, func(i, j int) bool { return records[i].CreatedAt.After(records[j].CreatedAt) })
}

// codemagicBuild is a build in Codemagic's builds API.
type codemagicBuild struct {
	ID          string    `json:"_id"`
	WorkflowID  string    `json:"workflowId"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"createdAt"`
	StartedAt   time.Time `json:"startedAt"`
	FinishedAt  time.Time `json:"finishedAt"`
	Message     string    `json:"message"`
	Environment struct {
		Variables map[string]any `json:"variables"`
	} `json:"environment"`
	Artefacts []struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
	} `json:"artefacts"`
	BuildActions []struct {
		Name       string    `json:"name"`
		Status     string    `json:"status"`
		StartedAt  time.Time `json:"startedAt"`
		FinishedAt time.Time `json:"finishedAt"`
		LogURL     string    `json:"logUrl"`
	} `json:"buildActions"`
}

func (c *Codemagic) record(b *codemagicBuild) Record {
	r := Record{ID: b.ID, Workflow: b.WorkflowID, State: b.Status, CreatedAt: b.CreatedAt, StartedAt: b.StartedAt,
		FinishedAt: b.FinishedAt, Reason: b.Message, Variables: map[string]string{},
		URL: "https://codemagic.io/app/" + url.PathEscape(c.config.AppID) + "/build/" + url.PathEscape(b.ID)}
	r.Done, r.Success = codemagicDone(b.Status)
	r.Cancelled = b.Status == "canceled"
	for k, v := range b.Environment.Variables {
		if s, ok := v.(string); ok {
			r.Variables[k] = s
		}
	}
	for _, a := range b.Artefacts {
		r.Artifacts = append(r.Artifacts, Artifact{Name: a.Name, Size: a.Size})
	}
	for _, a := range b.BuildActions {
		done, _ := codemagicDone(a.Status)
		if a.Status == "success" {
			done = true
		}
		r.Steps = append(r.Steps, Step{Name: a.Name, State: a.Status, Done: done, Failed: a.Status == "failed",
			StartedAt: a.StartedAt, EndedAt: a.FinishedAt, LogURL: a.LogURL})
	}
	return r
}

// codemagicDone maps a Codemagic build (or step) status onto done/success.
func codemagicDone(status string) (done, success bool) {
	switch status {
	case "finished":
		return true, true
	case "failed", "canceled", "timeout", "skipped":
		return true, false
	}
	return false, false
}

// Builds lists the app's builds of one workflow, newest first.
func (c *Codemagic) Builds(ctx context.Context, workflow string, limit int) ([]Record, error) {
	q := url.Values{}
	q.Set("appId", c.config.AppID)
	q.Set("workflowId", workflow)
	var result struct {
		Builds []codemagicBuild `json:"builds"`
	}
	if err := c.api.request(ctx, "GET", c.dispatchURL+"/builds?"+q.Encode(), nil, &result); err != nil {
		return nil, err
	}
	var records []Record
	for i := range result.Builds {
		b := &result.Builds[i]
		// The workflowId filter is the API's; checking it keeps other
		// workflows out should the filter be ignored.
		if b.WorkflowID != "" && b.WorkflowID != workflow {
			continue
		}
		records = append(records, c.record(b))
	}
	sortNewestFirst(records)
	if limit > 0 && len(records) > limit {
		records = records[:limit]
	}
	return records, nil
}

// Build reads one build by its Codemagic ID.
func (c *Codemagic) Build(ctx context.Context, id string) (Record, error) {
	var result struct {
		Build codemagicBuild `json:"build"`
	}
	if err := c.api.request(ctx, "GET", c.dispatchURL+"/builds/"+url.PathEscape(id), nil, &result); err != nil {
		return Record{}, err
	}
	if result.Build.ID == "" {
		return Record{}, fmt.Errorf("Codemagic build %s not found", id)
	}
	return c.record(&result.Build), nil
}

// StepLog reads one step's log. The token goes only to the API host: a log
// URL elsewhere is fetched without it, as a pre-signed link.
func (c *Codemagic) StepLog(ctx context.Context, s *Step, w io.Writer) error {
	if s.LogURL == "" {
		return fmt.Errorf("Codemagic reported no log for step %q", s.Name)
	}
	u, err := url.Parse(s.LogURL)
	api, _ := url.Parse(c.dispatchURL)
	if err != nil || api == nil || u.Host != api.Host || u.Scheme != api.Scheme {
		_, err := downloadURL(ctx, s.LogURL, w, c.api.http.Transport)
		return err
	}
	return c.api.text(ctx, s.LogURL, w)
}

// text streams a GET response body that is not JSON.
func (a apiClient) text(ctx context.Context, endpoint string, w io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return fmt.Errorf("invalid API request")
	}
	req.Header.Set(a.header, a.token)
	client := *a.http
	client.Timeout = 0 // a long log may take longer than one API call
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &APIError{}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{StatusCode: resp.StatusCode, message: providerMessage(resp)}
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

// bitriseBuild is a build in Bitrise's builds API.
type bitriseBuild struct {
	Slug        string     `json:"slug"`
	Status      *int       `json:"status"`
	StatusText  string     `json:"status_text"`
	Workflow    string     `json:"triggered_workflow"`
	TriggeredAt time.Time  `json:"triggered_at"`
	StartedAt   *time.Time `json:"started_on_worker_at"`
	FinishedAt  *time.Time `json:"finished_at"`
	AbortReason string     `json:"abort_reason"`
	Params      struct {
		Environments []struct {
			Key   string `json:"mapped_to"`
			Value string `json:"value"`
		} `json:"environments"`
	} `json:"original_build_params"`
}

func bitriseRecord(b *bitriseBuild) (Record, error) {
	if b.Status == nil || *b.Status < 0 || *b.Status > 4 {
		return Record{}, &APIError{message: "Bitrise returned an unknown build status"}
	}
	code := *b.Status
	r := Record{ID: b.Slug, Workflow: b.Workflow, State: b.StatusText, Done: code != 0, Success: code == 1,
		Cancelled: code == 3 || code == 4, Reason: b.AbortReason, CreatedAt: b.TriggeredAt, Variables: map[string]string{},
		URL: "https://app.bitrise.io/build/" + url.PathEscape(b.Slug)}
	if r.State == "" {
		r.State = "status " + strconv.Itoa(code)
	}
	if b.StartedAt != nil {
		r.StartedAt = *b.StartedAt
	}
	if b.FinishedAt != nil {
		r.FinishedAt = *b.FinishedAt
	}
	for _, e := range b.Params.Environments {
		r.Variables[e.Key] = e.Value
	}
	return r, nil
}

// Builds lists the app's builds of one workflow, newest first.
func (b *Bitrise) Builds(ctx context.Context, workflow string, limit int) ([]Record, error) {
	if limit <= 0 {
		limit = 50
	}
	var records []Record
	next := ""
	seen := map[string]bool{}
	for len(records) < limit {
		q := url.Values{}
		q.Set("workflow", workflow)
		q.Set("limit", strconv.Itoa(min(limit-len(records), 50)))
		if next != "" {
			q.Set("next", next)
		}
		var page struct {
			Data   []bitriseBuild `json:"data"`
			Paging struct {
				Next string `json:"next"`
			} `json:"paging"`
		}
		if err := b.api.request(ctx, "GET", b.buildsURL()+"?"+q.Encode(), nil, &page); err != nil {
			return nil, err
		}
		for i := range page.Data {
			item := &page.Data[i]
			if item.Workflow != "" && item.Workflow != workflow {
				continue
			}
			r, err := bitriseRecord(item)
			if err != nil {
				return nil, err
			}
			records = append(records, r)
		}
		next = page.Paging.Next
		if next == "" || len(page.Data) == 0 {
			break
		}
		if seen[next] {
			return nil, fmt.Errorf("Bitrise build pagination repeated a cursor")
		}
		seen[next] = true
	}
	sortNewestFirst(records)
	if len(records) > limit {
		records = records[:limit]
	}
	return records, nil
}

// Build reads one build by its Bitrise slug.
func (b *Bitrise) Build(ctx context.Context, id string) (Record, error) {
	var result struct {
		Data bitriseBuild `json:"data"`
	}
	if err := b.api.request(ctx, "GET", b.runURL(Run{ID: id}), nil, &result); err != nil {
		return Record{}, err
	}
	if result.Data.Slug == "" {
		result.Data.Slug = id
	}
	return bitriseRecord(&result.Data)
}

// BitriseLog is one read of a build's log: the chunks so far, and once the
// log is archived, the URL of the whole text.
type BitriseLog struct {
	Chunks   []LogChunk
	Archived bool
	RawURL   string
}

// LogChunk is a piece of a Bitrise log; Position orders them.
type LogChunk struct {
	Text     string `json:"chunk"`
	Position int    `json:"position"`
}

// Log reads the build log as it stands.
func (b *Bitrise) Log(ctx context.Context, id string) (BitriseLog, error) {
	var result struct {
		Chunks   []LogChunk `json:"log_chunks"`
		Archived bool       `json:"is_archived"`
		RawURL   string     `json:"expiring_raw_log_url"`
	}
	if err := b.api.request(ctx, "GET", b.runURL(Run{ID: id})+"/log", nil, &result); err != nil {
		return BitriseLog{}, err
	}
	sort.SliceStable(result.Chunks, func(i, j int) bool { return result.Chunks[i].Position < result.Chunks[j].Position })
	return BitriseLog{Chunks: result.Chunks, Archived: result.Archived, RawURL: result.RawURL}, nil
}

// RawLog copies an archived log from its expiring URL, without credentials.
func (b *Bitrise) RawLog(ctx context.Context, rawURL string, w io.Writer) error {
	_, err := downloadURL(ctx, rawURL, w, b.api.http.Transport)
	return err
}
