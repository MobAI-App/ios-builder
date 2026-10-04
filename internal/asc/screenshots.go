package asc

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Asset delivery states of an uploaded screenshot.
const (
	AssetStateAwaitingUpload = "AWAITING_UPLOAD"
	AssetStateUploadComplete = "UPLOAD_COMPLETE"
	AssetStateComplete       = "COMPLETE"
	AssetStateFailed         = "FAILED"
)

// AppScreenshotSet holds the screenshots of one display type (APP_IPHONE_67,
// APP_IPAD_PRO_3GEN_129, ...) in one version locale.
type AppScreenshotSet struct {
	ID          string
	DisplayType string
}

type appScreenshotSetAttributes struct {
	ScreenshotDisplayType string `json:"screenshotDisplayType,omitempty"`
}

// ListAppScreenshotSets lists the screenshot sets of a version locale.
func (c *Client) ListAppScreenshotSets(ctx context.Context, localizationID string) ([]AppScreenshotSet, error) {
	rs, err := getAll[appScreenshotSetAttributes](ctx, c, "/v1/appStoreVersionLocalizations/"+localizationID+"/appScreenshotSets", nil)
	if err != nil {
		return nil, err
	}
	sets := make([]AppScreenshotSet, 0, len(rs))
	for _, r := range rs {
		sets = append(sets, AppScreenshotSet{ID: r.ID, DisplayType: r.Attributes.ScreenshotDisplayType})
	}
	return sets, nil
}

// CreateAppScreenshotSet adds an empty set of a display type to a version locale.
func (c *Client) CreateAppScreenshotSet(ctx context.Context, localizationID, displayType string) (*AppScreenshotSet, error) {
	req := Resource[appScreenshotSetAttributes]{
		Type:          "appScreenshotSets",
		Attributes:    appScreenshotSetAttributes{ScreenshotDisplayType: displayType},
		Relationships: Relationships{"appStoreVersionLocalization": ToOne("appStoreVersionLocalizations", localizationID)},
	}
	r, err := post[appScreenshotSetAttributes, appScreenshotSetAttributes](ctx, c, "/v1/appScreenshotSets", req)
	if err != nil {
		return nil, err
	}
	return &AppScreenshotSet{ID: r.ID, DisplayType: r.Attributes.ScreenshotDisplayType}, nil
}

// AppScreenshot is one screenshot in a set.
type AppScreenshot struct {
	ID       string
	FileName string
	FileSize int64
	// Checksum is the MD5 (hex) of the uploaded file.
	Checksum string
	// TemplateURL has {w}, {h} and {f} placeholders; see ImageURL.
	TemplateURL      string
	Width, Height    int
	State            string
	Errors           []StateDetail
	UploadOperations []UploadOperation
}

// ImageURL is the download URL of the screenshot at its original size, in
// the format of its file name (png unless it was a JPEG).
func (s *AppScreenshot) ImageURL() string {
	format := "png"
	if ext := strings.ToLower(filepath.Ext(s.FileName)); ext == ".jpg" || ext == ".jpeg" {
		format = "jpg"
	}
	return strings.NewReplacer("{w}", strconv.Itoa(s.Width), "{h}", strconv.Itoa(s.Height), "{f}", format).Replace(s.TemplateURL)
}

type appScreenshotAttributes struct {
	FileName           string            `json:"fileName,omitempty"`
	FileSize           int64             `json:"fileSize,omitempty"`
	SourceFileChecksum string            `json:"sourceFileChecksum,omitempty"`
	ImageAsset         *imageAsset       `json:"imageAsset,omitempty"`
	AssetDeliveryState *uploadState      `json:"assetDeliveryState,omitempty"`
	UploadOperations   []UploadOperation `json:"uploadOperations,omitempty"`
	Uploaded           *bool             `json:"uploaded,omitempty"`
}

type imageAsset struct {
	TemplateURL string `json:"templateUrl,omitempty"`
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
}

func toAppScreenshot(r Resource[appScreenshotAttributes]) AppScreenshot {
	a := r.Attributes
	s := AppScreenshot{ID: r.ID, FileName: a.FileName, FileSize: a.FileSize, Checksum: a.SourceFileChecksum, UploadOperations: a.UploadOperations}
	if a.ImageAsset != nil {
		s.TemplateURL, s.Width, s.Height = a.ImageAsset.TemplateURL, a.ImageAsset.Width, a.ImageAsset.Height
	}
	if a.AssetDeliveryState != nil {
		s.State, s.Errors = a.AssetDeliveryState.State, a.AssetDeliveryState.Errors
	}
	return s
}

// ListAppScreenshots lists a set's screenshots in display order.
func (c *Client) ListAppScreenshots(ctx context.Context, setID string) ([]AppScreenshot, error) {
	rs, err := getAll[appScreenshotAttributes](ctx, c, "/v1/appScreenshotSets/"+setID+"/appScreenshots", nil)
	if err != nil {
		return nil, err
	}
	shots := make([]AppScreenshot, 0, len(rs))
	for _, r := range rs {
		shots = append(shots, toAppScreenshot(r))
	}
	return shots, nil
}

// GetAppScreenshot fetches one screenshot, e.g. to follow its delivery state.
func (c *Client) GetAppScreenshot(ctx context.Context, id string) (*AppScreenshot, error) {
	r, err := getOne[appScreenshotAttributes](ctx, c, "/v1/appScreenshots/"+id, nil)
	if err != nil {
		return nil, err
	}
	s := toAppScreenshot(*r)
	return &s, nil
}

// CreateAppScreenshot reserves a screenshot at the end of the set and
// returns the upload operations for its bytes.
func (c *Client) CreateAppScreenshot(ctx context.Context, setID, fileName string, size int64) (*AppScreenshot, error) {
	req := Resource[appScreenshotAttributes]{
		Type:          "appScreenshots",
		Attributes:    appScreenshotAttributes{FileName: fileName, FileSize: size},
		Relationships: Relationships{"appScreenshotSet": ToOne("appScreenshotSets", setID)},
	}
	r, err := post[appScreenshotAttributes, appScreenshotAttributes](ctx, c, "/v1/appScreenshots", req)
	if err != nil {
		return nil, err
	}
	s := toAppScreenshot(*r)
	return &s, nil
}

// CommitAppScreenshot marks the upload done; checksum is the file's MD5 in hex.
func (c *Client) CommitAppScreenshot(ctx context.Context, id, checksum string) error {
	uploaded := true
	req := Resource[appScreenshotAttributes]{Type: "appScreenshots", ID: id, Attributes: appScreenshotAttributes{Uploaded: &uploaded, SourceFileChecksum: checksum}}
	return c.Patch(ctx, "/v1/appScreenshots/"+id, Document[Resource[appScreenshotAttributes]]{Data: req}, nil)
}

// DeleteAppScreenshot removes a screenshot from its set.
func (c *Client) DeleteAppScreenshot(ctx context.Context, id string) error {
	return c.Delete(ctx, "/v1/appScreenshots/"+id, nil)
}

// FileMD5 returns the MD5 of a file in hex, the form sourceFileChecksum uses.
func FileMD5(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// UploadScreenshot appends the image at path to the set: reserve, PUT the
// bytes, commit with the MD5. Processing continues server-side; follow it
// with WaitForScreenshot.
func (c *Client) UploadScreenshot(ctx context.Context, setID, path string) (*AppScreenshot, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	sum, err := FileMD5(path)
	if err != nil {
		return nil, err
	}
	shot, err := c.CreateAppScreenshot(ctx, setID, filepath.Base(path), st.Size())
	if err != nil {
		return nil, fmt.Errorf("reserve screenshot %s: %w", filepath.Base(path), err)
	}
	if len(shot.UploadOperations) == 0 {
		return nil, fmt.Errorf("App Store Connect returned no upload operations for %s", filepath.Base(path))
	}
	if err := c.UploadChunks(ctx, f, shot.UploadOperations, nil); err != nil {
		return nil, fmt.Errorf("upload screenshot %s: %w", filepath.Base(path), err)
	}
	if err := c.CommitAppScreenshot(ctx, shot.ID, sum); err != nil {
		return nil, fmt.Errorf("commit screenshot %s: %w", filepath.Base(path), err)
	}
	shot.Checksum = sum
	return shot, nil
}

// ScreenshotFailedError reports a screenshot App Store Connect could not process.
type ScreenshotFailedError struct {
	Screenshot *AppScreenshot
}

func (e *ScreenshotFailedError) Error() string {
	msgs := make([]string, 0, len(e.Screenshot.Errors))
	for _, d := range e.Screenshot.Errors {
		msgs = append(msgs, d.String())
	}
	reason := "no details"
	if len(msgs) > 0 {
		reason = strings.Join(msgs, "; ")
	}
	return fmt.Sprintf("App Store Connect rejected screenshot %s: %s", e.Screenshot.FileName, reason)
}

// WaitForScreenshot polls until the screenshot's delivery is COMPLETE,
// returning a *ScreenshotFailedError when it FAILED.
func (c *Client) WaitForScreenshot(ctx context.Context, id string, interval time.Duration) (*AppScreenshot, error) {
	p := c.newPoller(interval)
	for {
		s, err := c.GetAppScreenshot(ctx, id)
		if err != nil {
			return nil, err
		}
		switch s.State {
		case AssetStateComplete:
			return s, nil
		case AssetStateFailed:
			return s, &ScreenshotFailedError{Screenshot: s}
		}
		if err := p.wait(ctx); err != nil {
			return s, err
		}
	}
}

// Download GETs a public asset URL (a screenshot's ImageURL) into w. The
// URL is Apple's CDN, not the API, so no token is sent.
func (c *Client) Download(ctx context.Context, assetURL string, w io.Writer) error {
	if assetURL == "" {
		return errors.New("asset has no URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, assetURL, nil)
	if err != nil {
		return err
	}
	resp, err := c.upload.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", assetURL, resp.StatusCode)
	}
	_, err = io.Copy(w, resp.Body)
	return err
}
