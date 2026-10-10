package ci

import (
	"context"
	"fmt"
	"net/url"
	"sort"
)

// SecretStore keeps build secrets on a provider. Values go in and never come
// back out: List returns names only.
type SecretStore interface {
	List(ctx context.Context) ([]string, error)
	Set(ctx context.Context, name, value string) error
	Delete(ctx context.Context, name string) (bool, error)
}

// CodemagicVariableGroup is the app variable group the generated
// codemagic.yaml imports (environment.groups: [builder]).
const CodemagicVariableGroup = "builder"

// CodemagicSecrets stores secrets as secure variables in the app's
// CodemagicVariableGroup, created on the first Set.
type CodemagicSecrets struct {
	api     apiClient
	appID   string
	baseURL string
}

// NewCodemagicSecrets talks to Codemagic's v3 API for one app.
func NewCodemagicSecrets(appID, token string) *CodemagicSecrets {
	return NewCodemagicSecretsAt("https://codemagic.io/api/v3", appID, token)
}

// NewCodemagicSecretsAt is NewCodemagicSecrets against another API root (tests).
func NewCodemagicSecretsAt(baseURL, appID, token string) *CodemagicSecrets {
	return &CodemagicSecrets{api: newAPI(token, "x-auth-token"), appID: appID, baseURL: baseURL}
}

type codemagicPage struct {
	CurrentPage int `json:"current_page"`
	TotalPages  int `json:"total_pages"`
}

// pages calls fetch for page 1, 2, ... until the last page; fetch returns
// the page metadata it decoded.
func pages(fetch func(page int) (codemagicPage, error)) error {
	for page := 1; ; page++ {
		meta, err := fetch(page)
		if err != nil {
			return err
		}
		if page >= meta.TotalPages || page > 1000 {
			return nil
		}
	}
}

// group finds the builder variable group's ID; empty when the app has none.
func (c *CodemagicSecrets) group(ctx context.Context) (string, error) {
	var id string
	err := pages(func(page int) (codemagicPage, error) {
		var res struct {
			codemagicPage
			Data []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"data"`
		}
		endpoint := fmt.Sprintf("%s/apps/%s/variable-groups?page_size=100&page=%d", c.baseURL, url.PathEscape(c.appID), page)
		if err := c.api.request(ctx, "GET", endpoint, nil, &res); err != nil {
			return res.codemagicPage, err
		}
		for _, g := range res.Data {
			if g.Name == CodemagicVariableGroup && id == "" {
				id = g.ID
			}
		}
		if id != "" {
			res.TotalPages = page // found; stop paging
		}
		return res.codemagicPage, nil
	})
	return id, err
}

type codemagicVariable struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (c *CodemagicSecrets) variables(ctx context.Context, group string) ([]codemagicVariable, error) {
	var vars []codemagicVariable
	err := pages(func(page int) (codemagicPage, error) {
		var res struct {
			codemagicPage
			Data []codemagicVariable `json:"data"`
		}
		endpoint := fmt.Sprintf("%s/variable-groups/%s/variables?page_size=100&page=%d", c.baseURL, url.PathEscape(group), page)
		if err := c.api.request(ctx, "GET", endpoint, nil, &res); err != nil {
			return res.codemagicPage, err
		}
		vars = append(vars, res.Data...)
		return res.codemagicPage, nil
	})
	return vars, err
}

func (c *CodemagicSecrets) find(ctx context.Context, name string) (group, id string, err error) {
	group, err = c.group(ctx)
	if err != nil || group == "" {
		return group, "", err
	}
	vars, err := c.variables(ctx, group)
	if err != nil {
		return group, "", err
	}
	for _, v := range vars {
		if v.Name == name {
			return group, v.ID, nil
		}
	}
	return group, "", nil
}

// List returns the variable names of the builder group, sorted.
func (c *CodemagicSecrets) List(ctx context.Context) ([]string, error) {
	group, err := c.group(ctx)
	if err != nil || group == "" {
		return nil, err
	}
	vars, err := c.variables(ctx, group)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(vars))
	for _, v := range vars {
		names = append(names, v.Name)
	}
	sort.Strings(names)
	return names, nil
}

// Set updates the variable in place, or adds it to the group as secure,
// creating the group when the app has none.
func (c *CodemagicSecrets) Set(ctx context.Context, name, value string) error {
	group, id, err := c.find(ctx, name)
	if err != nil {
		return err
	}
	if id != "" {
		body := map[string]any{"value": value, "secure": true}
		return c.api.request(ctx, "PATCH", c.baseURL+"/variable-groups/"+url.PathEscape(group)+"/variables/"+url.PathEscape(id), body, nil)
	}
	if group == "" {
		var res struct {
			Data struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		body := map[string]string{"name": CodemagicVariableGroup}
		if err := c.api.request(ctx, "POST", c.baseURL+"/apps/"+url.PathEscape(c.appID)+"/variable-groups", body, &res); err != nil {
			return err
		}
		if res.Data.ID == "" {
			return fmt.Errorf("Codemagic created the %s variable group but returned no ID", CodemagicVariableGroup)
		}
		group = res.Data.ID
	}
	body := map[string]any{"secure": true, "variables": []map[string]string{{"name": name, "value": value}}}
	return c.api.request(ctx, "POST", c.baseURL+"/variable-groups/"+url.PathEscape(group)+"/variables", body, nil)
}

// Delete removes the variable from the builder group.
func (c *CodemagicSecrets) Delete(ctx context.Context, name string) (bool, error) {
	group, id, err := c.find(ctx, name)
	if err != nil || id == "" {
		return false, err
	}
	return true, c.api.request(ctx, "DELETE", c.baseURL+"/variable-groups/"+url.PathEscape(group)+"/variables/"+url.PathEscape(id), nil, nil)
}

// BitriseSecrets stores secrets as Bitrise app secrets.
type BitriseSecrets struct {
	api     apiClient
	appSlug string
	baseURL string
}

// NewBitriseSecrets talks to the Bitrise API for one app.
func NewBitriseSecrets(appSlug, token string) *BitriseSecrets {
	return NewBitriseSecretsAt("https://api.bitrise.io/v0.1", appSlug, token)
}

// NewBitriseSecretsAt is NewBitriseSecrets against another API root (tests).
func NewBitriseSecretsAt(baseURL, appSlug, token string) *BitriseSecrets {
	return &BitriseSecrets{api: newAPI(token, "Authorization"), appSlug: appSlug, baseURL: baseURL}
}

func (b *BitriseSecrets) secretsURL() string {
	return b.baseURL + "/apps/" + url.PathEscape(b.appSlug) + "/secrets"
}

// List returns the app's secret names, sorted.
func (b *BitriseSecrets) List(ctx context.Context) ([]string, error) {
	var res struct {
		Secrets []struct {
			Name string `json:"name"`
		} `json:"secrets"`
	}
	if err := b.api.request(ctx, "GET", b.secretsURL(), nil, &res); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(res.Secrets))
	for _, s := range res.Secrets {
		names = append(names, s.Name)
	}
	sort.Strings(names)
	return names, nil
}

// Set updates an existing secret's value, leaving its flags as they are (a
// protected secret accepts nothing else), or creates it protected, with
// variable expansion and pull-request exposure off: a literal value only
// builds Builder dispatches can read.
func (b *BitriseSecrets) Set(ctx context.Context, name, value string) error {
	names, err := b.List(ctx)
	if err != nil {
		return err
	}
	if i := sort.SearchStrings(names, name); i < len(names) && names[i] == name {
		return b.api.request(ctx, "PATCH", b.secretsURL()+"/"+url.PathEscape(name), map[string]any{"value": value}, nil)
	}
	body := map[string]any{"name": name, "value": value, "is_protected": true,
		"expand_in_step_inputs": false, "is_exposed_for_pull_requests": false}
	return b.api.request(ctx, "POST", b.secretsURL(), body, nil)
}

// Delete removes the app secret.
func (b *BitriseSecrets) Delete(ctx context.Context, name string) (bool, error) {
	names, err := b.List(ctx)
	if err != nil {
		return false, err
	}
	if i := sort.SearchStrings(names, name); i == len(names) || names[i] != name {
		return false, nil
	}
	return true, b.api.request(ctx, "DELETE", b.secretsURL()+"/"+url.PathEscape(name), nil, nil)
}
