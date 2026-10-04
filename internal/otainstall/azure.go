package otainstall

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/MobAI-App/ios-builder/internal/config"
)

const (
	// azureVersion is the storage service version the SAS is signed for and
	// requests are made with.
	azureVersion = "2022-11-02"
	// azureMarker is the metadata name every Builder blob carries (metadata
	// names must be C# identifiers, so no dash).
	azureMarker = "iosbuilder"
	// azureOpTTL bounds the SAS of Builder's own requests; an IPA upload on a
	// slow link must finish within it.
	azureOpTTL = 2 * time.Hour
)

// AzureOptions configures NewAzure.
type AzureOptions struct {
	Account   string
	Container string
	// Key is the storage account key (base64), which signs service SAS tokens.
	Key string
	// Endpoint is the blob endpoint, default https://<account>.blob.core.windows.net
	// (Azurite: http://127.0.0.1:10000/devstoreaccount1).
	Endpoint   string
	Prefix     string
	TTL        time.Duration
	HTTPClient *http.Client
}

// NewAzure returns the bucket backend for Azure Blob Storage. Every request,
// Builder's own included, carries a service SAS signed with the account key,
// so one signer covers upload, delete, list and the install links.
func NewAzure(opts *AzureOptions) (*Bucket, error) {
	if opts.Account == "" || opts.Container == "" {
		return nil, errors.New(`the azure backend needs an account and a container: set "distribute": {"account": "...", "container": "..."} in builder.json (or AZURE_STORAGE_ACCOUNT)`)
	}
	key, err := base64.StdEncoding.DecodeString(opts.Key)
	if err != nil || len(key) == 0 {
		return nil, errors.New("the azure backend needs the storage account key in AZURE_STORAGE_KEY (base64), or AZURE_STORAGE_CONNECTION_STRING")
	}
	endpoint := opts.Endpoint
	if endpoint == "" {
		endpoint = "https://" + opts.Account + ".blob.core.windows.net"
	}
	if u, err := url.Parse(endpoint); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("azure endpoint %q is not an http(s) URL", endpoint)
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{}
	}
	store := &azureStore{account: opts.Account, container: opts.Container, key: key, base: strings.TrimRight(endpoint, "/") + "/" + uriEncode(opts.Container, true), http: client, now: time.Now}
	return newBucket(store, opts.Prefix, opts.TTL)
}

type azureStore struct {
	account, container string
	key                []byte
	base               string // endpoint/container
	http               *http.Client
	now                func() time.Time
}

func (a *azureStore) name(key string) string {
	return "azure://" + a.account + "/" + a.container + "/" + key
}

// sas is a service SAS query string. blob empty signs the container (sr=c),
// else that blob (sr=b). Only what a query parser would misread is escaped
// (the signature's +): se keeps its colons and sig its / and =, because the
// install link escapes every % again and each one costs three characters of
// QR code.
func (a *azureStore) sas(blob, perms string, expiry time.Time) string {
	resource, canonical := "c", "/blob/"+a.account+"/"+a.container
	if blob != "" {
		resource, canonical = "b", canonical+"/"+blob
	}
	se := expiry.UTC().Format(time.RFC3339)
	toSign := strings.Join([]string{
		perms, "", se, canonical, "", "", "", azureVersion, resource, "", "", "", "", "", "", "",
	}, "\n")
	m := hmac.New(sha256.New, a.key)
	m.Write([]byte(toSign))
	sig := base64.StdEncoding.EncodeToString(m.Sum(nil))
	return "sv=" + azureVersion + "&se=" + se + "&sr=" + resource + "&sp=" + perms + "&sig=" + strings.ReplaceAll(sig, "+", "%2B")
}

func (a *azureStore) blobURL(key string) string { return a.base + "/" + uriEncode(key, false) }

func (a *azureStore) do(ctx context.Context, method, rawURL string, header http.Header, body io.Reader, size int64) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, err
	}
	for k, v := range header {
		req.Header[k] = v
	}
	req.Header.Set("X-Ms-Version", azureVersion)
	if body != nil {
		req.ContentLength = size
	}
	return a.http.Do(req)
}

func (a *azureStore) put(ctx context.Context, key, contentType string, body io.Reader, size int64) error {
	h := http.Header{"Content-Type": {contentType}, "X-Ms-Blob-Type": {"BlockBlob"}, "X-Ms-Meta-" + azureMarker: {markerValue}}
	resp, err := a.do(ctx, "PUT", a.blobURL(key)+"?"+a.sas(key, "cw", a.now().Add(azureOpTTL)), h, body, size)
	if err != nil {
		return err
	}
	return azureError(resp, "PUT", a.name(key))
}

func (a *azureStore) delete(ctx context.Context, key string) error {
	resp, err := a.do(ctx, "DELETE", a.blobURL(key)+"?"+a.sas(key, "d", a.now().Add(azureOpTTL)), nil, nil, 0)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil
	}
	return azureError(resp, "DELETE", a.name(key))
}

// listMarked pages through List Blobs with metadata, which carries the marker.
func (a *azureStore) listMarked(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	marker := ""
	for {
		q := url.Values{"restype": {"container"}, "comp": {"list"}, "prefix": {prefix}, "include": {"metadata"}}
		if marker != "" {
			q.Set("marker", marker)
		}
		resp, err := a.do(ctx, "GET", a.base+"?"+q.Encode()+"&"+a.sas("", "l", a.now().Add(azureOpTTL)), nil, nil, 0)
		if err != nil {
			return nil, err
		}
		var page struct {
			Blobs []struct {
				Name     string `xml:"Name"`
				Metadata struct {
					Marker string `xml:"iosbuilder"`
				} `xml:"Metadata"`
			} `xml:"Blobs>Blob"`
			NextMarker string `xml:"NextMarker"`
		}
		if err := decodeXML(resp, "list", a.name(prefix), azureError, &page); err != nil {
			return nil, err
		}
		for _, b := range page.Blobs {
			if b.Metadata.Marker == markerValue {
				keys = append(keys, b.Name)
			}
		}
		if page.NextMarker == "" {
			return keys, nil
		}
		marker = page.NextMarker
	}
}

func (a *azureStore) presign(key string, ttl time.Duration, now time.Time) (string, error) {
	return a.blobURL(key) + "?" + a.sas(key, "r", now.Add(ttl)), nil
}

// azureError closes resp and turns a non-2xx answer into an error with the
// storage error code (AuthenticationFailed, ContainerNotFound, ...).
func azureError(resp *http.Response, op, what string) error {
	defer resp.Body.Close()
	if resp.StatusCode < 300 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	code := resp.Header.Get("X-Ms-Error-Code")
	if code == "" {
		return fmt.Errorf("azure %s %s: %s", op, what, resp.Status)
	}
	return fmt.Errorf("azure %s %s: %s %s", op, what, resp.Status, code)
}

// AzureFromConfig builds the azure backend from builder.json and the
// environment the Azure CLI uses: AZURE_STORAGE_ACCOUNT and AZURE_STORAGE_KEY,
// or AZURE_STORAGE_CONNECTION_STRING (AccountName, AccountKey, BlobEndpoint).
func AzureFromConfig(cfg *config.DistributeConfig, ttl time.Duration, getenv func(string) string) (*Bucket, error) {
	if cfg == nil {
		cfg = &config.DistributeConfig{}
	}
	opts := &AzureOptions{Account: cfg.Account, Container: cfg.Container, Endpoint: cfg.Endpoint, Prefix: cfg.Prefix, TTL: ttl}
	if cs := getenv("AZURE_STORAGE_CONNECTION_STRING"); cs != "" {
		for _, part := range strings.Split(cs, ";") {
			k, v, _ := strings.Cut(part, "=")
			switch k {
			case "AccountName":
				if opts.Account == "" {
					opts.Account = v
				}
			case "AccountKey":
				opts.Key = v
			case "BlobEndpoint":
				if opts.Endpoint == "" {
					opts.Endpoint = v
				}
			}
		}
	}
	if opts.Account == "" {
		opts.Account = getenv("AZURE_STORAGE_ACCOUNT")
	}
	if opts.Key == "" {
		opts.Key = getenv("AZURE_STORAGE_KEY")
	}
	return NewAzure(opts)
}
