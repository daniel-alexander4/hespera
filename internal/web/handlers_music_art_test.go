package web

import (
	"bytes"
	"encoding/base64"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
)

// a real 1x1 PNG (DetectContentType -> image/png, valid PNG magic).
var onePxPNG, _ = base64.StdEncoding.DecodeString(
	"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")

// a minimal GIF: valid image (image/gif) but a disallowed format.
var tinyGIF = []byte("GIF89a\x01\x00\x01\x00\x00\xff\x00,\x00\x00\x00\x00\x01\x00\x01\x00\x00\x02\x00;")

func artUploadBody(t *testing.T, albumID string, filename string, content []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("album_id", albumID)
	if content != nil {
		fw, err := mw.CreateFormFile("art", filename)
		if err != nil {
			t.Fatalf("CreateFormFile: %v", err)
		}
		_, _ = fw.Write(content)
	}
	_ = mw.Close()
	return &buf, mw.FormDataContentType()
}

func albumArtPath(t *testing.T, h *Handler, albumID int64) string {
	t.Helper()
	var ap string
	if err := h.db.QueryRow("SELECT art_path FROM music_albums WHERE id=?", albumID).Scan(&ap); err != nil {
		t.Fatalf("query art_path: %v", err)
	}
	return ap
}

func TestMusicAlbumArtUpload(t *testing.T) {
	h, db := newTestHandler(t)
	router := h.Router()
	_, _, albumID, _ := seedMusicData(t, db)
	idStr := strconv.FormatInt(albumID, 10)

	// 1. Valid PNG -> 303, art_path set, file on disk.
	body, ct := artUploadBody(t, idStr, "cover.png", onePxPNG)
	req := httptest.NewRequest(http.MethodPost, "/music/album/art", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("valid upload: status = %d, want 303 (body: %s)", rec.Code, rec.Body.String())
	}
	ap := albumArtPath(t, h, albumID)
	if ap == "" {
		t.Fatal("art_path not set after valid upload")
	}
	if !strings.HasSuffix(ap, ".png") {
		t.Fatalf("art_path = %q, want .png suffix", ap)
	}
	if _, err := os.Stat(ap); err != nil {
		t.Fatalf("art file not on disk: %v", err)
	}

	// 2. It serves via /art/album/{id} with nosniff.
	gr := httptest.NewRequest(http.MethodGet, "/art/album/"+idStr, nil)
	grec := httptest.NewRecorder()
	router.ServeHTTP(grec, gr)
	if grec.Code != http.StatusOK {
		t.Fatalf("serve art: status = %d, want 200", grec.Code)
	}
	if got := grec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if ct := grec.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("served Content-Type = %q, want image/png", ct)
	}

	// 3. Re-upload self-overwrites: same stable filename, art_path unchanged.
	body2, ct2 := artUploadBody(t, idStr, "cover2.png", onePxPNG)
	req2 := httptest.NewRequest(http.MethodPost, "/music/album/art", body2)
	req2.Header.Set("Content-Type", ct2)
	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusSeeOther {
		t.Fatalf("re-upload: status = %d, want 303", rec2.Code)
	}
	if ap2 := albumArtPath(t, h, albumID); ap2 != ap {
		t.Fatalf("re-upload changed art_path: %q -> %q (should be stable)", ap, ap2)
	}
}

func TestMusicAlbumArtUploadRejects(t *testing.T) {
	h, db := newTestHandler(t)
	router := h.Router()
	_, _, albumID, _ := seedMusicData(t, db)
	idStr := strconv.FormatInt(albumID, 10)

	cases := []struct {
		name     string
		content  []byte
		filename string
	}{
		{"non-image text", []byte("this is definitely not an image, just plain text content"), "evil.png"},
		{"disallowed gif format", tinyGIF, "anim.gif"},
		{"empty file", []byte{}, "empty.png"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body, ct := artUploadBody(t, idStr, c.filename, c.content)
			req := httptest.NewRequest(http.MethodPost, "/music/album/art", body)
			req.Header.Set("Content-Type", ct)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s: status = %d, want 400", c.name, rec.Code)
			}
			if ap := albumArtPath(t, h, albumID); ap != "" {
				t.Fatalf("%s: art_path = %q, want unchanged (empty)", c.name, ap)
			}
		})
	}
}
