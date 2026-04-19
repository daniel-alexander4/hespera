package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"isomedia/internal/jobs"
	"isomedia/internal/match"
)

func (h *Handler) musicMatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	idStr := strings.TrimSpace(r.FormValue("id"))
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid id", 400)
		return
	}

	matcher := match.New(h.db, h.cfg.DataDir)
	jobID, err := h.jobs.Enqueue("music_match", id, "user", func(ctx context.Context, jobID, libraryID int64) error {
		return matcher.RunMusicMatch(ctx, jobID, libraryID)
	})
	if err != nil {
		if errors.Is(err, jobs.ErrQueueFull) {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
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

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT a.id, a.title, COALESCE(ar.name, ''), a.year, COALESCE(a.art_path, ''),
		       a.match_status, COALESCE(a.match_confidence, 0), COALESCE(a.musicbrainz_id, '')
		FROM music_albums a
		LEFT JOIN music_artists ar ON ar.id = a.album_artist_id
		WHERE a.match_status IN ('uncertain', 'unmatched')
		ORDER BY a.match_status ASC, a.match_confidence DESC, a.title ASC
	`)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var albums []matchReviewRow
	for rows.Next() {
		var a matchReviewRow
		if err := rows.Scan(&a.AlbumID, &a.Title, &a.ArtistName, &a.Year, &a.ArtPath,
			&a.MatchStatus, &a.MatchConfidence, &a.MusicBrainzID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		albums = append(albums, a)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	h.render(w, "music_match_review.html", map[string]any{
		"Title":  "Match Review",
		"Albums": albums,
	})
}

func (h *Handler) musicMatchApprove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	albumID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("album_id")), 10, 64)
	if err != nil || albumID <= 0 {
		http.Error(w, "invalid album_id", 400)
		return
	}

	res, err := h.db.ExecContext(r.Context(),
		"UPDATE music_albums SET match_status='matched' WHERE id=? AND match_status='uncertain'",
		albumID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
		"UPDATE music_albums SET match_status='matched' WHERE match_status='uncertain'")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
		http.Error(w, err.Error(), 400)
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/music/match/review", http.StatusSeeOther)
}

func (h *Handler) musicMatchRematch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), 400)
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/music/match/review", http.StatusSeeOther)
}
