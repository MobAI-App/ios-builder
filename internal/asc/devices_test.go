package asc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListDevices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/devices" || r.URL.Query().Get("filter[platform]") != "IOS" || r.URL.Query().Get("limit") != "200" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		writeJSON(w, 200, map[string]any{"data": []map[string]any{
			{"type": "devices", "id": "dev-1", "attributes": map[string]any{"name": "Jane's iPhone", "udid": "00008030-000000000000001E", "platform": "IOS", "status": "ENABLED", "deviceClass": "IPHONE", "model": "iPhone 15"}},
			{"type": "devices", "id": "dev-2", "attributes": map[string]any{"name": "Old iPad", "udid": "00008020-000000000000002E", "platform": "IOS", "status": "DISABLED", "deviceClass": "IPAD"}},
		}})
	}))
	defer srv.Close()
	devices, err := newTestClient(t, srv).ListDevices(context.Background(), PlatformIOS)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 2 || devices[0].ID != "dev-1" || devices[0].UDID != "00008030-000000000000001E" || devices[0].Status != DeviceStatusEnabled || devices[0].Model != "iPhone 15" || devices[1].Status != DeviceStatusDisabled || devices[1].DeviceClass != "IPAD" {
		t.Errorf("devices = %+v", devices)
	}
}

func TestRegisterDevice(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/devices" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		writeJSON(w, 201, map[string]any{"data": map[string]any{"type": "devices", "id": "dev-3", "attributes": map[string]any{"name": "iPhone 00001E", "udid": "00008030-000000000000001E", "platform": "IOS", "status": "ENABLED"}}})
	}))
	defer srv.Close()
	d, err := newTestClient(t, srv).RegisterDevice(context.Background(), "iPhone 00001E", "00008030-000000000000001E", PlatformIOS)
	if err != nil {
		t.Fatal(err)
	}
	if d.ID != "dev-3" || d.Status != DeviceStatusEnabled {
		t.Errorf("device = %+v", d)
	}
	attrs := obj(t, body, "data", "attributes")
	if obj(t, body, "data")["type"] != "devices" || attrs["name"] != "iPhone 00001E" || attrs["udid"] != "00008030-000000000000001E" || attrs["platform"] != "IOS" {
		t.Errorf("POST body = %v", body)
	}
}
