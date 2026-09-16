// Package mobai provides a client for the MobAI HTTP API.
// It supports device listing, app installation, launching, and port forwarding.
package mobai

// Device represents a connected mobile device
type Device struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Platform      string `json:"platform"`
	Model         string `json:"model"`
	OSVersion     string `json:"osVersion"`
	BridgeRunning bool   `json:"bridgeRunning"`
	Virtual       bool   `json:"virtual"`
	Cloud         bool   `json:"cloud"` // lives in a device farm; the ID is a farm handle, not a UDID
}

// InstallAppRequest is the request body for installing an app
type InstallAppRequest struct {
	Path     string `json:"path"`
	Resign   bool   `json:"resign,omitempty"`
	AppleID  string `json:"appleId,omitempty"`
	Password string `json:"password,omitempty"`
}

// InstallAppResponse is the response from installing an app
type InstallAppResponse struct {
	Success bool `json:"success"`
	Data    struct {
		BundleID  string `json:"bundleId"`
		SignedIPA string `json:"signedIpa"`
		TeamID    string `json:"teamId"`
	} `json:"data"`
}

// PortForwardRequest is the request body for forwarding a port
type PortForwardRequest struct {
	DevicePort int `json:"devicePort"`
	HostPort   int `json:"hostPort,omitempty"` // 0 means auto-assign
}

// PortForwardResponse is the response from forwarding a port
type PortForwardResponse struct {
	ID         string `json:"id"`
	HostPort   int    `json:"hostPort"`
	DevicePort int    `json:"devicePort"`
}

// DebugConfig is the configuration sent to the debug WebSocket after connecting
type DebugConfig struct {
	Environment map[string]string `json:"environment,omitempty"`
	Arguments   []string          `json:"arguments,omitempty"`
}

// DebugOutput represents a message from the debug WebSocket stream
type DebugOutput struct {
	Type    string `json:"type"`    // "stdout", "stderr", "error", "exit"
	Data    string `json:"data"`    // Output data
	Message string `json:"message"` // Additional message
}

// APIError represents an error response from the MobAI API
type APIError struct {
	Error   string `json:"error"`
	Message string `json:"message,omitempty"`
	Code    string `json:"code,omitempty"` // e.g. CLAIM_REQUIRED, DEVICE_IN_USE
}

func (e *APIError) String() string {
	if e.Message != "" {
		return e.Message
	}
	return e.Error
}
