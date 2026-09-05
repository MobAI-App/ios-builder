package ci

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

type apiClient struct {
	http          *http.Client
	token, header string
}

func newAPI(token, header string) apiClient {
	return apiClient{http: &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}, token: token, header: header}
}

func (a apiClient) request(ctx context.Context, method, endpoint string, body, dest any) error {
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("invalid API request")
	}
	req.Header.Set(a.header, a.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// URL errors may contain credentials or signed query parameters.
		return fmt.Errorf("CI API request failed; check connectivity and the provider dashboard")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("CI API returned HTTP %d; check credentials, app access, and account allowance", resp.StatusCode)
	}
	if dest == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(dest); err != nil {
		return fmt.Errorf("invalid CI API response")
	}
	return nil
}

// downloadURL uses an unauthenticated client, including every redirect hop.
// Production artifact URLs must use HTTPS; tests use an injected transport.
func downloadURL(ctx context.Context, endpoint string, w io.Writer, transport http.RoundTripper) (int64, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return 0, fmt.Errorf("provider returned an invalid HTTPS artifact URL")
	}
	client := &http.Client{Transport: transport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 || req.URL.Scheme != "https" || req.URL.User != nil {
			return fmt.Errorf("invalid artifact redirect")
		}
		return nil
	}}
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return 0, fmt.Errorf("invalid artifact request")
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, fmt.Errorf("artifact download failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("artifact download returned HTTP %d", resp.StatusCode)
	}
	return io.Copy(w, resp.Body)
}
