package asc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Build upload states.
const (
	UploadStateAwaitingUpload = "AWAITING_UPLOAD"
	UploadStateProcessing     = "PROCESSING"
	UploadStateFailed         = "FAILED"
	UploadStateComplete       = "COMPLETE"
)

// StateDetail is one message App Store Connect attaches to an upload state.
type StateDetail struct {
	Code        string `json:"code,omitempty"`
	Description string `json:"description,omitempty"`
}

func (d StateDetail) String() string {
	if d.Code == "" {
		return d.Description
	}
	return d.Code + ": " + d.Description
}

// BuildUpload is a build delivery in progress or finished.
type BuildUpload struct {
	ID           string
	Version      string
	BuildNumber  string
	Platform     string
	State        string
	Errors       []StateDetail
	Warnings     []StateDetail
	Infos        []StateDetail
	CreatedDate  time.Time
	UploadedDate time.Time
}

type uploadState struct {
	State    string        `json:"state,omitempty"`
	Errors   []StateDetail `json:"errors,omitempty"`
	Warnings []StateDetail `json:"warnings,omitempty"`
	Infos    []StateDetail `json:"infos,omitempty"`
}

type buildUploadAttributes struct {
	CFBundleShortVersionString string       `json:"cfBundleShortVersionString,omitempty"`
	CFBundleVersion            string       `json:"cfBundleVersion,omitempty"`
	Platform                   string       `json:"platform,omitempty"`
	State                      *uploadState `json:"state,omitempty"`
	CreatedDate                *time.Time   `json:"createdDate,omitempty"`
	UploadedDate               *time.Time   `json:"uploadedDate,omitempty"`
}

func toBuildUpload(r Resource[buildUploadAttributes]) BuildUpload {
	u := BuildUpload{
		ID:          r.ID,
		Version:     r.Attributes.CFBundleShortVersionString,
		BuildNumber: r.Attributes.CFBundleVersion,
		Platform:    r.Attributes.Platform,
	}
	if s := r.Attributes.State; s != nil {
		u.State, u.Errors, u.Warnings, u.Infos = s.State, s.Errors, s.Warnings, s.Infos
	}
	if r.Attributes.CreatedDate != nil {
		u.CreatedDate = *r.Attributes.CreatedDate
	}
	if r.Attributes.UploadedDate != nil {
		u.UploadedDate = *r.Attributes.UploadedDate
	}
	return u
}

// CreateBuildUpload opens a build delivery for the app.
func (c *Client) CreateBuildUpload(ctx context.Context, appID, version, buildNumber, platform string) (*BuildUpload, error) {
	req := Resource[buildUploadAttributes]{
		Type:          "buildUploads",
		Attributes:    buildUploadAttributes{CFBundleShortVersionString: version, CFBundleVersion: buildNumber, Platform: platform},
		Relationships: Relationships{"app": ToOne("apps", appID)},
	}
	r, err := post[buildUploadAttributes, buildUploadAttributes](ctx, c, "/v1/buildUploads", req)
	if err != nil {
		return nil, err
	}
	u := toBuildUpload(*r)
	return &u, nil
}

// GetBuildUpload fetches the current state of a delivery.
func (c *Client) GetBuildUpload(ctx context.Context, id string) (*BuildUpload, error) {
	r, err := getOne[buildUploadAttributes](ctx, c, "/v1/buildUploads/"+id, nil)
	if err != nil {
		return nil, err
	}
	u := toBuildUpload(*r)
	return &u, nil
}

// HTTPHeader is a header a presigned upload URL requires.
type HTTPHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// UploadOperation is one chunk PUT to Apple's storage.
type UploadOperation struct {
	Method         string       `json:"method,omitempty"`
	URL            string       `json:"url,omitempty"`
	Length         int64        `json:"length,omitempty"`
	Offset         int64        `json:"offset,omitempty"`
	RequestHeaders []HTTPHeader `json:"requestHeaders,omitempty"`
}

// BuildUploadFile is the reserved slot for the IPA within a delivery.
type BuildUploadFile struct {
	ID               string
	FileName         string
	FileSize         int64
	UploadOperations []UploadOperation
}

type buildUploadFileAttributes struct {
	AssetType        string            `json:"assetType,omitempty"`
	FileName         string            `json:"fileName,omitempty"`
	FileSize         int64             `json:"fileSize,omitempty"`
	UTI              string            `json:"uti,omitempty"`
	UploadOperations []UploadOperation `json:"uploadOperations,omitempty"`
}

// The reference implementation sends no checksum: ASC accepts the upload
// without one and rejects some checksum encodings, so it stays out.
type buildUploadFileCommit struct {
	Uploaded bool `json:"uploaded"`
}

func toBuildUploadFile(r Resource[buildUploadFileAttributes]) BuildUploadFile {
	return BuildUploadFile{
		ID:               r.ID,
		FileName:         r.Attributes.FileName,
		FileSize:         r.Attributes.FileSize,
		UploadOperations: r.Attributes.UploadOperations,
	}
}

// utiFor maps the archive extension to Apple's uniform type identifier.
func utiFor(fileName string) string {
	if strings.EqualFold(filepath.Ext(fileName), ".pkg") {
		return "com.apple.pkg"
	}
	return "com.apple.ipa"
}

// CreateBuildUploadFile reserves the file slot and returns the presigned chunk operations.
func (c *Client) CreateBuildUploadFile(ctx context.Context, uploadID, fileName string, size int64) (*BuildUploadFile, error) {
	req := Resource[buildUploadFileAttributes]{
		Type:          "buildUploadFiles",
		Attributes:    buildUploadFileAttributes{AssetType: "ASSET", FileName: fileName, FileSize: size, UTI: utiFor(fileName)},
		Relationships: Relationships{"buildUpload": ToOne("buildUploads", uploadID)},
	}
	r, err := post[buildUploadFileAttributes, buildUploadFileAttributes](ctx, c, "/v1/buildUploadFiles", req)
	if err != nil {
		return nil, err
	}
	f := toBuildUploadFile(*r)
	return &f, nil
}

// CommitBuildUploadFile tells App Store Connect every chunk has been sent.
func (c *Client) CommitBuildUploadFile(ctx context.Context, fileID string) error {
	req := Resource[buildUploadFileCommit]{Type: "buildUploadFiles", ID: fileID, Attributes: buildUploadFileCommit{Uploaded: true}}
	return c.Patch(ctx, "/v1/buildUploadFiles/"+fileID, Document[Resource[buildUploadFileCommit]]{Data: req}, nil)
}

// UploadChunks PUTs each operation's byte range of file to its presigned URL.
// progress, when set, is called after every chunk with the bytes sent so far.
func (c *Client) UploadChunks(ctx context.Context, file io.ReaderAt, ops []UploadOperation, progress func(sent, total int64)) error {
	var total, sent int64
	for _, op := range ops {
		total += op.Length
	}
	for i, op := range ops {
		if err := c.uploadChunk(ctx, file, op); err != nil {
			return fmt.Errorf("upload chunk %d/%d: %w", i+1, len(ops), err)
		}
		sent += op.Length
		if progress != nil {
			progress(sent, total)
		}
	}
	return nil
}

func (c *Client) uploadChunk(ctx context.Context, file io.ReaderAt, op UploadOperation) error {
	if op.URL == "" {
		return errors.New("upload operation has no URL")
	}
	method := op.Method
	if method == "" {
		method = http.MethodPut
	}
	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			if err := c.sleep(ctx, c.retryDelay<<(attempt-1)); err != nil {
				return err
			}
		}
		req, err := http.NewRequestWithContext(ctx, method, op.URL, io.NewSectionReader(file, op.Offset, op.Length))
		if err != nil {
			return err
		}
		req.ContentLength = op.Length
		for _, h := range op.RequestHeaders {
			req.Header.Set(h.Name, h.Value)
		}
		resp, err := c.upload.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		// Storage error bodies are short XML; 1 KB keeps the reason without
		// echoing a whole presigned request back into the error.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		resp.Body.Close()
		if resp.StatusCode < 300 {
			return nil
		}
		lastErr = fmt.Errorf("storage returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		if resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode != http.StatusRequestTimeout {
			return lastErr
		}
	}
	return lastErr
}

// UploadBuildOptions describes a build delivery.
type UploadBuildOptions struct {
	AppID       string
	Version     string // CFBundleShortVersionString
	BuildNumber string // CFBundleVersion
	Platform    string // defaults to PlatformIOS
	Path        string // .ipa (or .pkg) on disk
	// Progress, when set, receives the bytes sent so far and the total.
	Progress func(sent, total int64)
}

// UploadBuild runs the buildUploads flow end to end: create the delivery,
// reserve the file, PUT the chunks and commit. It returns as soon as App
// Store Connect has the file; use WaitForBuildUpload to follow processing.
func (c *Client) UploadBuild(ctx context.Context, opts *UploadBuildOptions) (*BuildUpload, error) {
	platform := opts.Platform
	if platform == "" {
		platform = PlatformIOS
	}
	f, err := os.Open(opts.Path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	upload, err := c.CreateBuildUpload(ctx, opts.AppID, opts.Version, opts.BuildNumber, platform)
	if err != nil {
		return nil, fmt.Errorf("create build upload: %w", err)
	}
	file, err := c.CreateBuildUploadFile(ctx, upload.ID, filepath.Base(opts.Path), st.Size())
	if err != nil {
		return nil, fmt.Errorf("reserve upload file: %w", err)
	}
	if len(file.UploadOperations) == 0 {
		return nil, errors.New("App Store Connect returned no upload operations for the file")
	}
	if err := c.UploadChunks(ctx, f, file.UploadOperations, opts.Progress); err != nil {
		return nil, err
	}
	if err := c.CommitBuildUploadFile(ctx, file.ID); err != nil {
		return nil, fmt.Errorf("commit upload: %w", err)
	}
	return c.GetBuildUpload(ctx, upload.ID)
}

// UploadFailedError reports a delivery App Store Connect rejected.
type UploadFailedError struct {
	Upload *BuildUpload
}

func (e *UploadFailedError) Error() string {
	msgs := make([]string, 0, len(e.Upload.Errors))
	for _, d := range e.Upload.Errors {
		msgs = append(msgs, d.String())
	}
	if len(msgs) == 0 {
		return "App Store Connect rejected the upload without details"
	}
	return "App Store Connect rejected the upload: " + strings.Join(msgs, "; ")
}

// poller spaces out status polls: the wait starts at the base interval and
// grows by half each time, capped at four times the base, so a long
// processing run costs fewer requests without making short ones sluggish.
type poller struct {
	c       *Client
	next    time.Duration
	maximum time.Duration
}

func (c *Client) newPoller(interval time.Duration) *poller {
	return &poller{c: c, next: interval, maximum: 4 * interval}
}

func (p *poller) wait(ctx context.Context) error {
	d := p.next
	if p.next = p.next * 3 / 2; p.next > p.maximum {
		p.next = p.maximum
	}
	return p.c.sleep(ctx, d)
}

// WaitForBuildUpload polls the delivery until it is COMPLETE, returning an
// *UploadFailedError when it FAILED. onPoll, when set, sees every poll result.
func (c *Client) WaitForBuildUpload(ctx context.Context, id string, interval time.Duration, onPoll func(*BuildUpload)) (*BuildUpload, error) {
	p := c.newPoller(interval)
	for {
		u, err := c.GetBuildUpload(ctx, id)
		if err != nil {
			return nil, err
		}
		if onPoll != nil {
			onPoll(u)
		}
		switch u.State {
		case UploadStateComplete:
			return u, nil
		case UploadStateFailed:
			return u, &UploadFailedError{Upload: u}
		}
		if err := p.wait(ctx); err != nil {
			return u, err
		}
	}
}

// WaitForBuild polls until the build for the version pair exists and has
// left PROCESSING. A FAILED or INVALID build is returned with an error.
func (c *Client) WaitForBuild(ctx context.Context, appID, version, buildNumber string, interval time.Duration, onPoll func(*Build)) (*Build, error) {
	p := c.newPoller(interval)
	for {
		builds, err := c.ListBuilds(ctx, &BuildFilter{AppID: appID, Platform: PlatformIOS, Version: version, BuildNumber: buildNumber, Limit: 1})
		if err != nil {
			return nil, err
		}
		if len(builds) > 0 {
			b := &builds[0]
			if onPoll != nil {
				onPoll(b)
			}
			switch b.ProcessingState {
			case ProcessingStateValid:
				return b, nil
			case ProcessingStateFailed, ProcessingStateInvalid:
				return b, fmt.Errorf("build %s (%s) finished processing as %s; App Store Connect emails the reason to the team", b.BuildNumber, b.ID, b.ProcessingState)
			}
		} else if onPoll != nil {
			onPoll(nil)
		}
		if err := p.wait(ctx); err != nil {
			return nil, err
		}
	}
}
