package asc

import (
	"context"
	"fmt"
	"net/url"
)

// App is an App Store Connect app record.
type App struct {
	ID            string
	BundleID      string
	Name          string
	SKU           string
	PrimaryLocale string
}

type appAttributes struct {
	BundleID      string `json:"bundleId,omitempty"`
	Name          string `json:"name,omitempty"`
	SKU           string `json:"sku,omitempty"`
	PrimaryLocale string `json:"primaryLocale,omitempty"`
}

func toApp(r Resource[appAttributes]) App {
	return App{ID: r.ID, BundleID: r.Attributes.BundleID, Name: r.Attributes.Name, SKU: r.Attributes.SKU, PrimaryLocale: r.Attributes.PrimaryLocale}
}

// AppByBundleID finds the app record for a bundle identifier.
func (c *Client) AppByBundleID(ctx context.Context, bundleID string) (*App, error) {
	// The filter may match more than the exact ID, so page through and compare.
	apps, err := getAll[appAttributes](ctx, c, "/v1/apps", url.Values{"filter[bundleId]": {bundleID}})
	if err != nil {
		return nil, err
	}
	for _, r := range apps {
		if r.Attributes.BundleID == bundleID {
			app := toApp(r)
			return &app, nil
		}
	}
	return nil, fmt.Errorf("no App Store Connect app has bundle ID %s; create the app record in App Store Connect (My Apps → +) with that bundle ID first, and check the API key can see it", bundleID)
}
