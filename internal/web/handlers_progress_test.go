package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// The watched flag is EARNED by the progress stream and never revoked by it. The
// client sends completed:false on every tick (15s, pause, beforeunload,
// turbo:before-cache) meaning "I haven't seen it finish" — not "unwatched" — so a
// blind upsert un-watched episodes the user had genuinely finished. Clearing has
// its own owner (markWatched).

// postProgress drives the real endpoint the player's beacon hits.
func postProgress(t *testing.T, router http.Handler, path string, fileID int64, pos, dur float64, completed bool) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"file_id":          fileID,
		"position_seconds": pos,
		"duration_seconds": dur,
		"completed":        completed,
	})
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("progress %s: %d — %s", path, rec.Code, rec.Body.String())
	}
}

func TestProgressNeverRevokesTheWatchedFlag(t *testing.T) {
	h, db := newTestHandler(t)
	router := h.Router()

	res, err := db.Exec("INSERT INTO libraries (name, type, root_path) VALUES ('TV', 'tv', ?)", h.cfg.MediaRoot)
	if err != nil {
		t.Fatal(err)
	}
	libID, _ := res.LastInsertId()
	fres, err := db.Exec("INSERT INTO tv_series_files (library_id, abs_path, container) VALUES (?, ?, 'mkv')",
		libID, filepath.Join(h.cfg.MediaRoot, "p.s01e01.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	fileID, _ := fres.LastInsertId()

	completed := func() int {
		var c int
		if err := db.QueryRow("SELECT completed FROM tv_playback_progress WHERE file_id=?", fileID).Scan(&c); err != nil {
			t.Fatalf("read progress: %v", err)
		}
		return c
	}

	// Watch it to the end: the `ended` beacon marks it watched.
	postProgress(t, router, "/tv/playback-progress", fileID, 1200, 1767, false)
	postProgress(t, router, "/tv/playback-progress", fileID, 1767, 1767, true)
	if completed() != 1 {
		t.Fatal("a finished episode must be marked watched")
	}

	// The live bug: Up Next advances, the page unloads, and the unload beacon
	// asserts completed:false — which used to wipe the ✓ the user just earned.
	postProgress(t, router, "/tv/playback-progress", fileID, 1768, 1767, false)
	if completed() != 1 {
		t.Fatal("the unload beacon revoked the watched flag (the original bug)")
	}

	// Merely OPENING a watched episode ticks completed:false from its first
	// timeupdate — the nastiest of the three clearing sequences, since no playthrough
	// is even involved.
	postProgress(t, router, "/tv/playback-progress", fileID, 30, 1767, false)
	if completed() != 1 {
		t.Fatal("re-opening a watched episode revoked its watched flag")
	}

	// Clearing still works — it just has a different owner.
	if err := markWatched(context.Background(), db, "tv_playback_progress", []int64{fileID}, false); err != nil {
		t.Fatal(err)
	}
	if completed() != 0 {
		t.Fatal("mark-unwatched must still clear the flag")
	}
	var pos float64
	if err := db.QueryRow("SELECT position_seconds FROM tv_playback_progress WHERE file_id=?", fileID).Scan(&pos); err != nil {
		t.Fatal(err)
	}
	if pos != 0 {
		t.Fatalf("mark-unwatched must zero the position, got %v", pos)
	}
}

// resumePosition is the sole owner of "is there anything to resume". A position at
// the end of a finished playthrough is not a resume point: handing it back seeks
// the player to the credits, fires `ended`, and auto-advances Up Next into the next
// episode. Everything short of that resumes — including a completed item, which is
// what makes a partial re-watch keep both its ✓ and its place.
func TestResumePosition(t *testing.T) {
	cases := []struct {
		name     string
		pos, dur float64
		want     float64
	}{
		{"mid-file resume", 600, 1767, 600},
		{"a finished playthrough is not a resume point", 1767, 1767, 0},
		{"past the end (the shape every finished episode is in today)", 1768, 1767, 0},
		{"inside the end guard", 1755, 1767, 0},
		{"just outside the end guard", 1751, 1767, 1751},
		{"90% through still resumes — that's 3 real minutes of episode", 1590, 1767, 1590},
		{"unknown duration can't be judged, so honor the position", 900, 0, 900},
		{"nothing watched", 0, 1767, 0},
	}
	for _, c := range cases {
		if got := resumePosition(c.pos, c.dur); got != c.want {
			t.Fatalf("%s: resumePosition(%v, %v) = %v, want %v", c.name, c.pos, c.dur, got, c.want)
		}
	}
}

// The session must report a watched item's genuine mid-file resume point — the
// decoupling Dan asked for. Previously `completed` suppressed the resume entirely,
// so a re-watch could only ever start from the beginning.
func TestPlaybackSessionResumesAPartialRewatch(t *testing.T) {
	h, db := newTestHandler(t)
	router := h.Router()

	res, err := db.Exec("INSERT INTO libraries (name, type, root_path) VALUES ('TV', 'tv', ?)", h.cfg.MediaRoot)
	if err != nil {
		t.Fatal(err)
	}
	libID, _ := res.LastInsertId()
	path := filepath.Join(h.cfg.MediaRoot, "r.s01e01.mkv")
	if err := os.WriteFile(path, []byte("stub"), 0o644); err != nil {
		t.Fatal(err)
	}
	fres, err := db.Exec(
		"INSERT INTO tv_series_files (library_id, abs_path, container, stream_info_json) VALUES (?, ?, 'mp4', '{}')",
		libID, path)
	if err != nil {
		t.Fatal(err)
	}
	fileID, _ := fres.LastInsertId()

	// Watched, and now half-way through a re-watch.
	if _, err := db.Exec(
		"INSERT INTO tv_playback_progress (file_id, position_seconds, duration_seconds, completed, updated_at) VALUES (?, 900, 1767, 1, datetime('now'))",
		fileID); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/tv/playback-session?file=%d", fileID), nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("session: %d — %s", rec.Code, rec.Body.String())
	}
	var out struct {
		ResumePosition float64 `json:"resume_position_seconds"`
		Completed      bool    `json:"completed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Completed {
		t.Fatal("the episode is watched; the session must still say so")
	}
	if out.ResumePosition != 900 {
		t.Fatalf("a partial re-watch must resume where it was paused, got %v", out.ResumePosition)
	}
}
