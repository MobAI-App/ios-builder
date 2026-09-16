package asc

import (
	"context"
	"net/url"
)

// Device statuses. Disabled devices stay registered and count against the
// yearly limit, but cannot be put in a profile.
const (
	DeviceStatusEnabled  = "ENABLED"
	DeviceStatusDisabled = "DISABLED"
)

// Device is a device registered with the team.
type Device struct {
	ID          string
	Name        string
	UDID        string
	Platform    string
	Status      string
	DeviceClass string
	Model       string
}

type deviceAttributes struct {
	DeviceClass string `json:"deviceClass,omitempty"`
	Model       string `json:"model,omitempty"`
	Name        string `json:"name,omitempty"`
	Platform    string `json:"platform,omitempty"`
	Status      string `json:"status,omitempty"`
	UDID        string `json:"udid,omitempty"`
}

func toDevice(r Resource[deviceAttributes]) Device {
	return Device{ID: r.ID, Name: r.Attributes.Name, UDID: r.Attributes.UDID, Platform: r.Attributes.Platform, Status: r.Attributes.Status, DeviceClass: r.Attributes.DeviceClass, Model: r.Attributes.Model}
}

// ListDevices lists the registered devices of a platform (PlatformIOS),
// enabled and disabled.
func (c *Client) ListDevices(ctx context.Context, platform string) ([]Device, error) {
	rs, err := getAll[deviceAttributes](ctx, c, "/v1/devices", url.Values{"filter[platform]": {platform}})
	if err != nil {
		return nil, err
	}
	devices := make([]Device, 0, len(rs))
	for _, r := range rs {
		devices = append(devices, toDevice(r))
	}
	return devices, nil
}

// RegisterDevice registers a device by UDID. Apple allows 100 devices per
// product family per membership year and never frees a slot on removal.
func (c *Client) RegisterDevice(ctx context.Context, name, udid, platform string) (*Device, error) {
	req := Resource[deviceAttributes]{Type: "devices", Attributes: deviceAttributes{Name: name, UDID: udid, Platform: platform}}
	r, err := post[deviceAttributes, deviceAttributes](ctx, c, "/v1/devices", req)
	if err != nil {
		return nil, err
	}
	d := toDevice(*r)
	return &d, nil
}
