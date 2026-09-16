package asc

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"time"
)

// Certificate types Builder issues. DEVELOPMENT is "Apple Development",
// DISTRIBUTION is "Apple Distribution"; both sign iOS apps (the older
// IOS_DEVELOPMENT / IOS_DISTRIBUTION types are iOS-only variants).
const (
	CertificateTypeDevelopment  = "DEVELOPMENT"
	CertificateTypeDistribution = "DISTRIBUTION"
)

// Certificate is a signing certificate issued to the team.
type Certificate struct {
	ID             string
	Name           string
	DisplayName    string
	SerialNumber   string
	Type           string
	Platform       string
	ExpirationDate time.Time
	// Content is the certificate in DER form, as the portal's .cer download.
	Content []byte
}

type certificateAttributes struct {
	CertificateContent string     `json:"certificateContent,omitempty"`
	DisplayName        string     `json:"displayName,omitempty"`
	ExpirationDate     *time.Time `json:"expirationDate,omitempty"`
	Name               string     `json:"name,omitempty"`
	Platform           string     `json:"platform,omitempty"`
	SerialNumber       string     `json:"serialNumber,omitempty"`
	CertificateType    string     `json:"certificateType,omitempty"`
	CSRContent         string     `json:"csrContent,omitempty"`
}

func toCertificate(r Resource[certificateAttributes]) (Certificate, error) {
	c := Certificate{
		ID:           r.ID,
		Name:         r.Attributes.Name,
		DisplayName:  r.Attributes.DisplayName,
		SerialNumber: r.Attributes.SerialNumber,
		Type:         r.Attributes.CertificateType,
		Platform:     r.Attributes.Platform,
	}
	if r.Attributes.ExpirationDate != nil {
		c.ExpirationDate = *r.Attributes.ExpirationDate
	}
	if r.Attributes.CertificateContent != "" {
		der, err := base64.StdEncoding.DecodeString(r.Attributes.CertificateContent)
		if err != nil {
			return c, fmt.Errorf("certificate %s: decode certificateContent: %w", r.ID, err)
		}
		c.Content = der
	}
	return c, nil
}

// CheckAccess verifies the key with one cheap read-only call. It lists one
// certificate rather than one app: apps?limit=1 answers 200 with an empty
// page for a key of any role, while certificates demands the Certificates,
// Identifiers & Profiles access that signing needs (and every role that can
// upload builds has).
func (c *Client) CheckAccess(ctx context.Context) error {
	_, err := getPage[certificateAttributes](ctx, c, "/v1/certificates", url.Values{"limit": {"1"}})
	return err
}

// ListCertificates lists the team's certificates of one type
// (CertificateTypeDevelopment or CertificateTypeDistribution).
func (c *Client) ListCertificates(ctx context.Context, certificateType string) ([]Certificate, error) {
	rs, err := getAll[certificateAttributes](ctx, c, "/v1/certificates", url.Values{"filter[certificateType]": {certificateType}})
	if err != nil {
		return nil, err
	}
	certs := make([]Certificate, 0, len(rs))
	for _, r := range rs {
		cert, err := toCertificate(r)
		if err != nil {
			return nil, err
		}
		certs = append(certs, cert)
	}
	return certs, nil
}

// CreateCertificate has Apple issue a certificate for a PEM-encoded signing
// request (as written by signing.GenerateKeyAndCSR).
func (c *Client) CreateCertificate(ctx context.Context, certificateType string, csrPEM []byte) (*Certificate, error) {
	req := Resource[certificateAttributes]{Type: "certificates", Attributes: certificateAttributes{CertificateType: certificateType, CSRContent: string(csrPEM)}}
	r, err := post[certificateAttributes, certificateAttributes](ctx, c, "/v1/certificates", req)
	if err != nil {
		return nil, err
	}
	cert, err := toCertificate(*r)
	if err != nil {
		return nil, err
	}
	return &cert, nil
}
