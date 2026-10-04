package otainstall

import (
	"bufio"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/MobAI-App/ios-builder/internal/config"
)

const (
	// s3MarkerHeader is the user metadata every Builder object carries;
	// cleanup deletes nothing without it.
	s3MarkerHeader = "X-Amz-Meta-Ios-Builder"
	markerValue    = "distribute"
)

// S3Options configures NewS3.
type S3Options struct {
	Bucket string
	// Region defaults to us-east-1 (Cloudflare R2 takes auto or us-east-1).
	Region string
	// Endpoint is an S3-compatible store's URL (R2, MinIO, GCS), addressed
	// path-style; empty is AWS, virtual-hosted.
	Endpoint    string
	Prefix      string
	Credentials AWSCredentials
	TTL         time.Duration
	HTTPClient  *http.Client
}

// NewS3 returns the bucket backend for S3 and S3-compatible stores.
func NewS3(opts *S3Options) (*Bucket, error) {
	if opts.Bucket == "" {
		return nil, errors.New(`the s3 backend needs a bucket: set "distribute": {"bucket": "..."} in builder.json`)
	}
	if opts.Credentials.AccessKeyID == "" || opts.Credentials.SecretAccessKey == "" {
		return nil, errors.New("the s3 backend needs credentials")
	}
	region := opts.Region
	if region == "" {
		region = "us-east-1"
	}
	base, err := s3BaseURL(opts.Bucket, region, opts.Endpoint)
	if err != nil {
		return nil, err
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{}
	}
	store := &s3Store{bucket: opts.Bucket, base: base, signer: &sigv4{creds: opts.Credentials, region: region, service: "s3"}, http: client, now: time.Now}
	return newBucket(store, opts.Prefix, opts.TTL)
}

// s3BaseURL is where keys are appended: https://bucket.s3.region.amazonaws.com/
// on AWS (path-style when the bucket name has a dot, which TLS would reject
// as a host), endpoint/bucket/ elsewhere.
func s3BaseURL(bucket, region, endpoint string) (string, error) {
	if endpoint == "" {
		if strings.Contains(bucket, ".") {
			return "https://s3." + region + ".amazonaws.com/" + uriEncode(bucket, true) + "/", nil
		}
		return "https://" + bucket + ".s3." + region + ".amazonaws.com/", nil
	}
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", fmt.Errorf("distribute.endpoint %q is not an http(s) URL", endpoint)
	}
	return strings.TrimRight(endpoint, "/") + "/" + uriEncode(bucket, true) + "/", nil
}

type s3Store struct {
	bucket string
	base   string
	signer *sigv4
	http   *http.Client
	now    func() time.Time
}

func (s *s3Store) objectURL(key string) (*url.URL, error) {
	return url.Parse(s.base + uriEncode(key, false))
}

func (s *s3Store) name(key string) string { return "s3://" + s.bucket + "/" + key }

func (s *s3Store) do(ctx context.Context, method, rawURL string, header http.Header, body io.Reader, size int64) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, err
	}
	for k, v := range header {
		req.Header[k] = v
	}
	payload := emptySHA256
	if body != nil {
		req.ContentLength = size
		payload = unsignedPayload
	}
	s.signer.sign(req, payload, s.now())
	return s.http.Do(req)
}

func (s *s3Store) put(ctx context.Context, key, contentType string, body io.Reader, size int64) error {
	u, err := s.objectURL(key)
	if err != nil {
		return err
	}
	h := http.Header{"Content-Type": {contentType}, s3MarkerHeader: {markerValue}}
	resp, err := s.do(ctx, "PUT", u.String(), h, body, size)
	if err != nil {
		return err
	}
	return s3Error(resp, "PUT", s.name(key))
}

func (s *s3Store) delete(ctx context.Context, key string) error {
	u, err := s.objectURL(key)
	if err != nil {
		return err
	}
	resp, err := s.do(ctx, "DELETE", u.String(), nil, nil, 0)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil
	}
	return s3Error(resp, "DELETE", s.name(key))
}

// listMarked pages through ListObjectsV2 and keeps the keys whose HEAD shows
// the marker; the listing does not carry user metadata.
func (s *s3Store) listMarked(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	token := ""
	for {
		q := url.Values{"list-type": {"2"}, "prefix": {prefix}}
		if token != "" {
			q.Set("continuation-token", token)
		}
		resp, err := s.do(ctx, "GET", s.base+"?"+canonicalQuery(q), nil, nil, 0)
		if err != nil {
			return nil, err
		}
		var page struct {
			Contents []struct {
				Key string `xml:"Key"`
			} `xml:"Contents"`
			IsTruncated           bool   `xml:"IsTruncated"`
			NextContinuationToken string `xml:"NextContinuationToken"`
		}
		if err := decodeXML(resp, "list", "s3://"+s.bucket+"/"+prefix, s3Error, &page); err != nil {
			return nil, err
		}
		for _, c := range page.Contents {
			marked, err := s.marked(ctx, c.Key)
			if err != nil {
				return nil, err
			}
			if marked {
				keys = append(keys, c.Key)
			}
		}
		if !page.IsTruncated || page.NextContinuationToken == "" {
			return keys, nil
		}
		token = page.NextContinuationToken
	}
}

func (s *s3Store) marked(ctx context.Context, key string) (bool, error) {
	u, err := s.objectURL(key)
	if err != nil {
		return false, err
	}
	resp, err := s.do(ctx, "HEAD", u.String(), nil, nil, 0)
	if err != nil {
		return false, err
	}
	resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return false, nil
	case resp.StatusCode >= 300:
		return false, fmt.Errorf("s3 HEAD %s: %s", s.name(key), resp.Status)
	}
	return resp.Header.Get(s3MarkerHeader) == markerValue, nil
}

func (s *s3Store) presign(key string, ttl time.Duration, now time.Time) (string, error) {
	u, err := s.objectURL(key)
	if err != nil {
		return "", err
	}
	return s.signer.presign("GET", u, ttl, now), nil
}

// s3Error closes resp and turns a non-2xx answer into an error carrying S3's
// code and message (AccessDenied, NoSuchBucket, SignatureDoesNotMatch).
func s3Error(resp *http.Response, op, what string) error {
	defer resp.Body.Close()
	if resp.StatusCode < 300 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	var e struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if xml.Unmarshal(data, &e) == nil && e.Code != "" {
		return fmt.Errorf("s3 %s %s: %s %s: %s", op, what, resp.Status, e.Code, e.Message)
	}
	return fmt.Errorf("s3 %s %s: %s", op, what, resp.Status)
}

// decodeXML decodes a 2xx XML body into v, or returns errFn's error.
func decodeXML(resp *http.Response, op, what string, errFn func(*http.Response, string, string) error, v any) error {
	if resp.StatusCode >= 300 {
		return errFn(resp, op, what)
	}
	defer resp.Body.Close()
	if err := xml.NewDecoder(resp.Body).Decode(v); err != nil {
		return fmt.Errorf("%s %s: %w", op, what, err)
	}
	return nil
}

// S3FromConfig builds the s3 backend from builder.json and the standard AWS
// environment: AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY and AWS_SESSION_TOKEN,
// else the AWS_PROFILE (or default) section of ~/.aws/credentials
// (AWS_SHARED_CREDENTIALS_FILE); the region is distribute.region, else
// AWS_REGION, else AWS_DEFAULT_REGION.
func S3FromConfig(cfg *config.DistributeConfig, ttl time.Duration, getenv func(string) string) (*Bucket, error) {
	if cfg == nil {
		cfg = &config.DistributeConfig{}
	}
	creds, err := LoadAWSCredentials(getenv)
	if err != nil {
		return nil, err
	}
	region := cfg.Region
	if region == "" {
		region = getenv("AWS_REGION")
	}
	if region == "" {
		region = getenv("AWS_DEFAULT_REGION")
	}
	return NewS3(&S3Options{Bucket: cfg.Bucket, Region: region, Endpoint: cfg.Endpoint, Prefix: cfg.Prefix, Credentials: creds, TTL: ttl})
}

// LoadAWSCredentials reads the environment, then the shared credentials file.
func LoadAWSCredentials(getenv func(string) string) (AWSCredentials, error) {
	creds := AWSCredentials{AccessKeyID: getenv("AWS_ACCESS_KEY_ID"), SecretAccessKey: getenv("AWS_SECRET_ACCESS_KEY"), SessionToken: getenv("AWS_SESSION_TOKEN")}
	if creds.AccessKeyID != "" && creds.SecretAccessKey != "" {
		return creds, nil
	}
	if creds.AccessKeyID != "" || creds.SecretAccessKey != "" {
		return AWSCredentials{}, errors.New("set both AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY, or neither to use ~/.aws/credentials")
	}
	path := getenv("AWS_SHARED_CREDENTIALS_FILE")
	if path == "" {
		home := getenv("HOME")
		if home == "" {
			home, _ = os.UserHomeDir()
		}
		path = filepath.Join(home, ".aws", "credentials")
	}
	profile := getenv("AWS_PROFILE")
	if profile == "" {
		profile = "default"
	}
	f, err := os.Open(path)
	if err != nil {
		return AWSCredentials{}, fmt.Errorf("no AWS credentials: set AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY, or AWS_PROFILE with a section in %s (%v)", path, err)
	}
	defer f.Close()
	section := ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || section != profile {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "aws_access_key_id":
			creds.AccessKeyID = v
		case "aws_secret_access_key":
			creds.SecretAccessKey = v
		case "aws_session_token":
			creds.SessionToken = v
		}
	}
	if err := sc.Err(); err != nil {
		return AWSCredentials{}, err
	}
	if creds.AccessKeyID == "" || creds.SecretAccessKey == "" {
		return AWSCredentials{}, fmt.Errorf("no AWS credentials: %s has no aws_access_key_id and aws_secret_access_key in [%s] (SSO and credential_process profiles are not read; export AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY instead)", path, profile)
	}
	return creds, nil
}
