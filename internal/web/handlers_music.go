package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"isomedia/internal/match"
	"isomedia/internal/music"
	"isomedia/internal/pathguard"
)

// --- Row types ---

type artistRow struct {
	ID             int64
	Name           string
	ArtPath        string
	Count          int
	PlayCount      int
	LastPlayedRaw  string
	LastPlayedText string
}

type musicHomeArtistRow struct {
	ID      int64
	Name    string
	ArtPath string
}

type musicHomeAlbumRow struct {
	ID         int64
	Title      string
	Year       int
	ArtPath    string
	ArtistName string
	DiscText   string
}

type compilationAlbumRow struct {
	ID       int64
	Title    string
	Year     int
	ArtPath  string
	DiscText string
}

type albumDetailRow struct {
	ID            int64
	Title         string
	Year          int
	ArtPath       string
	ArtistID      int64
	ArtistName    string
	IsCompilation bool
}

type trackRow struct {
	ID            int64
	AlbumID       int64
	AlbumTitle    string
	AlbumYear     int
	Title         string
	Artist        string
	ArtistID      int64
	ArtistDisplay string
	TrackNo       int
	DiscNo        int
	MIME          string
	IsCompilation bool
}

type discSection struct {
	DiscNo int
	Tracks []trackRow
}

type artistAlbumRow struct {
	ID        int64
	Title     string
	Year      int
	ArtPath   string
	IsComp    bool
	Count     int
	PlayCount int
}

type playEventInput struct {
	TrackID   int64  `json:"track_id"`
	PlayedMS  int64  `json:"played_ms"`
	Completed bool   `json:"completed"`
	Source    string `json:"source"`
}

// --- Helpers ---

func scanNullString(ns sql.NullString) string {
	if ns.Valid {
		return strings.TrimSpace(ns.String)
	}
	return ""
}

func (h *Handler) resolveMusicLibraryID(r *http.Request) int64 {
	if v := strings.TrimSpace(r.URL.Query().Get("library")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	var id int64
	_ = h.db.QueryRowContext(r.Context(),
		"SELECT id FROM libraries WHERE type='music' ORDER BY id DESC LIMIT 1",
	).Scan(&id)
	return id
}

func formatPlayTimestamp(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if len(raw) >= 10 {
		return raw[:10]
	}
	return raw
}

// --- Music Home ---

func (h *Handler) musicHome(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	libraryID := h.resolveMusicLibraryID(r)
	if libraryID == 0 {
		h.render(w, "music_home.html", map[string]any{
			"Title": "Music",
		})
		return
	}

	loadArtists := func(query string, args ...any) ([]musicHomeArtistRow, error) {
		rows, err := h.db.QueryContext(r.Context(), query, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := make([]musicHomeArtistRow, 0, 20)
		for rows.Next() {
			var row musicHomeArtistRow
			var art sql.NullString
			if err := rows.Scan(&row.ID, &row.Name, &art); err != nil {
				return nil, err
			}
			row.ArtPath = scanNullString(art)
			out = append(out, row)
		}
		return out, rows.Err()
	}

	recentlyPlayed, err := loadArtists(`
SELECT a.id, a.name, a.art_path
FROM music_artists a
JOIN (
  SELECT artist_id, MAX(created_at) AS last_played
  FROM play_history
  WHERE library_id=?
  GROUP BY artist_id
) x ON x.artist_id = a.id
WHERE a.library_id=?
ORDER BY x.last_played DESC, lower(a.name)
LIMIT 18
`, libraryID, libraryID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	recentlyAddedAlbums, err := h.loadRecentlyAddedAlbums(r.Context(), libraryID, 18)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// All artists
	artistRows, err := h.db.QueryContext(r.Context(), `
SELECT a.id, a.name, a.art_path,
       (SELECT COUNT(*) FROM music_tracks t
         JOIN music_albums al ON al.id=t.album_id
         WHERE t.artist_id=a.id AND COALESCE(al.is_compilation,0)=0) AS track_count,
       (SELECT COUNT(*) FROM play_history ph WHERE ph.artist_id=a.id) AS play_count,
       COALESCE((SELECT MAX(ph.created_at) FROM play_history ph WHERE ph.artist_id=a.id), '') AS last_played
FROM music_artists a
WHERE a.library_id=?
  AND EXISTS (
    SELECT 1 FROM music_albums al
    WHERE al.album_artist_id=a.id AND COALESCE(al.is_compilation,0)=0
  )
ORDER BY lower(a.name)
`, libraryID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer artistRows.Close()

	artists := make([]artistRow, 0, 64)
	for artistRows.Next() {
		var a artistRow
		var art sql.NullString
		if err := artistRows.Scan(&a.ID, &a.Name, &art, &a.Count, &a.PlayCount, &a.LastPlayedRaw); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		a.ArtPath = scanNullString(art)
		a.LastPlayedText = formatPlayTimestamp(a.LastPlayedRaw)
		artists = append(artists, a)
	}
	if err := artistRows.Err(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// All compilations
	compRows, err := h.db.QueryContext(r.Context(), `
SELECT id, title, year, art_path
FROM music_albums
WHERE library_id=? AND COALESCE(is_compilation,0)=1
ORDER BY year, lower(title)
`, libraryID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer compRows.Close()

	compilations := make([]compilationAlbumRow, 0, 32)
	for compRows.Next() {
		var row compilationAlbumRow
		var art sql.NullString
		if err := compRows.Scan(&row.ID, &row.Title, &row.Year, &art); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		row.ArtPath = scanNullString(art)
		compilations = append(compilations, row)
	}
	if err := compRows.Err(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	h.render(w, "music_home.html", map[string]any{
		"Title":               "Music",
		"LibraryID":           libraryID,
		"RecentlyPlayed":      recentlyPlayed,
		"RecentlyAddedAlbums": recentlyAddedAlbums,
		"Artists":             artists,
		"Compilations":        compilations,
	})
}

func (h *Handler) loadRecentlyAddedAlbums(ctx context.Context, libraryID int64, limit int) ([]musicHomeAlbumRow, error) {
	if libraryID <= 0 {
		return []musicHomeAlbumRow{}, nil
	}
	rows, err := h.db.QueryContext(ctx, `
SELECT al.id, al.title, al.year, al.art_path, ar.name
FROM music_albums al
JOIN music_artists ar ON ar.id = CASE
  WHEN al.album_artist_id > 0 THEN al.album_artist_id
  ELSE al.artist_id
END
WHERE al.library_id=?
  AND COALESCE(al.is_compilation,0)=0
ORDER BY al.created_at DESC, al.id DESC
LIMIT ?
`, libraryID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]musicHomeAlbumRow, 0, limit)
	for rows.Next() {
		var row musicHomeAlbumRow
		var art sql.NullString
		if err := rows.Scan(&row.ID, &row.Title, &row.Year, &art, &row.ArtistName); err != nil {
			return nil, err
		}
		row.ArtPath = scanNullString(art)
		out = append(out, row)
	}
	return out, rows.Err()
}

// --- Artist Detail ---

func (h *Handler) musicArtistAlbums(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	idStr := strings.TrimPrefix(r.URL.Path, "/music/artist/")
	idStr = path.Clean("/" + idStr)
	idStr = strings.TrimPrefix(idStr, "/")
	artistID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || artistID <= 0 {
		http.NotFound(w, r)
		return
	}

	var libraryID int64
	var artistName string
	var artistArt sql.NullString
	var artistBio sql.NullString
	var bioSourceURL sql.NullString
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT library_id, name, art_path, bio, bio_source_url FROM music_artists WHERE id=?",
		artistID,
	).Scan(&libraryID, &artistName, &artistArt, &artistBio, &bioSourceURL); err != nil {
		http.NotFound(w, r)
		return
	}

	// Albums by this artist (non-compilation)
	rows, err := h.db.QueryContext(r.Context(), `
SELECT al.id, al.title, al.year, al.art_path, COALESCE(al.is_compilation,0),
       (SELECT COUNT(*) FROM music_tracks t WHERE t.album_id=al.id) AS track_count,
       (SELECT COUNT(*) FROM play_history ph WHERE ph.album_id=al.id) AS play_count
FROM music_albums al
WHERE al.album_artist_id=? AND COALESCE(al.is_compilation,0)=0
ORDER BY CASE WHEN al.year > 0 THEN 0 ELSE 1 END, al.year DESC, lower(al.title)
`, artistID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer rows.Close()

	albums := make([]artistAlbumRow, 0, 16)
	for rows.Next() {
		var a artistAlbumRow
		var art sql.NullString
		var comp int
		if err := rows.Scan(&a.ID, &a.Title, &a.Year, &art, &comp, &a.Count, &a.PlayCount); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		a.ArtPath = scanNullString(art)
		a.IsComp = comp != 0
		albums = append(albums, a)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	// Compilations that include this artist's tracks
	compRows, err := h.db.QueryContext(r.Context(), `
SELECT DISTINCT al.id, al.title, al.year, al.art_path
FROM music_albums al
JOIN music_tracks t ON t.album_id=al.id
WHERE t.artist_id=? AND COALESCE(al.is_compilation,0)=1
ORDER BY al.year, lower(al.title)
`, artistID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer compRows.Close()

	comps := make([]compilationAlbumRow, 0, 8)
	for compRows.Next() {
		var c compilationAlbumRow
		var art sql.NullString
		if err := compRows.Scan(&c.ID, &c.Title, &c.Year, &art); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		c.ArtPath = scanNullString(art)
		comps = append(comps, c)
	}
	if err := compRows.Err(); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	// All tracks by this artist for queue building
	trackRows, err := h.db.QueryContext(r.Context(), `
SELECT t.id, t.album_id, al.title, al.year, t.title, ar.name, t.track_no, t.disc_no, COALESCE(NULLIF(t.mime_type,''), 'application/octet-stream')
FROM music_tracks t
JOIN music_albums al ON al.id=t.album_id
JOIN music_artists ar ON ar.id=t.artist_id
WHERE al.album_artist_id=? AND COALESCE(al.is_compilation,0)=0
ORDER BY al.year, lower(al.title), t.disc_no, t.track_no, lower(t.title)
`, artistID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer trackRows.Close()

	tracks := make([]trackRow, 0, 64)
	for trackRows.Next() {
		var t trackRow
		if err := trackRows.Scan(&t.ID, &t.AlbumID, &t.AlbumTitle, &t.AlbumYear, &t.Title, &t.Artist, &t.TrackNo, &t.DiscNo, &t.MIME); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		tracks = append(tracks, t)
	}
	if err := trackRows.Err(); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	h.render(w, "music_artist.html", map[string]any{
		"Title":        artistName,
		"ArtistID":     artistID,
		"ArtistName":   artistName,
		"ArtistArt":    scanNullString(artistArt),
		"ArtistBio":    scanNullString(artistBio),
		"BioSourceURL": scanNullString(bioSourceURL),
		"Albums":       albums,
		"Compilations": comps,
		"Tracks":       tracks,
		"LibraryID":    libraryID,
	})
}

// --- Album Tracks ---

func (h *Handler) musicAlbumTracks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	idStr := strings.TrimPrefix(r.URL.Path, "/music/album/")
	idStr = path.Clean("/" + idStr)
	idStr = strings.TrimPrefix(idStr, "/")
	albumID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || albumID <= 0 {
		http.NotFound(w, r)
		return
	}

	var albumTitle string
	var albumYear int
	var albumArt sql.NullString
	var artistID int64
	var artistName string
	var compInt int
	if err := h.db.QueryRowContext(r.Context(), `
SELECT al.title, al.year, al.art_path, ar.id, ar.name, COALESCE(al.is_compilation,0)
FROM music_albums al
JOIN music_artists ar ON ar.id = al.album_artist_id
WHERE al.id=?
`, albumID).Scan(&albumTitle, &albumYear, &albumArt, &artistID, &artistName, &compInt); err != nil {
		http.NotFound(w, r)
		return
	}
	isCompilation := compInt != 0

	rows, err := h.db.QueryContext(r.Context(), `
SELECT t.id, t.title, ar.name, ar.id, t.track_no, t.disc_no, COALESCE(NULLIF(t.mime_type,''), 'application/octet-stream')
FROM music_tracks t
JOIN music_artists ar ON ar.id=t.artist_id
WHERE t.album_id=?
ORDER BY disc_no, track_no, lower(title)
`, albumID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer rows.Close()

	var tracks []trackRow
	discBuckets := map[int][]trackRow{}
	discOrder := make([]int, 0, 2)
	for rows.Next() {
		var t trackRow
		if err := rows.Scan(&t.ID, &t.Title, &t.Artist, &t.ArtistID, &t.TrackNo, &t.DiscNo, &t.MIME); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		t.AlbumID = albumID
		t.AlbumTitle = albumTitle
		t.AlbumYear = albumYear
		t.IsCompilation = isCompilation
		t.ArtistDisplay = t.Artist
		tracks = append(tracks, t)

		discNo := t.DiscNo
		if discNo <= 0 {
			discNo = 1
		}
		if _, ok := discBuckets[discNo]; !ok {
			discOrder = append(discOrder, discNo)
		}
		discBuckets[discNo] = append(discBuckets[discNo], t)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	sections := make([]discSection, 0, len(discOrder))
	for _, discNo := range discOrder {
		sections = append(sections, discSection{
			DiscNo: discNo,
			Tracks: discBuckets[discNo],
		})
	}

	h.render(w, "music_album.html", map[string]any{
		"Title":         albumTitle,
		"ArtistID":      artistID,
		"ArtistName":    artistName,
		"AlbumID":       albumID,
		"AlbumTitle":    albumTitle,
		"AlbumYear":     albumYear,
		"AlbumArt":      scanNullString(albumArt),
		"Tracks":        tracks,
		"DiscTracks":    sections,
		"MultiDisc":     len(sections) > 1,
		"IsCompilation": isCompilation,
	})
}

// --- Album Edit ---

func (h *Handler) musicAlbumEdit(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimSpace(r.URL.Query().Get("id"))
	albumID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || albumID <= 0 {
		http.NotFound(w, r)
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.musicAlbumEditGET(w, r, albumID)
	case http.MethodPost:
		h.musicAlbumEditPOST(w, r, albumID)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (h *Handler) musicAlbumEditGET(w http.ResponseWriter, r *http.Request, albumID int64) {
	writeback := r.URL.Query().Get("writeback") == "1"

	var albumTitle string
	var albumYear int
	var artistName string
	var compInt int
	if err := h.db.QueryRowContext(r.Context(), `
		SELECT al.title, al.year, ar.name, COALESCE(al.is_compilation, 0)
		FROM music_albums al
		JOIN music_artists ar ON ar.id = al.album_artist_id
		WHERE al.id=?
	`, albumID).Scan(&albumTitle, &albumYear, &artistName, &compInt); err != nil {
		http.NotFound(w, r)
		return
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT t.id, t.title, t.track_no, t.disc_no, ar.name
		FROM music_tracks t
		JOIN music_artists ar ON ar.id = t.artist_id
		WHERE t.album_id=?
		ORDER BY t.disc_no, t.track_no, lower(t.title)
	`, albumID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer rows.Close()

	type editTrack struct {
		ID      int64
		Title   string
		TrackNo int
		DiscNo  int
		Artist  string
	}
	var tracks []editTrack
	for rows.Next() {
		var t editTrack
		if err := rows.Scan(&t.ID, &t.Title, &t.TrackNo, &t.DiscNo, &t.Artist); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		tracks = append(tracks, t)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	var wbSuccess, wbErrors int
	if s := r.URL.Query().Get("success"); s != "" {
		wbSuccess, _ = strconv.Atoi(s)
	}
	if s := r.URL.Query().Get("errors"); s != "" {
		wbErrors, _ = strconv.Atoi(s)
	}

	h.render(w, "music_album_edit.html", map[string]any{
		"Title":         "Edit Album",
		"AlbumID":       albumID,
		"AlbumTitle":    albumTitle,
		"AlbumYear":     albumYear,
		"ArtistName":    artistName,
		"Tracks":        tracks,
		"Writeback":     writeback,
		"IsCompilation": compInt != 0,
		"WBSuccess":     wbSuccess,
		"WBErrors":      wbErrors,
	})
}

func (h *Handler) musicAlbumEditPOST(w http.ResponseWriter, r *http.Request, albumID int64) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}

	writeback := r.FormValue("writeback") == "1"
	newTitle := strings.TrimSpace(r.FormValue("title"))
	newArtist := strings.TrimSpace(r.FormValue("artist"))
	newYearStr := strings.TrimSpace(r.FormValue("year"))
	newYear, _ := strconv.Atoi(newYearStr)

	if newTitle == "" || newArtist == "" {
		http.Error(w, "title and artist are required", 400)
		return
	}

	// Get current album info for comparison.
	var curTitle, curArtist string
	var curYear int
	var libraryID, curArtistID int64
	if err := h.db.QueryRowContext(r.Context(), `
		SELECT al.title, al.year, al.library_id, al.album_artist_id, ar.name
		FROM music_albums al
		JOIN music_artists ar ON ar.id = al.album_artist_id
		WHERE al.id=?
	`, albumID).Scan(&curTitle, &curYear, &libraryID, &curArtistID, &curArtist); err != nil {
		http.NotFound(w, r)
		return
	}

	// Build selected track set for writeback mode.
	var selectedSet map[int64]bool
	if writeback {
		selectedIDs := r.Form["selected_track"]
		if len(selectedIDs) == 0 {
			http.Error(w, "no tracks selected", 400)
			return
		}
		selectedSet = make(map[int64]bool, len(selectedIDs))
		for _, s := range selectedIDs {
			if id, err := strconv.ParseInt(s, 10, 64); err == nil && id > 0 {
				selectedSet[id] = true
			}
		}
		if len(selectedSet) == 0 {
			http.Error(w, "no valid tracks selected", 400)
			return
		}
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer tx.Rollback()

	// Resolve target artist ID (may be new).
	targetArtistID := curArtistID
	if newArtist != curArtist {
		if _, err := tx.ExecContext(r.Context(),
			"INSERT OR IGNORE INTO music_artists (library_id, name) VALUES (?, ?)",
			libraryID, newArtist); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if err := tx.QueryRowContext(r.Context(),
			"SELECT id FROM music_artists WHERE library_id=? AND name=?",
			libraryID, newArtist).Scan(&targetArtistID); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
	}

	// Determine target album for writeback.
	targetAlbumID := albumID
	if writeback {
		// Count total tracks in the source album.
		var totalTracks int
		if err := tx.QueryRowContext(r.Context(),
			"SELECT COUNT(*) FROM music_tracks WHERE album_id=?",
			albumID).Scan(&totalTracks); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}

		allSelected := len(selectedSet) >= totalTracks

		compVal := 0
		if r.FormValue("compilation") == "1" {
			compVal = 1
		}

		if allSelected {
			// All tracks selected: update album in place.
			if _, err := tx.ExecContext(r.Context(),
				"UPDATE music_albums SET title=?, year=?, album_artist_id=?, is_compilation=? WHERE id=?",
				newTitle, newYear, targetArtistID, compVal, albumID); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
		} else {
			// Partial selection: find or create target album, move selected tracks.
			err := tx.QueryRowContext(r.Context(),
				"SELECT id FROM music_albums WHERE library_id=? AND title=? AND album_artist_id=? AND id!=?",
				libraryID, newTitle, targetArtistID, albumID).Scan(&targetAlbumID)
			if err == sql.ErrNoRows {
				res, err := tx.ExecContext(r.Context(), `
					INSERT INTO music_albums (library_id, title, year, artist_id, album_artist_id, is_compilation, match_status)
					VALUES (?, ?, ?, ?, ?, ?, 'manual')`,
					libraryID, newTitle, newYear, targetArtistID, targetArtistID, compVal)
				if err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				targetAlbumID, _ = res.LastInsertId()
			} else if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}

			// Move selected tracks to target album.
			for id := range selectedSet {
				if _, err := tx.ExecContext(r.Context(),
					"UPDATE music_tracks SET album_id=? WHERE id=? AND album_id=?",
					targetAlbumID, id, albumID); err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
			}
		}

		// Single-track edit: update that track's title, artist, track_no, disc_no.
		if singleIDStr := strings.TrimSpace(r.FormValue("single_track_id")); singleIDStr != "" {
			singleID, err := strconv.ParseInt(singleIDStr, 10, 64)
			if err == nil && singleID > 0 && selectedSet[singleID] {
				stTitle := strings.TrimSpace(r.FormValue("single_track_title"))
				stTrackNo, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("single_track_no")))
				stDiscNo, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("single_track_disc")))

				if stTitle != "" {
					if _, err := tx.ExecContext(r.Context(),
						"UPDATE music_tracks SET title=?, track_no=?, disc_no=? WHERE id=? AND album_id=?",
						stTitle, stTrackNo, stDiscNo, singleID, targetAlbumID); err != nil {
						http.Error(w, err.Error(), 500)
						return
					}
				}

				stArtist := strings.TrimSpace(r.FormValue("single_track_artist"))
				if stArtist != "" {
					if _, err := tx.ExecContext(r.Context(),
						"INSERT OR IGNORE INTO music_artists (library_id, name) VALUES (?, ?)",
						libraryID, stArtist); err != nil {
						http.Error(w, err.Error(), 500)
						return
					}
					var stArtistID int64
					if err := tx.QueryRowContext(r.Context(),
						"SELECT id FROM music_artists WHERE library_id=? AND name=?",
						libraryID, stArtist).Scan(&stArtistID); err != nil {
						http.Error(w, err.Error(), 500)
						return
					}
					if _, err := tx.ExecContext(r.Context(),
						"UPDATE music_tracks SET artist_id=? WHERE id=? AND album_id=?",
						stArtistID, singleID, targetAlbumID); err != nil {
						http.Error(w, err.Error(), 500)
						return
					}
				}
			}
		}
	} else {
		// Non-writeback mode: update album in place + inline track titles.
		if _, err := tx.ExecContext(r.Context(),
			"UPDATE music_albums SET title=?, year=?, album_artist_id=? WHERE id=?",
			newTitle, newYear, targetArtistID, albumID); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}

		trackIDs := r.Form["track_id"]
		for _, tidStr := range trackIDs {
			tid, err := strconv.ParseInt(tidStr, 10, 64)
			if err != nil {
				continue
			}
			newTrackTitle := strings.TrimSpace(r.FormValue(fmt.Sprintf("track_title_%d", tid)))
			if newTrackTitle == "" {
				continue
			}
			newTrackNo, _ := strconv.Atoi(strings.TrimSpace(r.FormValue(fmt.Sprintf("track_no_%d", tid))))
			if _, err := tx.ExecContext(r.Context(),
				"UPDATE music_tracks SET title=?, track_no=? WHERE id=? AND album_id=?",
				newTrackTitle, newTrackNo, tid, albumID); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
		}
	}

	// Set match_status to 'manual' on the target album.
	if _, err := tx.ExecContext(r.Context(),
		"UPDATE music_albums SET match_status='manual' WHERE id=?",
		targetAlbumID); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	if err := tx.Commit(); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	// Write tags to audio files if in writeback mode (only selected tracks).
	if writeback {
		rows, err := h.db.QueryContext(r.Context(), `
			SELECT t.id, t.abs_path, t.title, t.track_no, t.disc_no, ar.name
			FROM music_tracks t
			JOIN music_artists ar ON ar.id = t.artist_id
			WHERE t.album_id=?
		`, targetAlbumID)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer rows.Close()

		var success, errCount int
		for rows.Next() {
			var id int64
			var absPath, title, trackArtist string
			var trackNo, discNo int
			if err := rows.Scan(&id, &absPath, &title, &trackNo, &discNo, &trackArtist); err != nil {
				errCount++
				continue
			}
			if !selectedSet[id] {
				continue
			}
			fields := music.TagWriteFields{
				Title:       title,
				Artist:      trackArtist,
				AlbumArtist: newArtist,
				Album:       newTitle,
				Year:        newYear,
				TrackNo:     trackNo,
				DiscNo:      discNo,
			}
			if err := music.WriteTrackTags(absPath, fields); err != nil {
				slog.Error("tag writeback failed", "track_id", id, "path", absPath, "err", err)
				errCount++
				continue
			}
			success++
		}
		if err := rows.Err(); err != nil {
			slog.Error("tag writeback row iteration failed", "err", err)
		}

		http.Redirect(w, r, fmt.Sprintf("/music/album/edit?id=%d&writeback=1&success=%d&errors=%d", targetAlbumID, success, errCount), http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/music/album/%d", albumID), http.StatusSeeOther)
}

// --- Albums Grid ---

func (h *Handler) musicAlbums(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	libraryID := h.resolveMusicLibraryID(r)
	if libraryID == 0 {
		h.render(w, "music_albums.html", map[string]any{"Title": "Albums"})
		return
	}

	rows, err := h.db.QueryContext(r.Context(), `
SELECT al.id, al.title, al.year, al.art_path, ar.name, ar.id, COALESCE(al.is_compilation,0)
FROM music_albums al
JOIN music_artists ar ON ar.id = CASE
  WHEN al.album_artist_id > 0 THEN al.album_artist_id
  ELSE al.artist_id
END
WHERE al.library_id=?
ORDER BY lower(ar.name), al.year, lower(al.title)
`, libraryID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer rows.Close()

	type albumGridRow struct {
		ID         int64
		Title      string
		Year       int
		ArtPath    string
		ArtistName string
		ArtistID   int64
		IsComp     bool
	}
	albums := make([]albumGridRow, 0, 64)
	for rows.Next() {
		var a albumGridRow
		var art sql.NullString
		var comp int
		if err := rows.Scan(&a.ID, &a.Title, &a.Year, &art, &a.ArtistName, &a.ArtistID, &comp); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		a.ArtPath = scanNullString(art)
		a.IsComp = comp != 0
		albums = append(albums, a)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	h.render(w, "music_albums.html", map[string]any{
		"Title":     "Albums",
		"Albums":    albums,
		"LibraryID": libraryID,
	})
}

// --- Compilations ---

func (h *Handler) musicCompilations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	libraryID := h.resolveMusicLibraryID(r)
	if libraryID == 0 {
		h.render(w, "music_compilations.html", map[string]any{"Title": "Compilations"})
		return
	}
	rows, err := h.db.QueryContext(r.Context(), `
SELECT id, title, year, art_path
FROM music_albums
WHERE library_id=? AND COALESCE(is_compilation,0)=1
ORDER BY year, lower(title)
`, libraryID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	compilations := make([]compilationAlbumRow, 0, 24)
	for rows.Next() {
		var row compilationAlbumRow
		var art sql.NullString
		if err := rows.Scan(&row.ID, &row.Title, &row.Year, &art); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		row.ArtPath = scanNullString(art)
		compilations = append(compilations, row)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	h.render(w, "music_compilations.html", map[string]any{
		"Title":        "Compilations",
		"LibraryID":    libraryID,
		"Compilations": compilations,
	})
}

// --- Player ---

func (h *Handler) musicPlayer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	albumIDStr := strings.TrimSpace(r.URL.Query().Get("album"))
	albumID, err := strconv.ParseInt(albumIDStr, 10, 64)
	if err != nil || albumID <= 0 {
		http.NotFound(w, r)
		return
	}

	var albumTitle string
	var albumYear int
	var compInt int
	if err := h.db.QueryRowContext(r.Context(), `
SELECT title, year, COALESCE(is_compilation,0) FROM music_albums WHERE id=?
`, albumID).Scan(&albumTitle, &albumYear, &compInt); err != nil {
		http.NotFound(w, r)
		return
	}

	rows, err := h.db.QueryContext(r.Context(), `
SELECT t.id, t.album_id, al.title, al.year, t.title, ar.name, ar.id, t.track_no, t.disc_no, COALESCE(NULLIF(t.mime_type,''), 'application/octet-stream')
FROM music_tracks t
JOIN music_albums al ON al.id=t.album_id
JOIN music_artists ar ON ar.id=t.artist_id
WHERE t.album_id=?
ORDER BY t.disc_no, t.track_no, lower(t.title)
`, albumID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer rows.Close()

	tracks := make([]trackRow, 0, 32)
	for rows.Next() {
		var t trackRow
		if err := rows.Scan(&t.ID, &t.AlbumID, &t.AlbumTitle, &t.AlbumYear, &t.Title, &t.Artist, &t.ArtistID, &t.TrackNo, &t.DiscNo, &t.MIME); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		t.ArtistDisplay = t.Artist
		tracks = append(tracks, t)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	shuffle := strings.TrimSpace(r.URL.Query().Get("shuffle")) == "1"
	startTrackID := int64(0)
	if v := strings.TrimSpace(r.URL.Query().Get("track")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			startTrackID = n
		}
	}

	backURL := fmt.Sprintf("/music/album/%d", albumID)

	h.render(w, "player.html", map[string]any{
		"Title":        "Player",
		"PlayerTitle":  albumTitle,
		"BackURL":      backURL,
		"QueueTracks":  tracks,
		"Shuffle":      shuffle,
		"StartTrackID": startTrackID,
	})
}

// --- Stream Track ---

func (h *Handler) streamTrack(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	idStr := strings.TrimPrefix(r.URL.Path, "/stream/track/")
	idStr = path.Clean("/" + idStr)
	idStr = strings.TrimPrefix(idStr, "/")
	trackID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || trackID <= 0 {
		http.NotFound(w, r)
		return
	}

	var absPath string
	var mt sql.NullString
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT abs_path, COALESCE(NULLIF(mime_type,''), 'application/octet-stream') FROM music_tracks WHERE id=?",
		trackID,
	).Scan(&absPath, &mt); err != nil {
		http.NotFound(w, r)
		return
	}

	mediaRoot := filepath.Clean(h.cfg.MediaRoot)
	clean, err := pathguard.ResolveExistingUnderRoot(mediaRoot, absPath)
	if err != nil {
		http.Error(w, "track path is outside media root", 500)
		return
	}

	f, err := os.Open(clean)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), 500)
		return
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	mimeType := "application/octet-stream"
	if mt.Valid && strings.TrimSpace(mt.String) != "" {
		mimeType = strings.Split(mt.String, ";")[0]
	}
	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("Accept-Ranges", "bytes")
	http.ServeContent(w, r, filepath.Base(clean), st.ModTime(), f)
}

// --- Play Event ---

func (h *Handler) musicPlayEvent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var in playEventInput
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		http.Error(w, "invalid json payload", http.StatusBadRequest)
		return
	}
	if in.TrackID <= 0 {
		http.Error(w, "track_id is required", http.StatusBadRequest)
		return
	}
	if in.PlayedMS < 0 {
		http.Error(w, "played_ms must be >= 0", http.StatusBadRequest)
		return
	}
	if in.PlayedMS > 6*60*60*1000 {
		in.PlayedMS = 6 * 60 * 60 * 1000
	}
	if strings.TrimSpace(in.Source) == "" {
		in.Source = "unknown"
	}
	if len(in.Source) > 32 {
		in.Source = strings.TrimSpace(in.Source[:32])
	}

	// Ignore very short partial listens.
	if !in.Completed && in.PlayedMS < 15*1000 {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "recorded": false})
		return
	}

	var libraryID, artistID, albumID int64
	if err := h.db.QueryRowContext(r.Context(), `
SELECT library_id, artist_id, album_id FROM music_tracks WHERE id=?
`, in.TrackID).Scan(&libraryID, &artistID, &albumID); err != nil {
		if err == sql.ErrNoRows {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	completed := 0
	if in.Completed {
		completed = 1
	}
	if _, err := h.db.ExecContext(r.Context(), `
INSERT INTO play_history (track_id, library_id, artist_id, album_id, played_ms, completed, source)
VALUES (?, ?, ?, ?, ?, ?, ?)
`, in.TrackID, libraryID, artistID, albumID, in.PlayedMS, completed, in.Source); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "recorded": true})
}

// --- Duplicates ---

func (h *Handler) musicDuplicates(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	libraryID := h.resolveMusicLibraryID(r)
	if libraryID == 0 {
		h.render(w, "music_duplicates.html", map[string]any{
			"Title": "Duplicate Albums",
		})
		return
	}

	groups, err := match.FindDuplicateAlbums(r.Context(), h.db, libraryID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	h.render(w, "music_duplicates.html", map[string]any{
		"Title":  "Duplicate Albums",
		"Groups": groups,
	})
}

func (h *Handler) musicDuplicatesMerge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	targetID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("target")), 10, 64)
	if err != nil || targetID <= 0 {
		http.Error(w, "invalid target", 400)
		return
	}
	sourceID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("source")), 10, 64)
	if err != nil || sourceID <= 0 {
		http.Error(w, "invalid source", 400)
		return
	}

	if err := match.MergeAlbums(r.Context(), h.db, targetID, sourceID); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	http.Redirect(w, r, "/music/duplicates", http.StatusSeeOther)
}

// --- Art Serving ---

func (h *Handler) albumArt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	idStr := strings.TrimPrefix(r.URL.Path, "/art/album/")
	idStr = path.Clean("/" + idStr)
	idStr = strings.TrimPrefix(idStr, "/")
	albumID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || albumID <= 0 {
		http.NotFound(w, r)
		return
	}

	var artPath sql.NullString
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT art_path FROM music_albums WHERE id=?", albumID,
	).Scan(&artPath); err != nil {
		http.NotFound(w, r)
		return
	}

	ap := scanNullString(artPath)
	if ap == "" {
		h.serveStaticImageFallback(w, r, "missing.album.webp", "image/webp")
		return
	}

	dataDir := filepath.Clean(h.cfg.DataDir)
	clean, err := pathguard.ResolveExistingUnderRoot(dataDir, ap)
	if err != nil {
		h.serveStaticImageFallback(w, r, "missing.album.webp", "image/webp")
		return
	}

	f, err := os.Open(clean)
	if err != nil {
		h.serveStaticImageFallback(w, r, "missing.album.webp", "image/webp")
		return
	}
	defer f.Close()

	ct := artMIMEFromExt(clean)
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = io.Copy(w, f)
}

func (h *Handler) artistArt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	idStr := strings.TrimPrefix(r.URL.Path, "/art/artist/")
	idStr = path.Clean("/" + idStr)
	idStr = strings.TrimPrefix(idStr, "/")
	artistID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || artistID <= 0 {
		http.NotFound(w, r)
		return
	}

	var artPath sql.NullString
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT art_path FROM music_artists WHERE id=?", artistID,
	).Scan(&artPath); err != nil {
		http.NotFound(w, r)
		return
	}

	ap := scanNullString(artPath)
	if ap == "" {
		http.NotFound(w, r)
		return
	}

	dataDir := filepath.Clean(h.cfg.DataDir)
	clean, err := pathguard.ResolveExistingUnderRoot(dataDir, ap)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	f, err := os.Open(clean)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	ct := artMIMEFromExt(clean)
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = io.Copy(w, f)
}

func (h *Handler) serveStaticImageFallback(w http.ResponseWriter, r *http.Request, fileName, contentType string) {
	fp := filepath.Join(h.staticDir, fileName)
	f, err := os.Open(fp)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = io.Copy(w, f)
}

func artMIMEFromExt(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	default:
		return "application/octet-stream"
	}
}
