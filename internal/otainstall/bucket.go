package otainstall

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	// BucketDir is the folder, under the configured prefix, that holds every
	// upload; cleanup only looks there and only deletes objects that also
	// carry the marker metadata.
	BucketDir = "ios-builder/"
	// manifestObject is short on purpose: the manifest URL is the QR code.
	manifestObject = "m.plist"
	// DefaultBucketTTL is how long a bucket link lives without --ttl.
	DefaultBucketTTL = time.Hour
	// MinTTL keeps the refresh (a minute before expiry) from spinning.
	MinTTL = 2 * time.Minute
	// MaxTTL is the longest SigV4 presigned URL; azure keeps the same cap.
	MaxTTL = 7 * 24 * time.Hour
)

// objectStore is the little a bucket backend needs from S3 or Azure Blob.
type objectStore interface {
	// put stores body under key with Builder's marker metadata.
	put(ctx context.Context, key, contentType string, body io.Reader, size int64) error
	// delete removes key; a missing key is not an error.
	delete(ctx context.Context, key string) error
	// listMarked lists the keys under prefix that carry Builder's marker.
	listMarked(ctx context.Context, prefix string) ([]string, error)
	// presign is a GET URL for key that needs no credentials until now+ttl.
	presign(key string, ttl time.Duration, now time.Time) (string, error)
	// name is how a key is shown to the user (s3://bucket/key).
	name(key string) string
}

// Bucket keeps the IPA and the manifest as objects under one upload folder
// and links them with presigned URLs, which live up to seven days.
type Bucket struct {
	store  objectStore
	prefix string
	ttl    time.Duration
	now    func() time.Time
}

// CheckTTL refuses a link lifetime a bucket backend cannot give.
func CheckTTL(ttl time.Duration) error {
	if ttl < MinTTL || ttl > MaxTTL {
		return fmt.Errorf("--ttl must be between %s and 168h (seven days, the longest an S3 presigned URL lives), got %s", MinTTL, ttl)
	}
	return nil
}

func newBucket(store objectStore, prefix string, ttl time.Duration) (*Bucket, error) {
	if ttl == 0 {
		ttl = DefaultBucketTTL
	}
	if err := CheckTTL(ttl); err != nil {
		return nil, err
	}
	prefix = strings.TrimLeft(prefix, "/")
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	return &Bucket{store: store, prefix: prefix, ttl: ttl, now: time.Now}, nil
}

type bucketUpload struct {
	b *Bucket
	// remaining are the keys Close still has to delete, IPA first.
	remaining   []string
	ipaKey      string
	manifestKey string
}

func (b *Bucket) Upload(ctx context.Context, app *App, progress func(done, total int64)) (Upload, error) {
	f, err := os.Open(app.Path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	dir := b.prefix + BucketDir + uuid.NewString()[:8] + "/"
	up := &bucketUpload{b: b, ipaKey: dir + assetName(app.Title), manifestKey: dir + manifestObject}
	var body io.Reader = f
	if progress != nil {
		body = &progressReader{r: f, total: st.Size(), progress: progress}
	}
	if err := b.store.put(ctx, up.ipaKey, "application/octet-stream", body, st.Size()); err != nil {
		// A failed PUT stores nothing, but one cut short by Ctrl-C may have.
		dctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		_ = b.store.delete(dctx, up.ipaKey)
		return nil, err
	}
	up.remaining = []string{up.ipaKey}
	return up, nil
}

// Cleanup deletes every marked object under the upload folder.
func (b *Bucket) Cleanup(ctx context.Context) (int, error) {
	keys, err := b.store.listMarked(ctx, b.prefix+BucketDir)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, k := range keys {
		if err := b.store.delete(ctx, k); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// Mint presigns the IPA, writes the manifest naming that URL over the
// previous one (so an older link that is still valid serves the newest
// manifest) and presigns the manifest.
func (u *bucketUpload) Mint(ctx context.Context, build func(ipaURL string) ([]byte, error)) (*Links, error) {
	now := u.b.now()
	ipaURL, err := u.b.store.presign(u.ipaKey, u.b.ttl, now)
	if err != nil {
		return nil, err
	}
	body, err := build(ipaURL)
	if err != nil {
		return nil, err
	}
	if err := u.b.store.put(ctx, u.manifestKey, "application/xml", strings.NewReader(string(body)), int64(len(body))); err != nil {
		return nil, err
	}
	if len(u.remaining) == 1 && u.remaining[0] == u.ipaKey {
		u.remaining = append(u.remaining, u.manifestKey)
	}
	manifestURL, err := u.b.store.presign(u.manifestKey, u.b.ttl, now)
	if err != nil {
		return nil, err
	}
	return &Links{
		Link: Link(manifestURL), ManifestURL: manifestURL, IPAURL: ipaURL, ExpiresAt: now.Add(u.b.ttl),
		Objects: []string{u.b.store.name(u.ipaKey), u.b.store.name(u.manifestKey)},
	}, nil
}

// Close deletes the manifest and the IPA; what it fails to delete stays in
// Leftovers.
func (u *bucketUpload) Close(ctx context.Context) error {
	var errs []error
	var left []string
	for _, k := range u.remaining {
		if err := u.b.store.delete(ctx, k); err != nil {
			errs = append(errs, err)
			left = append(left, k)
		}
	}
	u.remaining = left
	return errors.Join(errs...)
}

func (u *bucketUpload) Leftovers() []string {
	out := make([]string, 0, len(u.remaining))
	for _, k := range u.remaining {
		out = append(out, u.b.store.name(k))
	}
	return out
}

// progressReader reports how much of total has been read.
type progressReader struct {
	r        io.Reader
	done     int64
	total    int64
	progress func(done, total int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.done += int64(n)
	if n > 0 || errors.Is(err, io.EOF) {
		p.progress(p.done, p.total)
	}
	return n, err
}
