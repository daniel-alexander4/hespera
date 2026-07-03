package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func itoa(n int64) string      { return strconv.FormatInt(n, 10) }
func mgmtCtx() context.Context { return context.Background() }

// mgmtReq drives a request through the management router and returns the recorder.
func mgmtReq(t *testing.T, h *Handler, method, target string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	req := httptest.NewRequest(method, target, body)
	req.Header.Set("Accept", "application/json")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	rec := httptest.NewRecorder()
	h.ManagementRouter().ServeHTTP(rec, req)
	return rec
}

func decodeMgmt(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return m
}

func TestMgmtLibraryLifecycle(t *testing.T) {
	h, db := newTestHandler(t)
	root := filepath.Join(h.cfg.MediaRoot, "tunes")

	// Add.
	rec := mgmtReq(t, h, http.MethodPost, "/libraries/add", url.Values{
		"name": {"My Music"}, "type": {"music"}, "root_path": {root},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("add: want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var id int64
	_ = db.QueryRow("SELECT id FROM libraries WHERE name='My Music'").Scan(&id)
	if id == 0 {
		t.Fatal("library row not created")
	}

	// List.
	rec = mgmtReq(t, h, http.MethodGet, "/libraries", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "My Music") {
		t.Fatalf("list: want 200 with the library, got %d (%s)", rec.Code, rec.Body.String())
	}

	// Remove.
	rec = mgmtReq(t, h, http.MethodPost, "/libraries/rm", url.Values{"id": {itoa(id)}})
	if rec.Code != http.StatusOK {
		t.Fatalf("rm: want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var count int
	_ = db.QueryRow("SELECT COUNT(*) FROM libraries WHERE id=?", id).Scan(&count)
	if count != 0 {
		t.Fatal("library not deleted")
	}
}

func TestMgmtLibraryAddValidation(t *testing.T) {
	h, _ := newTestHandler(t)
	cases := []struct {
		name string
		form url.Values
	}{
		{"missing fields", url.Values{"name": {"x"}}},
		{"bad type", url.Values{"name": {"x"}, "type": {"bogus"}, "root_path": {h.cfg.MediaRoot}}},
		{"outside media root", url.Values{"name": {"x"}, "type": {"music"}, "root_path": {"/etc"}}},
		{"traversal escape", url.Values{"name": {"x"}, "type": {"music"}, "root_path": {h.cfg.MediaRoot + "/../etc"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := mgmtReq(t, h, http.MethodPost, "/libraries/add", tc.form)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d (%s)", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestMgmtConfigList(t *testing.T) {
	h, _ := newTestHandler(t)
	rec := mgmtReq(t, h, http.MethodGet, "/config", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, key := range []string{"tmdb_api_key", "lyrics_enabled", "integrity_autorepair", "media_root"} {
		if !strings.Contains(body, key) {
			t.Errorf("config list missing %q: %s", key, body)
		}
	}
}

func TestMgmtConfigToggles(t *testing.T) {
	h, _ := newTestHandler(t)

	// lyrics_enabled defaults off.
	if got := configValue(t, h, "lyrics_enabled"); got != "off" {
		t.Fatalf("lyrics default: want off, got %q", got)
	}
	// Turn it on → custom source, value on.
	if rec := mgmtReq(t, h, http.MethodPost, "/config/set", url.Values{"key": {"lyrics_enabled"}, "value": {"on"}}); rec.Code != http.StatusOK {
		t.Fatalf("set lyrics on: %d (%s)", rec.Code, rec.Body.String())
	}
	if got := configValue(t, h, "lyrics_enabled"); got != "on" {
		t.Fatalf("lyrics after on: want on, got %q", got)
	}
	if !h.effectiveLyricsEnabled(mgmtCtx()) {
		t.Fatal("effectiveLyricsEnabled should be true after CLI set on")
	}

	// integrity_autorepair defaults on; turn off.
	if got := configValue(t, h, "integrity_autorepair"); got != "on" {
		t.Fatalf("integrity default: want on, got %q", got)
	}
	if rec := mgmtReq(t, h, http.MethodPost, "/config/set", url.Values{"key": {"integrity_autorepair"}, "value": {"off"}}); rec.Code != http.StatusOK {
		t.Fatalf("set integrity off: %d", rec.Code)
	}
	if got := configValue(t, h, "integrity_autorepair"); got != "off" {
		t.Fatalf("integrity after off: want off, got %q", got)
	}
	if h.effectiveIntegrityAutoRepair(mgmtCtx()) {
		t.Fatal("effectiveIntegrityAutoRepair should be false after CLI set off")
	}

	// Bad toggle value → 400.
	if rec := mgmtReq(t, h, http.MethodPost, "/config/set", url.Values{"key": {"lyrics_enabled"}, "value": {"maybe"}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad toggle value: want 400, got %d", rec.Code)
	}
}

func TestMgmtConfigSecretMasked(t *testing.T) {
	h, _ := newTestHandler(t)
	if rec := mgmtReq(t, h, http.MethodPost, "/config/set", url.Values{"key": {"tmdb_api_key"}, "value": {"abcdef123456"}}); rec.Code != http.StatusOK {
		t.Fatalf("set tmdb: %d (%s)", rec.Code, rec.Body.String())
	}
	got := configValue(t, h, "tmdb_api_key")
	if strings.Contains(got, "abcdef") {
		t.Fatalf("secret leaked in output: %q", got)
	}
	if !strings.HasSuffix(got, "3456") {
		t.Fatalf("want masked value ending 3456, got %q", got)
	}
	if h.effectiveTMDBKey(mgmtCtx()) != "abcdef123456" {
		t.Fatal("effectiveTMDBKey should return the raw stored key")
	}
}

func TestMgmtConfigMediaRootValidation(t *testing.T) {
	h, _ := newTestHandler(t)

	// A valid existing dir → 200, applies on restart.
	rec := mgmtReq(t, h, http.MethodPost, "/config/set", url.Values{"key": {"media_root"}, "value": {h.cfg.MediaRoot}})
	if rec.Code != http.StatusOK {
		t.Fatalf("valid media_root: %d (%s)", rec.Code, rec.Body.String())
	}
	if msg, _ := decodeMgmt(t, rec)["message"].(string); !strings.Contains(msg, "restart") {
		t.Fatalf("want apply-on-restart message, got %q", msg)
	}

	// A non-existent dir → 400.
	rec = mgmtReq(t, h, http.MethodPost, "/config/set", url.Values{"key": {"media_root"}, "value": {filepath.Join(h.cfg.MediaRoot, "does-not-exist")}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid media_root: want 400, got %d", rec.Code)
	}
}

func TestMgmtConfigUnknownKey(t *testing.T) {
	h, _ := newTestHandler(t)
	if rec := mgmtReq(t, h, http.MethodPost, "/config/set", url.Values{"key": {"nope"}, "value": {"x"}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown key set: want 400, got %d", rec.Code)
	}
	if rec := mgmtReq(t, h, http.MethodGet, "/config/get?key=nope", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown key get: want 400, got %d", rec.Code)
	}
}

func TestMgmtMatchValidation(t *testing.T) {
	h, db := newTestHandler(t)
	// A photos library can't be matched.
	res, _ := db.Exec("INSERT INTO libraries (name, type, root_path) VALUES ('Pics','photos',?)", h.cfg.MediaRoot)
	photoID, _ := res.LastInsertId()
	// A TV library with no TMDB key can't match.
	res, _ = db.Exec("INSERT INTO libraries (name, type, root_path) VALUES ('Shows','tv',?)", h.cfg.MediaRoot)
	tvID, _ := res.LastInsertId()

	cases := []struct {
		name string
		form url.Values
		want int
	}{
		{"invalid id", url.Values{"id": {"0"}}, http.StatusBadRequest},
		{"missing library", url.Values{"id": {"99999"}}, http.StatusNotFound},
		{"photos unsupported", url.Values{"id": {itoa(photoID)}}, http.StatusBadRequest},
		{"tv without key", url.Values{"id": {itoa(tvID)}}, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := mgmtReq(t, h, http.MethodPost, "/match", tc.form)
			if rec.Code != tc.want {
				t.Fatalf("want %d, got %d (%s)", tc.want, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestMgmtStatus(t *testing.T) {
	h, _ := newTestHandler(t)
	rec := mgmtReq(t, h, http.MethodGet, "/status", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	data, _ := decodeMgmt(t, rec)["data"].(map[string]any)
	if data == nil || data["media_root"] != h.cfg.MediaRoot {
		t.Fatalf("status media_root mismatch: %v", data)
	}
}

// configValue fetches a single setting's rendered value via /config/get.
func configValue(t *testing.T, h *Handler, key string) string {
	t.Helper()
	rec := mgmtReq(t, h, http.MethodGet, "/config/get?key="+key, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("config get %s: %d (%s)", key, rec.Code, rec.Body.String())
	}
	data, _ := decodeMgmt(t, rec)["data"].(map[string]any)
	v, _ := data["value"].(string)
	return v
}
