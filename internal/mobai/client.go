package mobai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
)

const (
	// DefaultBaseURL is the default MobAI API base URL
	DefaultBaseURL = "http://localhost:8686"
)

// Client is a MobAI API client
type Client struct {
	httpClient *http.Client
	baseURL    string
	accessKey  string // MobAI API token, required from other hosts, see accessKey

	mu    sync.Mutex
	lease lease // device claim sent with every request, see Claim
}

// NewClient creates a new MobAI API client
func NewClient(baseURL string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		httpClient: &http.Client{},
		baseURL:    strings.TrimSuffix(baseURL, "/"),
		accessKey:  accessKey(),
	}
}

// request performs an HTTP request to the MobAI API
func (c *Client) request(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(jsonBody)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.accessKey != "" {
		req.Header.Set(accessKeyHeader, c.accessKey)
	}
	if token := c.currentLease().token; token != "" {
		req.Header.Set(leaseTokenHeader, token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	return resp, nil
}

// ResponseError is an error response from the MobAI API.
type ResponseError struct {
	StatusCode int
	Code       string // MobAI's machine-readable reason, e.g. CLAIM_REQUIRED
	Message    string
}

func (e *ResponseError) Error() string {
	return fmt.Sprintf("API error (status %d): %s", e.StatusCode, e.Message)
}

func newResponseError(statusCode int, body []byte) *ResponseError {
	e := &ResponseError{StatusCode: statusCode, Message: strings.TrimSpace(string(body))}
	var apiErr APIError
	if json.Unmarshal(body, &apiErr) == nil && apiErr.String() != "" {
		e.Code = apiErr.Code
		e.Message = apiErr.String()
	}
	return e
}

// do performs a request and decodes the response into result
func (c *Client) do(ctx context.Context, method, path string, body, result any) error {
	resp, err := c.request(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return newResponseError(resp.StatusCode, respBody)
	}

	if result != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, result); err != nil {
			return fmt.Errorf("failed to decode response: %w", err)
		}
	}

	return nil
}

// Health checks if MobAI is running
func (c *Client) Health(ctx context.Context) error {
	return c.do(ctx, "GET", "/api/v1/health", nil, nil)
}

// ListDevices returns all connected devices
func (c *Client) ListDevices(ctx context.Context) ([]Device, error) {
	var devices []Device
	if err := c.do(ctx, "GET", "/api/v1/devices", nil, &devices); err != nil {
		return nil, err
	}
	return devices, nil
}

// InstallApp installs an app on the specified device
func (c *Client) InstallApp(ctx context.Context, deviceID string, req InstallAppRequest) (*InstallAppResponse, error) {
	var resp InstallAppResponse
	path := fmt.Sprintf("/api/v1/devices/%s/install-app", deviceID)
	if err := c.do(ctx, "POST", path, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ForwardPort forwards a device port to a host port
func (c *Client) ForwardPort(ctx context.Context, deviceID string, req PortForwardRequest) (*PortForwardResponse, error) {
	var resp PortForwardResponse
	path := fmt.Sprintf("/api/v1/devices/%s/forward", deviceID)
	if err := c.do(ctx, "POST", path, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// DebugStream connects to the debug WebSocket endpoint to launch an app with debugger
// and stream stdout. Returns a channel for debug output and the WebSocket connection
// (which should be closed when done to stop the debugger).
func (c *Client) DebugStream(ctx context.Context, deviceID, bundleID string, config *DebugConfig) (<-chan DebugOutput, *websocket.Conn, error) {
	// Convert http:// to ws://
	wsURL := strings.Replace(c.baseURL, "http://", "ws://", 1)
	wsURL = strings.Replace(wsURL, "https://", "wss://", 1)

	path := fmt.Sprintf("/api/v1/devices/%s/debug?bundleId=%s", deviceID, url.QueryEscape(bundleID))

	header := http.Header{}
	if c.accessKey != "" {
		header.Set(accessKeyHeader, c.accessKey)
	}
	if token := c.currentLease().token; token != "" {
		header.Set(leaseTokenHeader, token)
	}

	conn, resp, err := websocket.DefaultDialer.DialContext(ctx, wsURL+path, header)
	if err != nil {
		// A refused handshake carries MobAI's JSON error, such as CLAIM_REQUIRED.
		if resp != nil && resp.StatusCode >= 400 {
			body, _ := io.ReadAll(resp.Body)
			return nil, nil, fmt.Errorf("websocket connect: %w", newResponseError(resp.StatusCode, body))
		}
		return nil, nil, fmt.Errorf("websocket connect: %w", err)
	}

	// Send config immediately after connecting
	cfg := config
	if cfg == nil {
		cfg = &DebugConfig{}
	}
	if err := conn.WriteJSON(cfg); err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("send config: %w", err)
	}

	outputChan := make(chan DebugOutput, 100)

	go func() {
		defer close(outputChan)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			var output DebugOutput
			if err := conn.ReadJSON(&output); err != nil {
				if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
					return
				}
				if ctx.Err() != nil {
					return
				}
				return
			}

			select {
			case outputChan <- output:
			case <-ctx.Done():
				return
			}
		}
	}()

	return outputChan, conn, nil
}
