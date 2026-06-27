package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
)

// The disambiguation happy path (GET candidate render, POST + re-enrich) makes
// live MusicBrainz/Wikipedia calls, so it's covered by manual verification and
// the match-package parsing test. These tests exercise the network-free guard
// paths: input validation, the MBID format check, and method dispatch.

func TestMusicArtistDisambiguateRejectsBadMBID(t *testing.T) {
	h, db := newTestHandler(t)
	router := h.Router()
	_, artistID, _, _ := seedMusicData(t, db)

	// Give the artist a bio so we can prove no mutation happens on a bad request.
	if _, err := db.Exec("UPDATE music_artists SET bio='keep me', musicbrainz_id='old-id' WHERE id=?", artistID); err != nil {
		t.Fatalf("seed: %v", err)
	}

	for _, bad := range []string{"", "not-a-uuid", "1234", "394492c0-cecf-40a8-b676"} {
		rec := postForm(t, router, "/music/artist/disambiguate", url.Values{
			"artist_id": {strconv.FormatInt(artistID, 10)},
			"mbid":      {bad},
		})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("mbid=%q: status = %d, want 400", bad, rec.Code)
		}
	}

	// Row must be untouched — validation precedes any DB write.
	var bio, mbid string
	if err := db.QueryRow("SELECT bio, musicbrainz_id FROM music_artists WHERE id=?", artistID).Scan(&bio, &mbid); err != nil {
		t.Fatalf("query: %v", err)
	}
	if bio != "keep me" || mbid != "old-id" {
		t.Fatalf("row mutated on bad request: bio=%q mbid=%q", bio, mbid)
	}
}

func TestMusicArtistDisambiguateRejectsBadArtistID(t *testing.T) {
	h, _ := newTestHandler(t)
	router := h.Router()

	for _, bad := range []string{"", "0", "-1", "abc"} {
		rec := postForm(t, router, "/music/artist/disambiguate", url.Values{
			"artist_id": {bad},
			"mbid":      {"394492c0-cecf-40a8-b676-0e5706317fab"},
		})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("artist_id=%q: status = %d, want 400", bad, rec.Code)
		}
	}
}

func TestMusicArtistDisambiguateUnknownArtist(t *testing.T) {
	h, _ := newTestHandler(t)
	router := h.Router()

	// Valid-format MBID but a nonexistent artist → 404 before any mutation or
	// network call (the exists check precedes both).
	rec := postForm(t, router, "/music/artist/disambiguate", url.Values{
		"artist_id": {"999999"},
		"mbid":      {"394492c0-cecf-40a8-b676-0e5706317fab"},
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestMusicArtistDisambiguateGETBadID(t *testing.T) {
	h, _ := newTestHandler(t)
	router := h.Router()

	for _, path := range []string{
		"/music/artist/disambiguate",
		"/music/artist/disambiguate?id=0",
		"/music/artist/disambiguate?id=abc",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s = %d, want 404", path, rec.Code)
		}
	}
}

func TestMusicArtistDisambiguateRejectsPUT(t *testing.T) {
	h, _ := newTestHandler(t)
	router := h.Router()

	req := httptest.NewRequest(http.MethodPut, "/music/artist/disambiguate", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT = %d, want 405", rec.Code)
	}
}
