package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// User-curated persistent playlists. Storage is playlists + playlist_tracks
// (PK (playlist_id, track_id) → adds are idempotent; ordering via a contiguous
// position column, renumbered on remove). Playback rides the one queue
// pipeline: source=playlist&playlist=N in buildPlayerQueue. The add-to-playlist
// picker (web/static/playlist_picker.js) drives the JSON endpoints; the detail
// page uses plain 303 forms.

// maxPlaylistNameLen bounds a playlist name (UI sanity, not a schema limit).
const maxPlaylistNameLen = 120

// playlistRow is one playlist in the music-home list and the picker JSON.
type playlistRow struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// loadPlaylistRows lists all playlists with track counts, name-ordered.
func (h *Handler) loadPlaylistRows(ctx context.Context) []playlistRow {
	rows, err := h.db.QueryContext(ctx, `
SELECT p.id, p.name, COUNT(pt.track_id)
FROM playlists p
LEFT JOIN playlist_tracks pt ON pt.playlist_id = p.id
GROUP BY p.id
ORDER BY lower(p.name)`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []playlistRow
	for rows.Next() {
		var p playlistRow
		if rows.Scan(&p.ID, &p.Name, &p.Count) == nil {
			out = append(out, p)
		}
	}
	return out
}

// musicPlaylists returns the playlist list as JSON — the picker's data source.
func (h *Handler) musicPlaylists(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Playlists []playlistRow `json:"playlists"`
	}{Playlists: h.loadPlaylistRows(r.Context())})
}

// musicPlaylistDetail renders one playlist: album-page-style track rows with
// remove/reorder, rename + delete, and Play/Shuffle via source=playlist.
func (h *Handler) musicPlaylistDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("id")), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	var name string
	if h.db.QueryRowContext(r.Context(), "SELECT name FROM playlists WHERE id=?", id).Scan(&name) != nil {
		http.NotFound(w, r)
		return
	}
	tracks, err := h.queryPlayerTracks(r.Context(),
		playerTrackSelect+` JOIN playlist_tracks pt ON pt.track_id=t.id WHERE pt.playlist_id=? ORDER BY pt.position`, id)
	if err != nil {
		httpError(w, 500, "internal server error", "db query failed", "handler", "musicPlaylistDetail", "err", err)
		return
	}
	// Row numbers show the playlist position, not the album track number.
	for i := range tracks {
		tracks[i].TrackNo = i + 1
	}
	h.render(w, "music_playlist.html", map[string]any{
		"Breadcrumb":   []crumb{bcHome, bcMusic},
		"Title":        name,
		"PlaylistID":   id,
		"PlaylistName": name,
		"Tracks":       tracks,
	})
}

// musicPlaylistCreate creates a playlist, optionally seeded with one track
// (the picker's "New playlist…" row) or a full id list (the now-playing "Save
// as playlist"). Always answers JSON — both callers are fetch.
func (h *Handler) musicPlaylistCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", 400)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" || len(name) > maxPlaylistNameLen {
		http.Error(w, "invalid name", 400)
		return
	}
	var trackIDs []int64
	if v := strings.TrimSpace(r.FormValue("track_id")); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			http.Error(w, "invalid track_id", 400)
			return
		}
		trackIDs = append(trackIDs, id)
	}
	for _, v := range strings.Split(r.FormValue("track_ids"), ",") {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			http.Error(w, "invalid track_ids", 400)
			return
		}
		trackIDs = append(trackIDs, id)
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		httpError(w, 500, "internal server error", "begin failed", "handler", "musicPlaylistCreate", "err", err)
		return
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(r.Context(), "INSERT INTO playlists (name) VALUES (?)", name)
	if err != nil {
		httpError(w, 500, "internal server error", "insert failed", "handler", "musicPlaylistCreate", "err", err)
		return
	}
	playlistID, _ := res.LastInsertId()
	pos := 0
	for _, tid := range trackIDs {
		// OR IGNORE: duplicate ids in a saved queue collapse to the first.
		r2, err := tx.ExecContext(r.Context(),
			"INSERT OR IGNORE INTO playlist_tracks (playlist_id, track_id, position) VALUES (?, ?, ?)",
			playlistID, tid, pos+1)
		if err != nil {
			httpError(w, 500, "internal server error", "insert track failed", "handler", "musicPlaylistCreate", "err", err)
			return
		}
		if n, _ := r2.RowsAffected(); n > 0 {
			pos++
		}
	}
	if err := tx.Commit(); err != nil {
		httpError(w, 500, "internal server error", "commit failed", "handler", "musicPlaylistCreate", "err", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"id": playlistID, "name": name, "count": pos})
}

// musicPlaylistAddTrack appends one track to a playlist (the picker's row
// click). Idempotent: adding a song already present is a friendly no-op.
func (h *Handler) musicPlaylistAddTrack(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	playlistID, trackID, ok := playlistTrackParams(r)
	if !ok {
		http.Error(w, "invalid input", 400)
		return
	}
	res, err := h.db.ExecContext(r.Context(), `
INSERT OR IGNORE INTO playlist_tracks (playlist_id, track_id, position)
SELECT p.id, t.id, COALESCE((SELECT MAX(position) FROM playlist_tracks WHERE playlist_id=p.id), 0) + 1
FROM playlists p, music_tracks t WHERE p.id=? AND t.id=?`, playlistID, trackID)
	if err != nil {
		httpError(w, 500, "internal server error", "insert failed", "handler", "musicPlaylistAddTrack", "err", err)
		return
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		h.touchPlaylist(r.Context(), playlistID)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "added": n > 0})
}

// musicPlaylistRemoveTrack removes one track (detail-page form) and renumbers
// the remainder contiguously.
func (h *Handler) musicPlaylistRemoveTrack(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	playlistID, trackID, ok := playlistTrackParams(r)
	if !ok {
		http.Error(w, "invalid input", 400)
		return
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		httpError(w, 500, "internal server error", "begin failed", "handler", "musicPlaylistRemoveTrack", "err", err)
		return
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(r.Context(),
		"DELETE FROM playlist_tracks WHERE playlist_id=? AND track_id=?", playlistID, trackID); err != nil {
		httpError(w, 500, "internal server error", "delete failed", "handler", "musicPlaylistRemoveTrack", "err", err)
		return
	}
	if err := renumberPlaylist(r.Context(), tx, playlistID); err != nil {
		httpError(w, 500, "internal server error", "renumber failed", "handler", "musicPlaylistRemoveTrack", "err", err)
		return
	}
	if err := tx.Commit(); err != nil {
		httpError(w, 500, "internal server error", "commit failed", "handler", "musicPlaylistRemoveTrack", "err", err)
		return
	}
	h.touchPlaylist(r.Context(), playlistID)
	http.Redirect(w, r, fmt.Sprintf("/music/playlist?id=%d", playlistID), http.StatusSeeOther)
}

// musicPlaylistMove moves one track up/down a step (detail-page ▲▼ forms —
// couch-friendly, no drag dependency).
func (h *Handler) musicPlaylistMove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	playlistID, trackID, ok := playlistTrackParams(r)
	dir := r.FormValue("dir")
	if !ok || (dir != "up" && dir != "down") {
		http.Error(w, "invalid input", 400)
		return
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		httpError(w, 500, "internal server error", "begin failed", "handler", "musicPlaylistMove", "err", err)
		return
	}
	defer tx.Rollback()
	// Renumber first so positions are contiguous, then swap with the neighbor.
	if err := renumberPlaylist(r.Context(), tx, playlistID); err != nil {
		httpError(w, 500, "internal server error", "renumber failed", "handler", "musicPlaylistMove", "err", err)
		return
	}
	var pos, maxPos int
	if tx.QueryRowContext(r.Context(),
		"SELECT position, (SELECT MAX(position) FROM playlist_tracks WHERE playlist_id=?) FROM playlist_tracks WHERE playlist_id=? AND track_id=?",
		playlistID, playlistID, trackID,
	).Scan(&pos, &maxPos) != nil {
		http.NotFound(w, r)
		return
	}
	other := pos - 1
	if dir == "down" {
		other = pos + 1
	}
	if other < 1 || other > maxPos {
		// Already at the edge — nothing to do.
		http.Redirect(w, r, fmt.Sprintf("/music/playlist?id=%d", playlistID), http.StatusSeeOther)
		return
	}
	// Swap with the neighbor (no UNIQUE on position, so plain updates suffice:
	// the neighbor takes pos, then our row takes the neighbor's slot).
	for _, step := range []struct {
		q    string
		args []any
	}{
		{"UPDATE playlist_tracks SET position=? WHERE playlist_id=? AND position=?", []any{pos, playlistID, other}},
		{"UPDATE playlist_tracks SET position=? WHERE playlist_id=? AND track_id=?", []any{other, playlistID, trackID}},
	} {
		if _, err := tx.ExecContext(r.Context(), step.q, step.args...); err != nil {
			httpError(w, 500, "internal server error", "swap failed", "handler", "musicPlaylistMove", "err", err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		httpError(w, 500, "internal server error", "commit failed", "handler", "musicPlaylistMove", "err", err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/music/playlist?id=%d", playlistID), http.StatusSeeOther)
}

// musicPlaylistRename renames a playlist (detail-page form).
func (h *Handler) musicPlaylistRename(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	id, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue("id")), 10, 64)
	name := strings.TrimSpace(r.FormValue("name"))
	if id <= 0 || name == "" || len(name) > maxPlaylistNameLen {
		http.Error(w, "invalid input", 400)
		return
	}
	if _, err := h.db.ExecContext(r.Context(),
		"UPDATE playlists SET name=?, updated_at=datetime('now') WHERE id=?", name, id); err != nil {
		httpError(w, 500, "internal server error", "update failed", "handler", "musicPlaylistRename", "err", err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/music/playlist?id=%d", id), http.StatusSeeOther)
}

// musicPlaylistDelete deletes a playlist (membership cascades).
func (h *Handler) musicPlaylistDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	id, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue("id")), 10, 64)
	if id <= 0 {
		http.Error(w, "invalid input", 400)
		return
	}
	if _, err := h.db.ExecContext(r.Context(), "DELETE FROM playlists WHERE id=?", id); err != nil {
		httpError(w, 500, "internal server error", "delete failed", "handler", "musicPlaylistDelete", "err", err)
		return
	}
	http.Redirect(w, r, "/music", http.StatusSeeOther)
}

// playlistTrackParams parses the (playlist_id, track_id) form pair.
func playlistTrackParams(r *http.Request) (playlistID, trackID int64, ok bool) {
	if r.ParseForm() != nil {
		return 0, 0, false
	}
	playlistID, _ = strconv.ParseInt(strings.TrimSpace(r.FormValue("playlist_id")), 10, 64)
	trackID, _ = strconv.ParseInt(strings.TrimSpace(r.FormValue("track_id")), 10, 64)
	return playlistID, trackID, playlistID > 0 && trackID > 0
}

// renumberPlaylist rewrites positions 1..N in current order. Read-then-write
// in Go — a correlated-subquery UPDATE can observe its own in-flight changes
// in SQLite, and playlists are small.
func renumberPlaylist(ctx context.Context, tx *sql.Tx, playlistID int64) error {
	rows, err := tx.QueryContext(ctx,
		"SELECT track_id FROM playlist_tracks WHERE playlist_id=? ORDER BY position", playlistID)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for i, id := range ids {
		if _, err := tx.ExecContext(ctx,
			"UPDATE playlist_tracks SET position=? WHERE playlist_id=? AND track_id=?", i+1, playlistID, id); err != nil {
			return err
		}
	}
	return nil
}

// touchPlaylist bumps updated_at (best-effort).
func (h *Handler) touchPlaylist(ctx context.Context, id int64) {
	_, _ = h.db.ExecContext(ctx, "UPDATE playlists SET updated_at=datetime('now') WHERE id=?", id)
}
