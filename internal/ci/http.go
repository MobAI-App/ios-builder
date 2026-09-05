package ci

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type apiClient struct {
	http          *http.Client
	token, header string
	retryDelay    time.Duration
}

func newAPI(token, header string) apiClient {
	return apiClient{retryDelay: time.Second, http: &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}, token: token, header: header}
}

// APIError deliberately excludes response bodies, URLs and credentials.
type APIError struct {
	StatusCode int
	retryAfter time.Duration
}

func (e *APIError) Error() string {
	if e.StatusCode == 0 {
		return "CI API request failed; check connectivity and the provider dashboard"
	}
	return fmt.Sprintf("CI API returned HTTP %d; check credentials, app access, and account allowance", e.StatusCode)
}

// DispatchRejected recognizes only explicit request/access rejections. Timeouts,
// conflicts and server failures remain ambiguous: a job may have been accepted.
func DispatchRejected(err error) bool {
	var e *APIError
	if !errors.As(err, &e) {
		return false
	}
	switch e.StatusCode {
	case 400, 401, 402, 403, 404, 405, 422:
		return true
	default:
		return false
	}
}

func (a apiClient) request(ctx context.Context, method, endpoint string, body, dest any) error {
	for attempt := 0; ; attempt++ {
		err := a.requestOnce(ctx, method, endpoint, body, dest)
		var apiErr *APIError
		if err == nil || method != http.MethodGet || attempt >= 3 || !errors.As(err, &apiErr) {
			return err
		}
		code := apiErr.StatusCode
		if code != 0 && code != 408 && code != 429 && (code < 500 || code > 599) {
			return err
		}
		delay := a.retryDelay * time.Duration(1<<attempt)
		if apiErr.retryAfter > delay {
			delay = apiErr.retryAfter
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func retryAfter(value string) time.Duration {
	if seconds, err := strconv.ParseInt(value, 10, 32); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if date, err := http.ParseTime(value); err == nil {
		return max(time.Until(date), 0)
	}
	return 0
}

func (a apiClient) requestOnce(ctx context.Context, method, endpoint string, body, dest any) error {
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
		return &APIError{}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{StatusCode: resp.StatusCode, retryAfter: retryAfter(resp.Header.Get("Retry-After"))}
	}
	if dest == nil {
		return nil
	}
	responseData, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &APIError{}
	}
	if err := json.Unmarshal(responseData, dest); err != nil {
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
