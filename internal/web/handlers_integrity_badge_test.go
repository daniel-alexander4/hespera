package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// These cover the per-file integrity "corrupt" badge on the detail pages: the
// TV season page (per-episode), the TV series page (per-season roll-up), the
// movie page (aggregate over the title's file copies), and the music album
// page (per-track). Only integrity_status='flagged' badges; ''/'ok'/'repaired'
// must render nothing.

func TestTVSeasonAndSeriesIntegrityBadge(t *testing.T) {
	h, db := newTestHandler(t)
	router := h.Router()

	res, err := db.Exec("INSERT INTO libraries (name, type, root_path) VALUES ('TV', 'tv', ?)", h.cfg.MediaRoot)
	if err != nil {
		t.Fatalf("insert library: %v", err)
	}
	libID, _ := res.LastInsertId()

	seed := func(name string, ep int, status, detail string) {
		fres, err := db.Exec(
			"INSERT INTO tv_series_files (library_id, abs_path, container, integrity_status, integrity_detail) VALUES (?, ?, 'mkv', ?, ?)",
			libID, filepath.Join(h.cfg.MediaRoot, name), status, detail)
		if err != nil {
			t.Fatalf("insert file: %v", err)
		}
		fileID, _ := fres.LastInsertId()
		if _, err := db.Exec(
			`INSERT INTO tv_series_identities (file_id, provider, series_id, status, guessed_title, season_number, episode_numbers_csv)
			 VALUES (?, 'tmdb', '777', 'matched', 'Badge Show', 1, ?)`, fileID, fmt.Sprint(ep)); err != nil {
			t.Fatalf("insert identity: %v", err)
		}
	}
	seed("badge.s01e01.mkv", 1, "flagged", "bitstream corruption (4 decode errors)")
	seed("badge.s01e02.mkv", 2, "ok", "")
	seed("badge.s01e03.mkv", 3, "repaired", "container remuxed")

	t.Run("season page badges only the flagged episode", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/tv/season/?series=777&season=1", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		if n := strings.Count(body, "badge-warn"); n != 1 {
			t.Fatalf("expected exactly 1 badge-warn, got %d", n)
		}
		if !strings.Contains(body, "bitstream corruption (4 decode errors)") {
			t.Fatal("badge tooltip should carry integrity_detail")
		}
	})

	t.Run("series page rolls up the flagged count per season", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/tv/series/777", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "1 corrupt") {
			t.Fatal("season card should show the flagged roll-up")
		}
	})
}

func TestMovieDetailIntegrityBadge(t *testing.T) {
	h, db := newTestHandler(t)
	router := h.Router()

	fileID := seedMovieFile(t, db, "Badge Film", 2020, "matched", 4242, h.cfg.MediaRoot)

	get := func() string {
		req := httptest.NewRequest(http.MethodGet, "/movie/4242", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	if strings.Contains(get(), "badge-warn") {
		t.Fatal("unflagged film must not badge")
	}
	if _, err := db.Exec(
		"UPDATE movie_files SET integrity_status='flagged', integrity_detail='audio gap 2.0s (missing audio)' WHERE id=?", fileID); err != nil {
		t.Fatalf("flag file: %v", err)
	}
	body := get()
	if !strings.Contains(body, "badge-warn") || !strings.Contains(body, "audio gap 2.0s (missing audio)") {
		t.Fatal("flagged film should badge with the detail tooltip")
	}
}

func TestMusicAlbumIntegrityBadge(t *testing.T) {
	h, db := newTestHandler(t)
	router := h.Router()

	_, _, albumID, trackID := seedMusicData(t, db)

	get := func() string {
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/music/album/%d", albumID), nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	if strings.Contains(get(), "badge-warn") {
		t.Fatal("unflagged track must not badge")
	}
	if _, err := db.Exec(
		"UPDATE music_tracks SET integrity_status='flagged', integrity_detail='bitstream corruption (2 decode errors)' WHERE id=?", trackID); err != nil {
		t.Fatalf("flag track: %v", err)
	}
	body := get()
	if strings.Count(body, "badge-warn") != 1 || !strings.Contains(body, "bitstream corruption (2 decode errors)") {
		t.Fatal("flagged track should badge once with the detail tooltip")
	}
}
