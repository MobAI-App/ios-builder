package asc

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
	"strings"
	"time"
)

// DefaultBaseURL is the production App Store Connect API endpoint.
const DefaultBaseURL = "https://api.appstoreconnect.apple.com"

// Client talks to the App Store Connect API.
type Client struct {
	baseURL    string
	http       *http.Client
	upload     *http.Client
	tokens     *tokenSource
	retryDelay time.Duration
	maxRetries int
	// sleep waits between retries and polls; tests replace it.
	sleep func(context.Context, time.Duration) error
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL points the client at another server, e.g. a test server.
func WithBaseURL(baseURL string) Option {
	return func(c *Client) { c.baseURL = strings.TrimRight(baseURL, "/") }
}

// WithHTTPClient replaces the HTTP client used for API calls.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.http = h }
}

// WithRetryDelay sets the base delay of the exponential backoff on 429/5xx.
func WithRetryDelay(d time.Duration) Option {
	return func(c *Client) { c.retryDelay = d }
}

// NewClient validates the credentials and returns a client. No network call is made.
func NewClient(creds Credentials, opts ...Option) (*Client, error) {
	tokens, err := newTokenSource(creds)
	if err != nil {
		return nil, fmt.Errorf("App Store Connect credentials: %w", err)
	}
	c := &Client{
		baseURL: DefaultBaseURL,
		http:    &http.Client{Timeout: 60 * time.Second},
		// Chunk PUTs go to Apple's storage, not the API; large chunks on a
		// slow uplink can legitimately take minutes.
		upload:     &http.Client{Timeout: 15 * time.Minute},
		tokens:     tokens,
		retryDelay: time.Second,
		maxRetries: 3,
		sleep:      sleep,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// Error is an error response from App Store Connect.
type Error struct {
	StatusCode int
	Method     string
	Path       string
	Errors     []ErrorDetail
	RetryAfter time.Duration
}

// ErrorDetail is one entry of the JSON:API errors array.
type ErrorDetail struct {
	ID     string       `json:"id,omitempty"`
	Status string       `json:"status,omitempty"`
	Code   string       `json:"code,omitempty"`
	Title  string       `json:"title,omitempty"`
	Detail string       `json:"detail,omitempty"`
	Source *ErrorSource `json:"source,omitempty"`
}

// ErrorSource points at the request field or parameter an error refers to.
type ErrorSource struct {
	Pointer   string `json:"pointer,omitempty"`
	Parameter string `json:"parameter,omitempty"`
}

// Error renders the status and every ASC error on one line.
func (e *Error) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "App Store Connect %s %s: HTTP %d", e.Method, e.Path, e.StatusCode)
	for i, d := range e.Errors {
		if i == 0 {
			b.WriteString(": ")
		} else {
			b.WriteString("; ")
		}
		b.WriteString(d.String())
	}
	return b.String()
}

// String renders one error as "CODE: title (detail)".
func (d ErrorDetail) String() string {
	var parts []string
	if d.Code != "" {
		parts = append(parts, d.Code)
	}
	if d.Title != "" {
		parts = append(parts, d.Title)
	}
	s := strings.Join(parts, ": ")
	if d.Detail != "" && d.Detail != d.Title {
		if s != "" {
			s += " (" + d.Detail + ")"
		} else {
			s = d.Detail
		}
	}
	if d.Source != nil && d.Source.Pointer != "" {
		s += " [" + d.Source.Pointer + "]"
	}
	return strings.ReplaceAll(s, "\n", " ")
}

// IsStatus reports whether err is an App Store Connect error with the given HTTP status.
func IsStatus(err error, status int) bool {
	var e *Error
	return errors.As(err, &e) && e.StatusCode == status
}

// Get performs a GET. path is relative to the base URL ("/v1/apps") or an
// absolute URL such as a pagination link; query is appended when non-nil.
func (c *Client) Get(ctx context.Context, path string, query url.Values, out any) error {
	return c.do(ctx, http.MethodGet, path, query, nil, out)
}

// Post performs a POST with a JSON body.
func (c *Client) Post(ctx context.Context, path string, body, out any) error {
	return c.do(ctx, http.MethodPost, path, nil, body, out)
}

// Patch performs a PATCH with a JSON body.
func (c *Client) Patch(ctx context.Context, path string, body, out any) error {
	return c.do(ctx, http.MethodPatch, path, nil, body, out)
}

// Delete performs a DELETE, with an optional JSON body (relationship removals).
func (c *Client) Delete(ctx context.Context, path string, body any) error {
	return c.do(ctx, http.MethodDelete, path, nil, body, nil)
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
	}
	for attempt := 0; ; attempt++ {
		err := c.once(ctx, method, path, query, payload, out)
		var apiErr *Error
		if err == nil || attempt >= c.maxRetries || !errors.As(err, &apiErr) || !retryable(method, apiErr.StatusCode) {
			return err
		}
		delay := c.retryDelay << attempt
		if apiErr.RetryAfter > delay {
			delay = apiErr.RetryAfter
		}
		if err := c.sleep(ctx, delay); err != nil {
			return err
		}
	}
}

// sleep waits for d or until ctx is done.
func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// retryable: 429 was not processed, so any method may retry. A 5xx on a POST
// may have created the resource already, so only idempotent methods retry.
func retryable(method string, status int) bool {
	if status == http.StatusTooManyRequests {
		return true
	}
	return status >= 500 && status <= 599 && method != http.MethodPost
}

func (c *Client) once(ctx context.Context, method, path string, query url.Values, payload []byte, out any) error {
	target := path
	if !strings.HasPrefix(path, "http://") && !strings.HasPrefix(path, "https://") {
		target = c.baseURL + path
	}
	if len(query) > 0 {
		sep := "?"
		if strings.Contains(target, "?") {
			sep = "&"
		}
		target += sep + query.Encode()
	}
	var bodyReader io.Reader
	if payload != nil {
		bodyReader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, bodyReader)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	token, err := c.tokens.Token()
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("App Store Connect request failed: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return decodeError(method, path, resp, data)
	}
	if out != nil && len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

func decodeError(method, path string, resp *http.Response, data []byte) *Error {
	e := &Error{StatusCode: resp.StatusCode, Method: method, Path: strings.SplitN(path, "?", 2)[0]}
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if secs, err := strconv.Atoi(ra); err == nil && secs > 0 {
			e.RetryAfter = time.Duration(secs) * time.Second
		}
	}
	var body struct {
		Errors []ErrorDetail `json:"errors"`
	}
	if json.Unmarshal(data, &body) == nil && len(body.Errors) > 0 {
		e.Errors = body.Errors
		return e
	}
	if text := strings.TrimSpace(string(data)); text != "" {
		if len(text) > 200 {
			text = text[:200] + "..."
		}
		e.Errors = []ErrorDetail{{Title: http.StatusText(resp.StatusCode), Detail: text}}
	}
	return e
}
