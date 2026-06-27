package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
)

// The success path writes real ID3 tags to a file and rescans, so it is
// manual-verify. These cover the GET render, input validation, the not-found
// guards, and the missing-file error path (which must redirect with error=1
// rather than fail silently).

func TestMusicTrackEditGET(t *testing.T) {
	h, db := newTestHandler(t)
	router := h.Router()
	_, _, _, trackID := seedMusicData(t, db)

	t.Run("renders for an existing track", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/music/track/edit?id="+strconv.FormatInt(trackID, 10), nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
	})

	t.Run("unknown track 404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/music/track/edit?id=999999", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
	})

	t.Run("non-numeric id 404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/music/track/edit?id=abc", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
	})
}

func TestMusicTrackEditPOSTValidation(t *testing.T) {
	h, db := newTestHandler(t)
	router := h.Router()
	_, _, _, trackID := seedMusicData(t, db)
	idStr := strconv.FormatInt(trackID, 10)

	cases := []struct {
		name string
		form url.Values
	}{
		{"missing title", url.Values{"album": {"A"}, "album_artist": {"AA"}}},
		{"missing album", url.Values{"title": {"T"}, "album_artist": {"AA"}}},
		{"missing album_artist", url.Values{"title": {"T"}, "album": {"A"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := postForm(t, router, "/music/track/edit?id="+idStr, c.form)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
		})
	}
}

func TestMusicTrackEditPOSTUnknownTrack(t *testing.T) {
	h, _ := newTestHandler(t)
	router := h.Router()
	rec := postForm(t, router, "/music/track/edit?id=999999", url.Values{
		"title": {"T"}, "album": {"A"}, "album_artist": {"AA"},
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// A valid edit whose backing file does not exist on disk must redirect back to
// the edit form with error=1 (the missing/moved-file path), never silently 200.
func TestMusicTrackEditPOSTMissingFile(t *testing.T) {
	h, db := newTestHandler(t)
	router := h.Router()
	_, _, _, trackID := seedMusicData(t, db) // track abs_path = /test/track1.mp3, which doesn't exist
	idStr := strconv.FormatInt(trackID, 10)

	rec := postForm(t, router, "/music/track/edit?id="+idStr, url.Values{
		"title": {"Track 1"}, "album": {"New Album"}, "album_artist": {"Test Artist"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if loc != "/music/track/edit?id="+idStr+"&error=1" {
		t.Fatalf("Location = %q, want the edit form with error=1", loc)
	}
}

func TestMusicTrackEditRejectsPut(t *testing.T) {
	h, db := newTestHandler(t)
	router := h.Router()
	_, _, _, trackID := seedMusicData(t, db)
	req := httptest.NewRequest(http.MethodPut, "/music/track/edit?id="+strconv.FormatInt(trackID, 10), nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT = %d, want 405", rec.Code)
	}
}
