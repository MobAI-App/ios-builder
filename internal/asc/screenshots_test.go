package asc

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestUploadScreenshotFlow(t *testing.T) {
	path, data := writeRandomFile(t, "01_home.png", 3_000)
	sum := md5.Sum(data)
	var (
		mu      sync.Mutex
		created map[string]any
		commit  map[string]any
		put     []byte
		polls   int
		srv     *httptest.Server
	)
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == "POST" && r.URL.Path == "/v1/appScreenshots":
			_ = json.NewDecoder(r.Body).Decode(&created)
			writeJSON(w, 201, map[string]any{"data": map[string]any{"type": "appScreenshots", "id": "shot-1", "attributes": map[string]any{
				"fileName": "01_home.png", "fileSize": len(data),
				"uploadOperations": []map[string]any{{"method": "PUT", "url": srv.URL + "/store", "offset": 0, "length": len(data)}},
			}}})
		case r.Method == "PUT" && r.URL.Path == "/store":
			if r.Header.Get("Authorization") != "" {
				t.Error("bearer token leaked to storage URL")
			}
			put, _ = io.ReadAll(r.Body)
			w.WriteHeader(200)
		case r.Method == "PATCH" && r.URL.Path == "/v1/appScreenshots/shot-1":
			_ = json.NewDecoder(r.Body).Decode(&commit)
			writeJSON(w, 200, map[string]any{"data": map[string]any{"type": "appScreenshots", "id": "shot-1"}})
		case r.Method == "GET" && r.URL.Path == "/v1/appScreenshots/shot-1":
			polls++
			state := "UPLOAD_COMPLETE"
			if polls >= 2 {
				state = "COMPLETE"
			}
			writeJSON(w, 200, map[string]any{"data": map[string]any{"type": "appScreenshots", "id": "shot-1", "attributes": map[string]any{
				"fileName": "01_home.png", "assetDeliveryState": map[string]any{"state": state},
			}}})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	slept := recordSleeps(c)
	ctx := context.Background()

	shot, err := c.UploadScreenshot(ctx, "set-1", path)
	if err != nil {
		t.Fatal(err)
	}
	if shot.ID != "shot-1" || shot.Checksum != hex.EncodeToString(sum[:]) {
		t.Errorf("shot = %+v", shot)
	}
	done, err := c.WaitForScreenshot(ctx, shot.ID, time.Second)
	if err != nil || done.State != AssetStateComplete || len(*slept) != 1 {
		t.Errorf("wait: %+v %v slept %v", done, err, *slept)
	}

	mu.Lock()
	defer mu.Unlock()
	attrs := obj(t, created, "data", "attributes")
	if attrs["fileName"] != "01_home.png" || attrs["fileSize"] != float64(len(data)) {
		t.Errorf("reserve attributes = %v", attrs)
	}
	if obj(t, created, "data", "relationships", "appScreenshotSet", "data")["id"] != "set-1" {
		t.Errorf("reserve relationships = %v", created)
	}
	if !bytes.Equal(put, data) {
		t.Error("uploaded bytes differ from the file")
	}
	commitAttrs := obj(t, commit, "data", "attributes")
	if commitAttrs["uploaded"] != true || commitAttrs["sourceFileChecksum"] != hex.EncodeToString(sum[:]) {
		t.Errorf("commit attributes = %v", commitAttrs)
	}
}

func TestWaitForScreenshotFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"data": map[string]any{"type": "appScreenshots", "id": "shot-1", "attributes": map[string]any{
			"fileName": "bad.png", "assetDeliveryState": map[string]any{"state": "FAILED", "errors": []map[string]string{{"code": "IMAGE_INCORRECT_DIMENSIONS", "description": "wrong size"}}},
		}}})
	}))
	defer srv.Close()
	_, err := newTestClient(t, srv).WaitForScreenshot(context.Background(), "shot-1", time.Millisecond)
	var failed *ScreenshotFailedError
	if !errors.As(err, &failed) || failed.Error() != "App Store Connect rejected screenshot bad.png: IMAGE_INCORRECT_DIMENSIONS: wrong size" {
		t.Errorf("err = %v", err)
	}
}

func TestScreenshotImageURL(t *testing.T) {
	s := AppScreenshot{FileName: "a.JPG", TemplateURL: "https://cdn.example/x/{w}x{h}bb.{f}", Width: 1290, Height: 2796}
	if got := s.ImageURL(); got != "https://cdn.example/x/1290x2796bb.jpg" {
		t.Errorf("ImageURL = %s", got)
	}
}

func TestLocalizationsAndCategories(t *testing.T) {
	var bodies sync.Map
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies.Store(r.Method+" "+r.URL.Path, body)
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/appStoreVersions/v-1/appStoreVersionLocalizations":
			writeJSON(w, 200, map[string]any{"data": []map[string]any{{"type": "appStoreVersionLocalizations", "id": "vl-1", "attributes": map[string]any{
				"locale": "en-US", "description": "Hello", "keywords": "a,b", "whatsNew": nil,
			}}}})
		case "POST /v1/appInfoLocalizations":
			writeJSON(w, 201, map[string]any{"data": map[string]any{"type": "appInfoLocalizations", "id": "il-2", "attributes": map[string]any{"locale": "de-DE", "name": "Hallo"}}})
		case "GET /v1/apps/app-1/appInfos":
			if r.URL.Query().Get("include") != "primaryCategory,secondaryCategory" {
				t.Errorf("appInfos query = %v", r.URL.Query())
			}
			writeJSON(w, 200, map[string]any{"data": []map[string]any{{"type": "appInfos", "id": "ai-1", "attributes": map[string]any{"state": "PREPARE_FOR_SUBMISSION"},
				"relationships": map[string]any{"primaryCategory": map[string]any{"data": map[string]any{"type": "appCategories", "id": "GAMES"}}, "secondaryCategory": map[string]any{"data": nil}}}}})
		case "PATCH /v1/appInfos/ai-1":
			writeJSON(w, 200, map[string]any{"data": map[string]any{"type": "appInfos", "id": "ai-1"}})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	ctx := context.Background()

	locs, err := c.ListVersionLocalizations(ctx, "v-1")
	if err != nil || len(locs) != 1 {
		t.Fatalf("locs = %+v, %v", locs, err)
	}
	if l := locs[0]; l.Locale != "en-US" || l.Fields[AttrDescription] != "Hello" || l.Fields[AttrKeywords] != "a,b" {
		t.Errorf("loc = %+v", l)
	}
	if _, has := locs[0].Fields[AttrWhatsNew]; has {
		t.Error("null attribute must be absent")
	}

	created, err := c.CreateAppInfoLocalization(ctx, "ai-1", "de-DE", map[string]string{AttrName: "Hallo"})
	if err != nil || created.ID != "il-2" {
		t.Fatalf("created = %+v, %v", created, err)
	}
	body, _ := bodies.Load("POST /v1/appInfoLocalizations")
	if a := obj(t, body, "data", "attributes"); a["locale"] != "de-DE" || a["name"] != "Hallo" {
		t.Errorf("create attributes = %v", a)
	}
	if obj(t, body, "data", "relationships", "appInfo", "data")["id"] != "ai-1" {
		t.Errorf("create relationships = %v", body)
	}

	infos, err := c.ListAppInfos(ctx, "app-1")
	if err != nil || len(infos) != 1 || infos[0].PrimaryCategoryID != "GAMES" || infos[0].SecondaryCategoryID != "" || !infos[0].Editable() {
		t.Fatalf("infos = %+v, %v", infos, err)
	}
	primary, clear := "PRODUCTIVITY", ""
	if err := c.UpdateAppInfoCategories(ctx, "ai-1", &primary, &clear); err != nil {
		t.Fatal(err)
	}
	body, _ = bodies.Load("PATCH /v1/appInfos/ai-1")
	rels := obj(t, body, "data", "relationships")
	if obj(t, rels, "primaryCategory", "data")["id"] != "PRODUCTIVITY" {
		t.Errorf("primary = %v", rels)
	}
	if data, has := obj(t, rels, "secondaryCategory")["data"]; !has || data != nil {
		t.Errorf("secondary must be sent as null: %v", rels)
	}
	if _, has := obj(t, body, "data")["attributes"]; has {
		t.Error("category update must not send attributes")
	}
}
