package metadata

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/md5"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MobAI-App/ios-builder/internal/asc"
)

// fakeASC is a small stateful App Store Connect: resources live in one list,
// keyed by type, ID and parent, and the generic JSON:API routes
// (GET /v1/<parent>/<id>/<children>, POST /v1/<type>, PATCH and DELETE
// /v1/<type>/<id>) act on it. Screenshot bytes go to /upload/<id> and come
// back from /cdn/<id>.
type fakeASC struct {
	t      *testing.T
	mu     sync.Mutex
	srv    *httptest.Server
	items  []*fakeRes
	seq    int
	calls  []string
	blobs  map[string][]byte
	bodies map[string]map[string]any
}

type fakeRes struct {
	typ, id, parent string
	attrs           map[string]any
	rels            map[string]any
}

func newFakeASC(t *testing.T) *fakeASC {
	f := &fakeASC{t: t, blobs: map[string][]byte{}, bodies: map[string]map[string]any{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeASC) client() *asc.Client {
	f.t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		f.t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	creds := asc.Credentials{IssuerID: "iss", KeyID: "kid", PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))}
	c, err := asc.NewClient(creds, asc.WithBaseURL(f.srv.URL), asc.WithRetryDelay(time.Millisecond))
	if err != nil {
		f.t.Fatal(err)
	}
	return c
}

// add stores a resource; an empty id gets a generated one.
func (f *fakeASC) add(typ, id, parent string, attrs map[string]any) *fakeRes {
	if id == "" {
		f.seq++
		id = fmt.Sprintf("%s-%d", typ, f.seq)
	}
	if attrs == nil {
		attrs = map[string]any{}
	}
	r := &fakeRes{typ: typ, id: id, parent: parent, attrs: attrs, rels: map[string]any{}}
	f.items = append(f.items, r)
	return r
}

func (f *fakeASC) find(typ, id string) *fakeRes {
	for _, r := range f.items {
		if r.typ == typ && r.id == id {
			return r
		}
	}
	return nil
}

func (f *fakeASC) children(typ, parent string) []*fakeRes {
	var out []*fakeRes
	for _, r := range f.items {
		if r.typ == typ && r.parent == parent {
			out = append(out, r)
		}
	}
	return out
}

func (f *fakeASC) json(r *fakeRes) map[string]any {
	return map[string]any{"type": r.typ, "id": r.id, "attributes": r.attrs, "relationships": r.rels}
}

// seed is an app with an editable version 1.1 in en-US and its app info.
func (f *fakeASC) seed() {
	f.add("apps", "app-1", "", map[string]any{"bundleId": "com.example.app", "name": "My App"})
	f.add("appStoreVersions", "v-1", "app-1", map[string]any{"versionString": "1.1", "appVersionState": "PREPARE_FOR_SUBMISSION", "platform": "IOS", "createdDate": "2026-09-01T10:00:00Z"})
	info := f.add("appInfos", "ai-1", "app-1", map[string]any{"state": "PREPARE_FOR_SUBMISSION"})
	info.rels["primaryCategory"] = map[string]any{"data": map[string]any{"type": "appCategories", "id": "GAMES"}}
	f.add("appInfoLocalizations", "il-en", "ai-1", map[string]any{"locale": "en-US", "name": "My App", "subtitle": "Old subtitle", "privacyPolicyUrl": "https://example.com/privacy"})
	f.add("appStoreVersionLocalizations", "vl-en", "v-1", map[string]any{"locale": "en-US", "description": "Old description", "keywords": "a,b", "whatsNew": nil, "supportUrl": "https://example.com/support"})
}

// writes lists the non-GET API calls in order.
func (f *fakeASC) writes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		if !strings.HasPrefix(c, "GET ") && !strings.HasPrefix(c, "PUT /upload") {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeASC) body(call string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bodies[call]
}

func (f *fakeASC) write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeASC) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := r.Method + " " + r.URL.Path
	f.calls = append(f.calls, call)
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	var body map[string]any
	if r.Method == "POST" || r.Method == "PATCH" {
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.bodies[call] = body
	}
	switch {
	case r.Method == "PUT" && parts[0] == "upload":
		data, _ := io.ReadAll(r.Body)
		f.blobs[parts[1]] = data
		w.WriteHeader(200)
	case r.Method == "GET" && parts[0] == "cdn":
		_, _ = w.Write(f.blobs[strings.SplitN(parts[1], ".", 2)[0]])
	case r.Method == "GET" && len(parts) == 2 && parts[1] == "apps":
		var list []any
		for _, a := range f.children("apps", "") {
			if a.attrs["bundleId"] == r.URL.Query().Get("filter[bundleId]") {
				list = append(list, f.json(a))
			}
		}
		f.write(w, 200, map[string]any{"data": list})
	case r.Method == "GET" && len(parts) == 4:
		list := []any{}
		for _, c := range f.children(parts[3], parts[2]) {
			list = append(list, f.json(c))
		}
		f.write(w, 200, map[string]any{"data": list})
	case r.Method == "GET" && len(parts) == 3:
		res := f.find(parts[1], parts[2])
		if res == nil {
			w.WriteHeader(404)
			return
		}
		f.write(w, 200, map[string]any{"data": f.json(res)})
	case r.Method == "POST" && len(parts) == 2:
		f.create(w, parts[1], body)
	case r.Method == "PATCH" && len(parts) == 3:
		res := f.find(parts[1], parts[2])
		if res == nil {
			w.WriteHeader(404)
			return
		}
		data, _ := body["data"].(map[string]any)
		attrs, _ := data["attributes"].(map[string]any)
		for k, v := range attrs {
			res.attrs[k] = v
		}
		if rels, ok := data["relationships"].(map[string]any); ok {
			for k, v := range rels {
				res.rels[k] = v
			}
		}
		if res.typ == "appScreenshots" && attrs["uploaded"] == true {
			res.attrs["assetDeliveryState"] = map[string]any{"state": "COMPLETE"}
			res.attrs["imageAsset"] = map[string]any{"templateUrl": f.srv.URL + "/cdn/" + res.id + ".{f}", "width": 1290, "height": 2796}
			delete(res.attrs, "uploaded")
		}
		f.write(w, 200, map[string]any{"data": f.json(res)})
	case r.Method == "DELETE" && len(parts) == 3:
		for i, res := range f.items {
			if res.typ == parts[1] && res.id == parts[2] {
				f.items = append(f.items[:i], f.items[i+1:]...)
				w.WriteHeader(204)
				return
			}
		}
		w.WriteHeader(404)
	default:
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL)
		w.WriteHeader(404)
	}
}

func (f *fakeASC) create(w http.ResponseWriter, typ string, body map[string]any) {
	data, _ := body["data"].(map[string]any)
	attrs, _ := data["attributes"].(map[string]any)
	parent := ""
	rels, _ := data["relationships"].(map[string]any)
	for _, rel := range rels {
		linkage, _ := rel.(map[string]any)["data"].(map[string]any)
		parent, _ = linkage["id"].(string)
	}
	res := f.add(typ, "", parent, attrs)
	switch typ {
	case "appStoreVersions":
		// App Store Connect copies the newest version's localizations
		// (without What's New) and opens an editable app info.
		res.attrs["appVersionState"] = "PREPARE_FOR_SUBMISSION"
		res.attrs["createdDate"] = "2026-10-01T10:00:00Z"
		var newest *fakeRes
		for _, v := range f.children("appStoreVersions", parent) {
			if v != res && (newest == nil || fmt.Sprint(v.attrs["createdDate"]) > fmt.Sprint(newest.attrs["createdDate"])) {
				newest = v
			}
		}
		if newest != nil {
			for _, l := range f.children("appStoreVersionLocalizations", newest.id) {
				copied := map[string]any{}
				for k, v := range l.attrs {
					copied[k] = v
				}
				delete(copied, "whatsNew")
				f.add("appStoreVersionLocalizations", "", res.id, copied)
			}
		}
		for _, info := range f.children("appInfos", parent) {
			if info.attrs["state"] == "READY_FOR_DISTRIBUTION" {
				editable := f.add("appInfos", "", parent, map[string]any{"state": "PREPARE_FOR_SUBMISSION"})
				editable.rels = info.rels
				for _, l := range f.children("appInfoLocalizations", info.id) {
					f.add("appInfoLocalizations", "", editable.id, l.attrs)
				}
			}
		}
	case "appScreenshots":
		res.attrs["assetDeliveryState"] = map[string]any{"state": "AWAITING_UPLOAD"}
		res.attrs["uploadOperations"] = []map[string]any{{"method": "PUT", "url": f.srv.URL + "/upload/" + res.id, "offset": 0, "length": res.attrs["fileSize"]}}
	}
	f.write(w, 201, map[string]any{"data": f.json(res)})
}

// addScreenshot puts a processed screenshot with the given bytes into a set.
func (f *fakeASC) addScreenshot(setID, fileName string, data []byte) {
	sum := md5.Sum(data)
	res := f.add("appScreenshots", "", setID, map[string]any{"fileName": fileName, "fileSize": len(data), "sourceFileChecksum": hex.EncodeToString(sum[:]),
		"assetDeliveryState": map[string]any{"state": "COMPLETE"}})
	res.attrs["imageAsset"] = map[string]any{"templateUrl": f.srv.URL + "/cdn/" + res.id + ".{f}", "width": 1290, "height": 2796}
	f.blobs[res.id] = data
}

func ctx() context.Context { return context.Background() }
