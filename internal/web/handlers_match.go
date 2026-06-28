package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"hespera/internal/jobs"
	"hespera/internal/match"
)

// musicLibraryExists reports whether id refers to an existing music library.
// Enqueue only rejects negative ids, so without this guard any positive int is
// accepted and dispatched as a job that runs against an empty result set.
func (h *Handler) musicLibraryExists(ctx context.Context, id int64) (bool, error) {
	var one int
	err := h.db.QueryRowContext(ctx, "SELECT 1 FROM libraries WHERE id=? AND type='music'", id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (h *Handler) musicMatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		httpError(w, 400, "bad request", "parse form failed", "handler", "musicMatch", "err", err)
		return
	}
	idStr := strings.TrimSpace(r.FormValue("id"))
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid id", 400)
		return
	}
	if ok, err := h.musicLibraryExists(r.Context(), id); err != nil {
		httpError(w, 500, "internal server error", "library lookup failed", "handler", "musicMatch", "err", err)
		return
	} else if !ok {
		http.Error(w, "music library not found", 404)
		return
	}

	matcher := match.New(h.db, h.cfg.DataDir, h.effectiveFanartKey(r.Context()), h.effectiveAudioDBKey(r.Context()))
	jobID, err := h.jobs.Enqueue("music_match", id, "user", func(ctx context.Context, jobID, libraryID int64) error {
		return matcher.RunMusicMatch(ctx, jobID, libraryID)
	})
	if err != nil {
		if errors.Is(err, jobs.ErrQueueFull) {
			httpError(w, http.StatusServiceUnavailable, "service unavailable", "job queue full", "handler", "musicMatch", "err", err)
			return
		}
		httpError(w, 500, "internal server error", "enqueue music match failed", "handler", "musicMatch", "err", err)
		return
	}

	if requestWantsJSON(r) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"message": "match queued",
			"data":    map[string]any{"library_id": id, "job_id": jobID},
		})
		return
	}
	http.Redirect(w, r, "/settings/jobs", http.StatusSeeOther)
}

type matchReviewRow struct {
	AlbumID         int64
	Title           string
	ArtistName      string
	Year            int
	ArtPath         string
	MatchStatus     string
	MatchConfidence int
	MusicBrainzID   string
}

func (h *Handler) musicMatchReview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	// The review backlog is worked top-down and re-loaded, so it's capped (not
	// paginated): show the highest-confidence reviewListCap and the total.
	var total int
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT COUNT(*) FROM music_albums WHERE match_status = 'unmatched'").Scan(&total); err != nil {
		httpError(w, 500, "internal server error", "db count failed", "handler", "musicMatchReview", "err", err)
		return
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT a.id, a.title, COALESCE(ar.name, ''), a.year, COALESCE(a.art_path, ''),
		       a.match_status, COALESCE(a.match_confidence, 0), COALESCE(a.musicbrainz_id, '')
		FROM music_albums a
		LEFT JOIN music_artists ar ON ar.id = a.album_artist_id
		WHERE a.match_status = 'unmatched'
		ORDER BY a.match_confidence DESC, a.title ASC
		LIMIT ?
	`, reviewListCap)
	if err != nil {
		httpError(w, 500, "internal server error", "db query failed", "handler", "musicMatchReview", "err", err)
		return
	}
	defer rows.Close()

	var albums []matchReviewRow
	for rows.Next() {
		var a matchReviewRow
		if err := rows.Scan(&a.AlbumID, &a.Title, &a.ArtistName, &a.Year, &a.ArtPath,
			&a.MatchStatus, &a.MatchConfidence, &a.MusicBrainzID); err != nil {
			httpError(w, 500, "internal server error", "row scan failed", "handler", "musicMatchReview", "err", err)
			return
		}
		albums = append(albums, a)
	}
	if err := rows.Err(); err != nil {
		httpError(w, 500, "internal server error", "rows iteration failed", "handler", "musicMatchReview", "err", err)
		return
	}

	// Library ID for the writeback button — via the canonical resolver (honors a
	// ?library= override, else the newest music library) instead of re-inlining it.
	libraryID := h.resolveMusicLibraryID(r)

	h.render(w, "music_match_review.html", map[string]any{
		"Title":      "Match Review",
		"Albums":     albums,
		"LibraryID":  libraryID,
		"TotalCount": total,
		"Shown":      len(albums),
		"Capped":     total > len(albums),
	})
}

func (h *Handler) musicMatchApprove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		httpError(w, 400, "bad request", "parse form failed", "handler", "musicMatchApprove", "err", err)
		return
	}
	albumID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("album_id")), 10, 64)
	if err != nil || albumID <= 0 {
		http.Error(w, "invalid album_id", 400)
		return
	}

	res, err := h.db.ExecContext(r.Context(),
		"UPDATE music_albums SET match_status='matched' WHERE id=? AND match_status='unmatched'",
		albumID)
	if err != nil {
		httpError(w, 500, "internal server error", "db update failed", "handler", "musicMatchApprove", "err", err)
		return
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		var exists int
		if err := h.db.QueryRowContext(r.Context(), "SELECT 1 FROM music_albums WHERE id=?", albumID).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				http.NotFound(w, r)
				return
			}
		}
	}

	http.Redirect(w, r, "/music/match/review", http.StatusSeeOther)
}

func (h *Handler) musicMatchApproveAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	_, err := h.db.ExecContext(r.Context(),
		"UPDATE music_albums SET match_status='matched' WHERE match_status='unmatched'")
	if err != nil {
		httpError(w, 500, "internal server error", "db update failed", "handler", "musicMatchApproveAll", "err", err)
		return
	}

	http.Redirect(w, r, "/music/match/review", http.StatusSeeOther)
}

func (h *Handler) musicMatchReject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		httpError(w, 400, "bad request", "parse form failed", "handler", "musicMatchReject", "err", err)
		return
	}
	albumID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("album_id")), 10, 64)
	if err != nil || albumID <= 0 {
		http.Error(w, "invalid album_id", 400)
		return
	}

	_, err = h.db.ExecContext(r.Context(), `
		UPDATE music_albums SET
			match_status='skipped',
			musicbrainz_id='',
			artist_musicbrainz_id='',
			match_confidence=0
		WHERE id=?
	`, albumID)
	if err != nil {
		httpError(w, 500, "internal server error", "db update failed", "handler", "musicMatchReject", "err", err)
		return
	}

	http.Redirect(w, r, "/music/match/review", http.StatusSeeOther)
}

func (h *Handler) musicWriteback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		httpError(w, 400, "bad request", "parse form failed", "handler", "musicWriteback", "err", err)
		return
	}
	idStr := strings.TrimSpace(r.FormValue("id"))
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid id", 400)
		return
	}
	if ok, err := h.musicLibraryExists(r.Context(), id); err != nil {
		httpError(w, 500, "internal server error", "library lookup failed", "handler", "musicWriteback", "err", err)
		return
	} else if !ok {
		http.Error(w, "music library not found", 404)
		return
	}

	matcher := match.New(h.db, h.cfg.DataDir, h.effectiveFanartKey(r.Context()), h.effectiveAudioDBKey(r.Context()))
	jobID, err := h.jobs.Enqueue("tag_writeback", id, "user", func(ctx context.Context, jobID, libraryID int64) error {
		return matcher.RunTagWriteback(ctx, jobID, libraryID)
	})
	if err != nil {
		if errors.Is(err, jobs.ErrQueueFull) {
			httpError(w, http.StatusServiceUnavailable, "service unavailable", "job queue full", "handler", "musicWriteback", "err", err)
			return
		}
		httpError(w, 500, "internal server error", "enqueue writeback failed", "handler", "musicWriteback", "err", err)
		return
	}

	if requestWantsJSON(r) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"message": "writeback queued",
			"data":    map[string]any{"library_id": id, "job_id": jobID},
		})
		return
	}
	http.Redirect(w, r, "/settings/jobs", http.StatusSeeOther)
}

func (h *Handler) musicMatchRematch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		httpError(w, 400, "bad request", "parse form failed", "handler", "musicMatchRematch", "err", err)
		return
	}
	albumID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("album_id")), 10, 64)
	if err != nil || albumID <= 0 {
		http.Error(w, "invalid album_id", 400)
		return
	}

	_, err = h.db.ExecContext(r.Context(), `
		UPDATE music_albums SET
			match_status='',
			musicbrainz_id='',
			artist_musicbrainz_id='',
			match_confidence=0,
			matched_at=''
		WHERE id=?
	`, albumID)
	if err != nil {
		httpError(w, 500, "internal server error", "db update failed", "handler", "musicMatchRematch", "err", err)
		return
	}

	http.Redirect(w, r, "/music/match/review", http.StatusSeeOther)
}
