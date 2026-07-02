package web

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"hespera/internal/match"
	"hespera/internal/music"
	"hespera/internal/pathguard"
	"hespera/internal/scan"
)

// maxAlbumArtBytes caps a manually uploaded cover image, matching the Cover Art
// Archive download cap (coverart.go).
const maxAlbumArtBytes = 15 << 20

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

	recentlyPlayed, err := h.loadRecentlyPlayedArtists(r.Context(), libraryID, 18)
	if err != nil {
		httpError(w, 500, "internal server error", "db query failed", "handler", "musicHome", "err", err)
		return
	}

	recentlyAddedAlbums, err := h.loadRecentlyAddedAlbums(r.Context(), libraryID, 18)
	if err != nil {
		httpError(w, 500, "internal server error", "load recently added albums failed", "handler", "musicHome", "err", err)
		return
	}

	// Artists (paginated — this tab is the primary artist browse; a ?q= filters
	// it by name). The artist EXISTS-an-album predicate + optional name filter is
	// shared by the COUNT and the page query.
	q := searchParam(r)
	const artistWhere = `
FROM music_artists a
WHERE a.library_id=?
  AND EXISTS (SELECT 1 FROM music_albums al WHERE al.album_artist_id=a.id AND COALESCE(al.is_compilation,0)=0)`
	artistArgs := []any{libraryID}
	artistFilter := ""
	if q != "" {
		artistFilter = " AND lower(a.name) LIKE ?"
		artistArgs = append(artistArgs, likeContains(q))
	}

	var artistTotal int
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT COUNT(*) "+artistWhere+artistFilter, artistArgs...).Scan(&artistTotal); err != nil {
		httpError(w, 500, "internal server error", "db count failed", "handler", "musicHome", "err", err)
		return
	}
	artistNav, artistOffset := paginate(pageParam(r), artistTotal, "/music")
	artistNav = artistNav.withQuery(q)

	artistRows, err := h.db.QueryContext(r.Context(),
		`SELECT a.id, a.name, a.art_path,
       (SELECT COUNT(*) FROM music_tracks t JOIN music_albums al ON al.id=t.album_id
         WHERE t.artist_id=a.id AND COALESCE(al.is_compilation,0)=0) AS track_count,
       (SELECT COUNT(*) FROM play_history ph WHERE ph.artist_id=a.id) AS play_count,
       COALESCE((SELECT MAX(ph.created_at) FROM play_history ph WHERE ph.artist_id=a.id), '') AS last_played `+
			artistWhere+artistFilter+" ORDER BY lower(a.name) LIMIT ? OFFSET ?",
		append(artistArgs, listPageSize, artistOffset)...)
	if err != nil {
		httpError(w, 500, "internal server error", "db query failed", "handler", "musicHome", "err", err)
		return
	}
	defer artistRows.Close()

	artists := make([]artistRow, 0, 64)
	for artistRows.Next() {
		var a artistRow
		var art sql.NullString
		if err := artistRows.Scan(&a.ID, &a.Name, &art, &a.Count, &a.PlayCount, &a.LastPlayedRaw); err != nil {
			httpError(w, 500, "internal server error", "row scan failed", "handler", "musicHome", "err", err)
			return
		}
		a.ArtPath = scanNullString(art)
		a.LastPlayedText = formatPlayTimestamp(a.LastPlayedRaw)
		artists = append(artists, a)
	}
	if err := artistRows.Err(); err != nil {
		httpError(w, 500, "internal server error", "rows iteration failed", "handler", "musicHome", "err", err)
		return
	}

	// Compilations: a capped preview on the home tab — the full, paginated list
	// lives at /music/compilations. Fetch one extra to know whether to show the
	// "see all" link.
	const compPreview = 24
	compRows, err := h.db.QueryContext(r.Context(), `
SELECT id, title, year, art_path
FROM music_albums
WHERE library_id=? AND COALESCE(is_compilation,0)=1
ORDER BY year, lower(title)
LIMIT ?
`, libraryID, compPreview+1)
	if err != nil {
		httpError(w, 500, "internal server error", "db query failed", "handler", "musicHome", "err", err)
		return
	}
	defer compRows.Close()

	compilations := make([]compilationAlbumRow, 0, 32)
	for compRows.Next() {
		var row compilationAlbumRow
		var art sql.NullString
		if err := compRows.Scan(&row.ID, &row.Title, &row.Year, &art); err != nil {
			httpError(w, 500, "internal server error", "row scan failed", "handler", "musicHome", "err", err)
			return
		}
		row.ArtPath = scanNullString(art)
		compilations = append(compilations, row)
	}
	moreCompilations := len(compilations) > compPreview
	if moreCompilations {
		compilations = compilations[:compPreview]
	}
	if err := compRows.Err(); err != nil {
		httpError(w, 500, "internal server error", "rows iteration failed", "handler", "musicHome", "err", err)
		return
	}

	h.render(w, "music_home.html", map[string]any{
		"Title":               "Music",
		"LibraryID":           libraryID,
		"RecentlyPlayed":      recentlyPlayed,
		"RecentlyAddedAlbums": recentlyAddedAlbums,
		"Artists":             artists,
		"ArtistsPage":         artistNav,
		"ArtistsSearch":       searchBox{Action: "/music", Q: q},
		"Compilations":        compilations,
		"MoreCompilations":    moreCompilations,
		"EraPicker":           h.eraPicker(r.Context(), libraryID),
	})
}

// eraPickerData is the context for the shuffle-era range picker partial
// (partials_era_picker.html): the year span of a music library's albums plus the
// library id the Play/Shuffle links target.
type eraPickerData struct {
	MinYear   int
	MaxYear   int
	LibraryID int64
}

// eraPicker returns the era-picker context for a music library, or nil if the
// library has no year-tagged albums (nothing to shuffle by era → the `with
// .EraPicker` block renders nothing). The single source for the picker's range,
// shared by musicHome and the Home Quick-Play card.
func (h *Handler) eraPicker(ctx context.Context, libraryID int64) *eraPickerData {
	var minY, maxY int
	err := h.db.QueryRowContext(ctx,
		"SELECT COALESCE(MIN(year), 0), COALESCE(MAX(year), 0) FROM music_albums WHERE library_id=? AND year>0",
		libraryID).Scan(&minY, &maxY)
	if err != nil || minY <= 0 || maxY <= 0 {
		return nil
	}
	return &eraPickerData{MinYear: minY, MaxYear: maxY, LibraryID: libraryID}
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

// loadRecentlyPlayedArtists returns artists ordered by their most recent play,
// for the music home "Recently Played" row and the landing-page dashboard.
func (h *Handler) loadRecentlyPlayedArtists(ctx context.Context, libraryID int64, limit int) ([]musicHomeArtistRow, error) {
	if libraryID <= 0 {
		return []musicHomeArtistRow{}, nil
	}
	rows, err := h.db.QueryContext(ctx, `
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
LIMIT ?
`, libraryID, libraryID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]musicHomeArtistRow, 0, limit)
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

// --- Artist Detail ---

func (h *Handler) musicArtistAlbums(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	artistID, err := pathID(r, "/music/artist/")
	if err != nil {
		http.NotFound(w, r)
		return
	}

	var libraryID int64
	var artistName string
	var artistArt sql.NullString
	var artistBio sql.NullString
	var bioSourceURL sql.NullString
	var artistMBID, similarJSON, similarFetchedAt sql.NullString
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT library_id, name, art_path, bio, bio_source_url, musicbrainz_id, similar_json, similar_fetched_at FROM music_artists WHERE id=?",
		artistID,
	).Scan(&libraryID, &artistName, &artistArt, &artistBio, &bioSourceURL, &artistMBID, &similarJSON, &similarFetchedAt); err != nil {
		http.NotFound(w, r)
		return
	}

	// Lazily fetch the similar-artists list the first time, in the background
	// (keyless, like the actor-bio backfill). Gated by similar_fetched_at so a
	// cache-miss view doesn't re-enqueue. Only artists with an MBID can be queried.
	mbid := scanNullString(artistMBID)
	if mbid != "" && scanNullString(similarFetchedAt) == "" {
		aid := artistID
		h.enqueueMusicFetch(r.Context(), fmt.Sprintf("artist-similar:%d", aid), "artist_similar_fetch",
			func(ctx context.Context, m *match.Matcher) error { return h.fetchArtistSimilar(ctx, m, aid, mbid) })
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
		httpError(w, 500, "internal server error", "db query failed", "handler", "musicArtistAlbums", "err", err)
		return
	}
	defer rows.Close()

	albums := make([]artistAlbumRow, 0, 16)
	for rows.Next() {
		var a artistAlbumRow
		var art sql.NullString
		var comp int
		if err := rows.Scan(&a.ID, &a.Title, &a.Year, &art, &comp, &a.Count, &a.PlayCount); err != nil {
			httpError(w, 500, "internal server error", "row scan failed", "handler", "musicArtistAlbums", "err", err)
			return
		}
		a.ArtPath = scanNullString(art)
		a.IsComp = comp != 0
		albums = append(albums, a)
	}
	if err := rows.Err(); err != nil {
		httpError(w, 500, "internal server error", "rows iteration failed", "handler", "musicArtistAlbums", "err", err)
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
		httpError(w, 500, "internal server error", "db query failed", "handler", "musicArtistAlbums", "err", err)
		return
	}
	defer compRows.Close()

	comps := make([]compilationAlbumRow, 0, 8)
	for compRows.Next() {
		var c compilationAlbumRow
		var art sql.NullString
		if err := compRows.Scan(&c.ID, &c.Title, &c.Year, &art); err != nil {
			httpError(w, 500, "internal server error", "row scan failed", "handler", "musicArtistAlbums", "err", err)
			return
		}
		c.ArtPath = scanNullString(art)
		comps = append(comps, c)
	}
	if err := compRows.Err(); err != nil {
		httpError(w, 500, "internal server error", "rows iteration failed", "handler", "musicArtistAlbums", "err", err)
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
		httpError(w, 500, "internal server error", "db query failed", "handler", "musicArtistAlbums", "err", err)
		return
	}
	defer trackRows.Close()

	tracks := make([]trackRow, 0, 64)
	for trackRows.Next() {
		var t trackRow
		if err := trackRows.Scan(&t.ID, &t.AlbumID, &t.AlbumTitle, &t.AlbumYear, &t.Title, &t.Artist, &t.TrackNo, &t.DiscNo, &t.MIME); err != nil {
			httpError(w, 500, "internal server error", "row scan failed", "handler", "musicArtistAlbums", "err", err)
			return
		}
		tracks = append(tracks, t)
	}
	if err := trackRows.Err(); err != nil {
		httpError(w, 500, "internal server error", "rows iteration failed", "handler", "musicArtistAlbums", "err", err)
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
		"Similar":      h.loadArtistSimilarCards(r.Context(), scanNullString(similarJSON)),
	})
}

// --- Album Tracks ---

func (h *Handler) musicAlbumTracks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	albumID, err := pathID(r, "/music/album/")
	if err != nil {
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
		httpError(w, 500, "internal server error", "db query failed", "handler", "musicAlbumTracks", "err", err)
		return
	}
	defer rows.Close()

	var tracks []trackRow
	discBuckets := map[int][]trackRow{}
	discOrder := make([]int, 0, 2)
	for rows.Next() {
		var t trackRow
		if err := rows.Scan(&t.ID, &t.Title, &t.Artist, &t.ArtistID, &t.TrackNo, &t.DiscNo, &t.MIME); err != nil {
			httpError(w, 500, "internal server error", "row scan failed", "handler", "musicAlbumTracks", "err", err)
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
		httpError(w, 500, "internal server error", "rows iteration failed", "handler", "musicAlbumTracks", "err", err)
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
	var albumTitle string
	var albumYear int
	var artistName string
	var matchStatus string
	var currentRGMBID string
	if err := h.db.QueryRowContext(r.Context(), `
		SELECT al.title, al.year, ar.name, al.match_status, COALESCE(al.musicbrainz_id, '')
		FROM music_albums al
		JOIN music_artists ar ON ar.id = al.album_artist_id
		WHERE al.id=?
	`, albumID).Scan(&albumTitle, &albumYear, &artistName, &matchStatus, &currentRGMBID); err != nil {
		http.NotFound(w, r)
		return
	}

	var successCount, errorCount, movedCount int
	if s := r.URL.Query().Get("success"); s != "" {
		successCount, _ = strconv.Atoi(s)
	}
	if s := r.URL.Query().Get("errors"); s != "" {
		errorCount, _ = strconv.Atoi(s)
	}
	if s := r.URL.Query().Get("moved"); s != "" {
		movedCount, _ = strconv.Atoi(s)
	}

	h.render(w, "music_album_edit.html", map[string]any{
		"Title":         "Edit Album",
		"AlbumID":       albumID,
		"AlbumTitle":    albumTitle,
		"AlbumYear":     albumYear,
		"ArtistName":    artistName,
		"IsMatched":     matchStatus == "matched",
		"CurrentRGMBID": currentRGMBID,
		"Success":       successCount,
		"Errors":        errorCount,
		"Moved":         movedCount,
	})
}

func (h *Handler) musicAlbumEditPOST(w http.ResponseWriter, r *http.Request, albumID int64) {
	if err := r.ParseForm(); err != nil {
		httpError(w, 400, "bad request", "parse form failed", "handler", "musicAlbumEditPOST", "err", err)
		return
	}

	newAlbum := strings.TrimSpace(r.FormValue("title"))
	newAlbumArtist := strings.TrimSpace(r.FormValue("artist"))
	newYear, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("year")))

	if newAlbum == "" || newAlbumArtist == "" {
		http.Error(w, "title and artist are required", 400)
		return
	}

	// Get library_id for this album.
	var libraryID int64
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT library_id FROM music_albums WHERE id=?", albumID).Scan(&libraryID); err != nil {
		http.NotFound(w, r)
		return
	}

	// Query abs_path for every track on the album — the edit writes album-level
	// title/artist/year across all of them (per-track fields are edited via the
	// per-track editor, not here).
	type trackInfo struct {
		ID      int64
		AbsPath string
	}
	var affectedTracks []trackInfo
	rows, err := h.db.QueryContext(r.Context(),
		"SELECT id, abs_path FROM music_tracks WHERE album_id=?", albumID)
	if err != nil {
		httpError(w, 500, "internal server error", "db query failed", "handler", "musicAlbumEditPOST", "err", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var t trackInfo
		if err := rows.Scan(&t.ID, &t.AbsPath); err != nil {
			httpError(w, 500, "internal server error", "row scan failed", "handler", "musicAlbumEditPOST", "err", err)
			return
		}
		affectedTracks = append(affectedTracks, t)
	}
	if err := rows.Err(); err != nil {
		httpError(w, 500, "internal server error", "rows iteration failed", "handler", "musicAlbumEditPOST", "err", err)
		return
	}

	if len(affectedTracks) == 0 {
		http.Error(w, "no tracks found", 404)
		return
	}

	// Write tags to files.
	var successPaths []string
	var errCount int
	var missingCount int
	for _, t := range affectedTracks {
		// Read current tags to preserve fields we're not editing.
		meta, err := music.ReadTrackMeta(t.AbsPath)
		if err != nil {
			// A missing file (folder renamed/moved on disk without a rescan) is the
			// common cause and is actionable — run a Scan to resync — so count it
			// separately from genuine read/write errors.
			if errors.Is(err, os.ErrNotExist) {
				missingCount++
			} else {
				slog.Error("edit: read tags failed", "path", t.AbsPath, "err", err)
				errCount++
			}
			continue
		}

		fields := music.TagWriteFields{
			Album:       newAlbum,
			AlbumArtist: newAlbumArtist,
			Year:        newYear,
			// Preserve per-track fields from file.
			Title:   meta.Title,
			Artist:  meta.Artist,
			TrackNo: meta.Track,
			DiscNo:  meta.Disc,
		}

		if err := music.WriteTrackTags(t.AbsPath, fields); err != nil {
			slog.Error("edit: write tags failed", "path", t.AbsPath, "err", err)
			errCount++
			continue
		}
		successPaths = append(successPaths, t.AbsPath)
	}

	if len(successPaths) == 0 {
		// Nothing written — redirect back with the failure breakdown so the edit
		// page can show an actionable message (moved files → run a Scan first).
		http.Redirect(w, r, fmt.Sprintf("/music/album/edit?id=%d&moved=%d&errors=%d", albumID, missingCount, errCount), http.StatusSeeOther)
		return
	}

	// Rescan successfully-written files.
	scanner := scan.New(h.cfg, h.db)
	if err := scanner.ScanFiles(r.Context(), libraryID, successPaths); err != nil {
		slog.Error("edit: rescan failed", "err", err)
	}

	// Determine where tracks ended up after rescan.
	var newAlbumID int64
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT album_id FROM music_tracks WHERE abs_path=? AND library_id=?",
		successPaths[0], libraryID).Scan(&newAlbumID); err != nil {
		newAlbumID = albumID
	}

	if errCount > 0 || missingCount > 0 {
		http.Redirect(w, r, fmt.Sprintf("/music/album/edit?id=%d&success=%d&moved=%d&errors=%d", newAlbumID, len(successPaths), missingCount, errCount), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/music/album/%d", newAlbumID), http.StatusSeeOther)
}

// musicTrackEdit serves the per-track tag editor (the "Edit" button on each row of
// the album track list). GET renders a form pre-filled with the track's current
// tags; POST writes them to the file and rescans. Editing Album/AlbumArtist/Year
// can move the track to a different album (the scan re-derives album membership).
func (h *Handler) musicTrackEdit(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimSpace(r.URL.Query().Get("id"))
	trackID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || trackID <= 0 {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		h.musicTrackEditGET(w, r, trackID)
	case http.MethodPost:
		h.musicTrackEditPOST(w, r, trackID)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (h *Handler) musicTrackEditGET(w http.ResponseWriter, r *http.Request, trackID int64) {
	var (
		title, trackArtist, album, albumArtist string
		year, trackNo, discNo                  int
		albumID                                int64
	)
	// Pre-fill from the DB (which mirrors the file tags after the last scan),
	// mirroring musicAlbumEditGET. The track's album-level fields come from its
	// album row; the per-track fields from the track row.
	if err := h.db.QueryRowContext(r.Context(), `
		SELECT t.title, ar.name, al.title, aa.name, al.year, t.track_no, t.disc_no, al.id
		FROM music_tracks t
		JOIN music_artists ar ON ar.id = t.artist_id
		JOIN music_albums al ON al.id = t.album_id
		JOIN music_artists aa ON aa.id = al.album_artist_id
		WHERE t.id=?
	`, trackID).Scan(&title, &trackArtist, &album, &albumArtist, &year, &trackNo, &discNo, &albumID); err != nil {
		http.NotFound(w, r)
		return
	}

	h.render(w, "music_track_edit.html", map[string]any{
		"Title":       "Edit Track",
		"TrackID":     trackID,
		"AlbumID":     albumID,
		"TrackTitle":  title,
		"TrackArtist": trackArtist,
		"Album":       album,
		"AlbumArtist": albumArtist,
		"Year":        year,
		"TrackNo":     trackNo,
		"DiscNo":      discNo,
		"Error":       r.URL.Query().Get("error") == "1",
	})
}

func (h *Handler) musicTrackEditPOST(w http.ResponseWriter, r *http.Request, trackID int64) {
	if err := r.ParseForm(); err != nil {
		httpError(w, 400, "bad request", "parse form failed", "handler", "musicTrackEditPOST", "err", err)
		return
	}
	title := strings.TrimSpace(r.FormValue("title"))
	album := strings.TrimSpace(r.FormValue("album"))
	albumArtist := strings.TrimSpace(r.FormValue("album_artist"))
	artist := strings.TrimSpace(r.FormValue("artist"))
	year, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("year")))
	trackNo, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("track_no")))
	discNo, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("disc_no")))

	if title == "" || album == "" || albumArtist == "" {
		http.Error(w, "title, album, and album artist are required", 400)
		return
	}

	var absPath string
	var libraryID int64
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT abs_path, library_id FROM music_tracks WHERE id=?", trackID).Scan(&absPath, &libraryID); err != nil {
		http.NotFound(w, r)
		return
	}

	// Empty/zero fields are left untouched on the file (WriteTrackTags guards each
	// field), so a cleared input means "keep current", not "blank it".
	fields := music.TagWriteFields{
		Title:       title,
		Artist:      artist,
		Album:       album,
		AlbumArtist: albumArtist,
		Year:        year,
		TrackNo:     trackNo,
		DiscNo:      discNo,
	}
	if err := music.WriteTrackTags(absPath, fields); err != nil {
		// Most likely the file is missing/moved (run a Scan first) or an
		// unsupported container. Surface it rather than failing silently.
		slog.Error("track edit: write tags failed", "path", absPath, "err", err)
		http.Redirect(w, r, fmt.Sprintf("/music/track/edit?id=%d&error=1", trackID), http.StatusSeeOther)
		return
	}

	scanner := scan.New(h.cfg, h.db)
	if err := scanner.ScanFiles(r.Context(), libraryID, []string{absPath}); err != nil {
		slog.Error("track edit: rescan failed", "err", err)
	}

	// The track row keeps its id (abs_path is unchanged) but its album_id may now
	// point at a different/new album when Album/AlbumArtist/Year changed.
	var newAlbumID int64
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT album_id FROM music_tracks WHERE id=?", trackID).Scan(&newAlbumID); err != nil || newAlbumID == 0 {
		http.Redirect(w, r, "/music", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/music/album/%d", newAlbumID), http.StatusSeeOther)
}

// musicAlbumArtUpload lets a user manually set an album's cover art when none
// could be matched (Cover Art Archive has no image, or the album mis-matched).
// The uploaded file is validated as a real image, stored under thumbs/music, and
// art_path is set unconditionally. The scanner/matcher art writers are
// empty-only-guarded, so manual art is never overwritten by a later rescan/match.
// Mounted under /music/ (not /art/) so the auth + same-origin-CSRF middleware
// applies. Single-file image upload only — no fetch-by-URL (SSRF surface).
func (h *Handler) musicAlbumArtUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	// Bound the whole request body before parsing — ParseMultipartForm's argument
	// only caps the in-memory portion (the rest spills to disk).
	r.Body = http.MaxBytesReader(w, r.Body, maxAlbumArtBytes+(1<<20))
	if err := r.ParseMultipartForm(maxAlbumArtBytes); err != nil {
		http.Error(w, "upload too large or malformed", http.StatusBadRequest)
		return
	}

	albumID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("album_id")), 10, 64)
	if err != nil || albumID <= 0 {
		http.Error(w, "invalid album_id", http.StatusBadRequest)
		return
	}
	var exists int
	if err := h.db.QueryRowContext(r.Context(), "SELECT 1 FROM music_albums WHERE id=?", albumID).Scan(&exists); err != nil {
		http.NotFound(w, r)
		return
	}

	file, _, err := r.FormFile("art")
	if err != nil {
		http.Error(w, "no image file provided", http.StatusBadRequest)
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxAlbumArtBytes))
	if err != nil {
		httpError(w, 500, "internal server error", "read upload failed", "handler", "musicAlbumArtUpload", "err", err)
		return
	}

	// Derive the MIME from the bytes themselves — never trust the client-supplied
	// content-type or filename. Gate on both the image check and the format
	// allowlist (jpeg/png/webp); this rejects SVG/GIF/BMP and non-images.
	detected := http.DetectContentType(data)
	if err := music.VerifyImage(detected, data); err != nil {
		http.Error(w, "file is not a valid image", http.StatusBadRequest)
		return
	}
	ext, err := music.ArtFileExt(detected)
	if err != nil {
		http.Error(w, "unsupported image format (use JPEG, PNG, or WebP)", http.StatusBadRequest)
		return
	}

	thumbDir := filepath.Join(h.cfg.DataDir, "thumbs", "music")
	if err := os.MkdirAll(thumbDir, 0o755); err != nil {
		httpError(w, 500, "internal server error", "mkdir thumbs failed", "handler", "musicAlbumArtUpload", "err", err)
		return
	}
	// Stable per-album filename so a re-upload self-overwrites (no orphan, no
	// concurrent-write race). Distinct key prefix avoids colliding with the
	// embedded-art file for the same album.
	sum := sha1.Sum([]byte(fmt.Sprintf("manual-album-%d", albumID)))
	outPath := filepath.Join(thumbDir, hex.EncodeToString(sum[:])+ext)

	// Write to a temp file then rename, so a concurrent GET never sees a
	// half-written image.
	tmp, err := os.CreateTemp(thumbDir, "art-*")
	if err != nil {
		httpError(w, 500, "internal server error", "create temp failed", "handler", "musicAlbumArtUpload", "err", err)
		return
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		_ = os.Remove(tmpName)
		httpError(w, 500, "internal server error", "write art failed", "handler", "musicAlbumArtUpload", "err", err)
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		httpError(w, 500, "internal server error", "close art failed", "handler", "musicAlbumArtUpload", "err", err)
		return
	}
	if err := os.Rename(tmpName, outPath); err != nil {
		_ = os.Remove(tmpName)
		httpError(w, 500, "internal server error", "publish art failed", "handler", "musicAlbumArtUpload", "err", err)
		return
	}

	// Unconditional override (unlike the empty-only scanner/matcher writers).
	if _, err := h.db.ExecContext(r.Context(),
		"UPDATE music_albums SET art_path=? WHERE id=?", outPath, albumID); err != nil {
		httpError(w, 500, "internal server error", "update art_path failed", "handler", "musicAlbumArtUpload", "err", err)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/music/album/%d", albumID), http.StatusSeeOther)
}

// musicAlbumArtClear removes an album's cover art (art_path=”) and resets its
// art-check timestamp so the next match run re-fetches it. Used from the album
// edit page when the current cover is wrong or absent.
func (h *Handler) musicAlbumArtClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		httpError(w, 400, "bad request", "parse form failed", "handler", "musicAlbumArtClear", "err", err)
		return
	}
	albumID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("album_id")), 10, 64)
	if err != nil || albumID <= 0 {
		http.Error(w, "invalid album_id", 400)
		return
	}
	if _, err := h.db.ExecContext(r.Context(),
		"UPDATE music_albums SET art_path='', art_checked_at='' WHERE id=?", albumID); err != nil {
		httpError(w, 500, "internal server error", "db update failed", "handler", "musicAlbumArtClear", "err", err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/music/album/edit?id=%d", albumID), http.StatusSeeOther)
}

// errInvalidImage marks a 400-class image-validation failure (vs a 500-class
// filesystem error) from writeArtistArtImage.
var errInvalidImage = errors.New("invalid image")

// musicArtistArt is the artist-image picker. GET renders candidate images from
// the configured providers (fanart.tv gallery + TheAudioDB) plus an upload form;
// POST applies a chosen provider image (validated against the freshly-fetched
// candidate set — never an arbitrary client URL), an uploaded file, or a clear.
// The chosen image is written unconditionally to music_artists.art_path so it
// survives the empty-only matcher writer. Mounted under /music/ for auth + CSRF.
func (h *Handler) musicArtistArt(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.musicArtistArtGET(w, r)
	case http.MethodPost:
		h.musicArtistArtPOST(w, r)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (h *Handler) musicArtistArtGET(w http.ResponseWriter, r *http.Request) {
	artistID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("id")), 10, 64)
	if err != nil || artistID <= 0 {
		http.NotFound(w, r)
		return
	}
	var name, mbid, artPath string
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT name, COALESCE(musicbrainz_id,''), COALESCE(art_path,'') FROM music_artists WHERE id=?", artistID,
	).Scan(&name, &mbid, &artPath); err != nil {
		http.NotFound(w, r)
		return
	}

	var candidates []match.ArtistImageCandidate
	if mbid != "" {
		matcher := match.New(h.db, h.cfg.DataDir, h.effectiveFanartKey(r.Context()), h.effectiveAudioDBKey(r.Context()), h.effectiveLastfmKey(r.Context()))
		// Bound the fanart.tv/TheAudioDB lookups so this interactive GET can't hang
		// on a slow provider; an empty result just renders the upload-only picker.
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		candidates = matcher.ArtistImageCandidates(ctx, mbid)
	}

	h.render(w, "music_artist_art.html", map[string]any{
		"Title":      "Artist image — " + name,
		"ArtistID":   artistID,
		"ArtistName": name,
		"HasMBID":    mbid != "",
		"HasArt":     artPath != "",
		"Candidates": candidates,
	})
}

func (h *Handler) musicArtistArtPOST(w http.ResponseWriter, r *http.Request) {
	// Bound the body before parsing (covers the multipart upload case). A
	// non-multipart (URL/clear) POST is parsed as a normal form below.
	r.Body = http.MaxBytesReader(w, r.Body, maxAlbumArtBytes+(1<<20))
	if err := r.ParseMultipartForm(maxAlbumArtBytes); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		http.Error(w, "upload too large or malformed", http.StatusBadRequest)
		return
	}

	artistID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("artist_id")), 10, 64)
	if err != nil || artistID <= 0 {
		http.Error(w, "invalid artist_id", http.StatusBadRequest)
		return
	}
	var mbid string
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT COALESCE(musicbrainz_id,'') FROM music_artists WHERE id=?", artistID).Scan(&mbid); err != nil {
		http.NotFound(w, r)
		return
	}
	redirect := fmt.Sprintf("/music/artist/%d", artistID)

	// Clear the current image.
	if r.FormValue("clear") == "1" {
		if _, err := h.db.ExecContext(r.Context(),
			"UPDATE music_artists SET art_path='' WHERE id=?", artistID); err != nil {
			httpError(w, 500, "internal server error", "clear artist art failed", "handler", "musicArtistArt", "err", err)
			return
		}
		http.Redirect(w, r, redirect, http.StatusSeeOther)
		return
	}

	var data []byte
	if artURL := strings.TrimSpace(r.FormValue("art_url")); artURL != "" {
		// SSRF guard: only download a URL we actually surfaced for this artist.
		// Re-fetch the candidate set and require exact membership — the server
		// never fetches an arbitrary client-supplied URL.
		if mbid == "" {
			http.Error(w, "artist has no MusicBrainz id", http.StatusBadRequest)
			return
		}
		matcher := match.New(h.db, h.cfg.DataDir, h.effectiveFanartKey(r.Context()), h.effectiveAudioDBKey(r.Context()), h.effectiveLastfmKey(r.Context()))
		allowed := false
		for _, c := range matcher.ArtistImageCandidates(r.Context(), mbid) {
			if c.URL == artURL {
				allowed = true
				break
			}
		}
		if !allowed {
			http.Error(w, "image is not a current candidate for this artist", http.StatusBadRequest)
			return
		}
		data, err = h.fetchRemoteImage(r.Context(), artURL)
		if err != nil {
			slog.Warn("artist art download failed", "handler", "musicArtistArt", "url", artURL, "err", err)
			http.Error(w, "could not download the selected image", http.StatusBadGateway)
			return
		}
	} else {
		file, _, ferr := r.FormFile("art")
		if ferr != nil {
			http.Error(w, "no image selected", http.StatusBadRequest)
			return
		}
		defer file.Close()
		data, err = io.ReadAll(io.LimitReader(file, maxAlbumArtBytes))
		if err != nil {
			httpError(w, 500, "internal server error", "read upload failed", "handler", "musicArtistArt", "err", err)
			return
		}
	}

	outPath, err := h.writeArtistArtImage(artistID, data)
	if err != nil {
		if errors.Is(err, errInvalidImage) {
			http.Error(w, "file is not a valid image (use JPEG, PNG, or WebP)", http.StatusBadRequest)
			return
		}
		httpError(w, 500, "internal server error", "write artist art failed", "handler", "musicArtistArt", "err", err)
		return
	}
	if _, err := h.db.ExecContext(r.Context(),
		"UPDATE music_artists SET art_path=? WHERE id=?", outPath, artistID); err != nil {
		httpError(w, 500, "internal server error", "update artist art_path failed", "handler", "musicArtistArt", "err", err)
		return
	}
	http.Redirect(w, r, redirect, http.StatusSeeOther)
}

// fetchRemoteImage downloads image bytes from a candidate-verified provider URL.
func (h *Handler) fetchRemoteImage(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Hespera/1.0 (+artist-art)")
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxAlbumArtBytes))
}

// writeArtistArtImage validates image bytes (MIME from content; jpeg/png/webp)
// and writes them to a stable per-artist file under thumbs/music (temp+rename).
// Returns errInvalidImage for validation failures (400-class).
func (h *Handler) writeArtistArtImage(artistID int64, data []byte) (string, error) {
	detected := http.DetectContentType(data)
	if err := music.VerifyImage(detected, data); err != nil {
		return "", errInvalidImage
	}
	ext, err := music.ArtFileExt(detected)
	if err != nil {
		return "", errInvalidImage
	}
	thumbDir := filepath.Join(h.cfg.DataDir, "thumbs", "music")
	if err := os.MkdirAll(thumbDir, 0o755); err != nil {
		return "", err
	}
	sum := sha1.Sum([]byte(fmt.Sprintf("manual-artist-%d", artistID)))
	outPath := filepath.Join(thumbDir, hex.EncodeToString(sum[:])+ext)
	tmp, err := os.CreateTemp(thumbDir, "art-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		_ = os.Remove(tmpName)
		return "", err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return "", err
	}
	if err := os.Rename(tmpName, outPath); err != nil {
		_ = os.Remove(tmpName)
		return "", err
	}
	return outPath, nil
}

// musicAlbumUnmatch fully resets an album's match — identity and cover art — so
// the next match run re-matches it from scratch. Mirrors musicMatchRematch and
// also clears art_path/art_checked_at.
func (h *Handler) musicAlbumUnmatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		httpError(w, 400, "bad request", "parse form failed", "handler", "musicAlbumUnmatch", "err", err)
		return
	}
	albumID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("album_id")), 10, 64)
	if err != nil || albumID <= 0 {
		http.Error(w, "invalid album_id", 400)
		return
	}
	if _, err := h.db.ExecContext(r.Context(), `
		UPDATE music_albums SET
			match_status='',
			musicbrainz_id='',
			artist_musicbrainz_id='',
			match_confidence=0,
			matched_at='',
			art_path='',
			art_checked_at=''
		WHERE id=?
	`, albumID); err != nil {
		httpError(w, 500, "internal server error", "db update failed", "handler", "musicAlbumUnmatch", "err", err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/music/album/edit?id=%d", albumID), http.StatusSeeOther)
}

// musicAlbumReassign is the manual release-group reassignment control — the
// album analogue of the artist disambiguation control. When the matcher binds an
// album to the wrong MusicBrainz release-group (e.g. a terse local "Grease"
// losing on title similarity to a sparse art-less stub RG instead of the real
// soundtrack RG), this lets a user paste the correct release-group MBID. It
// re-points the album's identity, clears the stale cover, and synchronously
// re-fetches art for the new RG — so a mis-matched album recovers proper art
// without sourcing and uploading an image by hand.
func (h *Handler) musicAlbumReassign(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		httpError(w, 400, "bad request", "parse form failed", "handler", "musicAlbumReassign", "err", err)
		return
	}
	albumID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("album_id")), 10, 64)
	if err != nil || albumID <= 0 {
		http.Error(w, "invalid album_id", http.StatusBadRequest)
		return
	}
	rgMBID := strings.TrimSpace(r.FormValue("release_group_mbid"))
	if !mbidPattern.MatchString(rgMBID) {
		http.Error(w, "invalid release-group MBID", http.StatusBadRequest)
		return
	}

	var exists int
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT 1 FROM music_albums WHERE id=?", albumID).Scan(&exists); err != nil {
		http.NotFound(w, r)
		return
	}

	// Re-point the album to the chosen release-group and clear the stale cover so
	// it re-fetches for the new identity. Keeps the album matched — the user is
	// asserting the correct RG. Touches only this album's identity/art; per-track
	// data and the artist row are left alone.
	if _, err := h.db.ExecContext(r.Context(), `
		UPDATE music_albums SET
			musicbrainz_id=?,
			match_status='matched',
			art_path='',
			art_checked_at=''
		WHERE id=?
	`, rgMBID, albumID); err != nil {
		httpError(w, 500, "internal server error", "db update failed", "handler", "musicAlbumReassign", "err", err)
		return
	}

	// Synchronously pull the cover for the new RG so it shows immediately.
	// Non-fatal: the identity is corrected regardless, and the next Match's
	// refetch-missing-art phase fills the cover if this network call fails.
	matcher := match.New(h.db, h.cfg.DataDir, h.effectiveFanartKey(r.Context()), h.effectiveAudioDBKey(r.Context()), h.effectiveLastfmKey(r.Context()))
	if err := matcher.RefetchAlbumArt(r.Context(), albumID); err != nil {
		slog.Warn("refetch album art after reassign failed", "album_id", albumID, "rg", rgMBID, "err", err)
	}

	http.Redirect(w, r, fmt.Sprintf("/music/album/edit?id=%d", albumID), http.StatusSeeOther)
}

func (h *Handler) musicAlbumRescan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		httpError(w, 400, "bad request", "parse form failed", "handler", "musicAlbumRescan", "err", err)
		return
	}
	albumID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("album_id")), 10, 64)
	if err != nil || albumID <= 0 {
		http.NotFound(w, r)
		return
	}

	var libraryID int64
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT library_id FROM music_albums WHERE id=?", albumID).Scan(&libraryID); err != nil {
		http.NotFound(w, r)
		return
	}

	rows, err := h.db.QueryContext(r.Context(),
		"SELECT abs_path FROM music_tracks WHERE album_id=?", albumID)
	if err != nil {
		httpError(w, 500, "internal server error", "db query failed", "handler", "musicAlbumRescan", "err", err)
		return
	}
	defer rows.Close()

	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			httpError(w, 500, "internal server error", "row scan failed", "handler", "musicAlbumRescan", "err", err)
			return
		}
		paths = append(paths, p)
	}
	if err := rows.Err(); err != nil {
		httpError(w, 500, "internal server error", "rows iteration failed", "handler", "musicAlbumRescan", "err", err)
		return
	}

	if len(paths) == 0 {
		http.Redirect(w, r, fmt.Sprintf("/music/album/%d", albumID), http.StatusSeeOther)
		return
	}

	scanner := scan.New(h.cfg, h.db)
	if err := scanner.ScanFiles(r.Context(), libraryID, paths); err != nil {
		slog.Error("rescan failed", "album_id", albumID, "err", err)
	}

	// Determine where tracks ended up.
	var newAlbumID int64
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT album_id FROM music_tracks WHERE abs_path=? AND library_id=?",
		paths[0], libraryID).Scan(&newAlbumID); err != nil {
		newAlbumID = albumID
	}

	http.Redirect(w, r, fmt.Sprintf("/music/album/%d", newAlbumID), http.StatusSeeOther)
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

	// A ?q= search matches the album title OR the (album-)artist name. The COUNT
	// uses the same JOIN+filter as the page query so total-pages stays accurate.
	q := searchParam(r)
	const albumsFrom = `
FROM music_albums al
JOIN music_artists ar ON ar.id = CASE
  WHEN al.album_artist_id > 0 THEN al.album_artist_id
  ELSE al.artist_id
END
WHERE al.library_id=?`
	args := []any{libraryID}
	filter := ""
	if q != "" {
		filter = " AND (lower(al.title) LIKE ? OR lower(ar.name) LIKE ?)"
		like := likeContains(q)
		args = append(args, like, like)
	}

	var total int
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT COUNT(*) "+albumsFrom+filter, args...).Scan(&total); err != nil {
		httpError(w, 500, "internal server error", "db count failed", "handler", "musicAlbums", "err", err)
		return
	}
	nav, offset := paginate(pageParam(r), total, "/music/albums")
	nav = nav.withQuery(q)

	rows, err := h.db.QueryContext(r.Context(),
		"SELECT al.id, al.title, al.year, al.art_path, ar.name, ar.id, COALESCE(al.is_compilation,0) "+
			albumsFrom+filter+" ORDER BY lower(ar.name), al.year, lower(al.title) LIMIT ? OFFSET ?",
		append(args, listPageSize, offset)...)
	if err != nil {
		httpError(w, 500, "internal server error", "db query failed", "handler", "musicAlbums", "err", err)
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
			httpError(w, 500, "internal server error", "row scan failed", "handler", "musicAlbums", "err", err)
			return
		}
		a.ArtPath = scanNullString(art)
		a.IsComp = comp != 0
		albums = append(albums, a)
	}
	if err := rows.Err(); err != nil {
		httpError(w, 500, "internal server error", "rows iteration failed", "handler", "musicAlbums", "err", err)
		return
	}

	h.render(w, "music_albums.html", map[string]any{
		"Title":     "Albums",
		"Albums":    albums,
		"LibraryID": libraryID,
		"Page":      nav,
		"Search":    searchBox{Action: "/music/albums", Q: q},
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
	q := searchParam(r)
	args := []any{libraryID}
	filter := ""
	if q != "" {
		filter = " AND lower(title) LIKE ?"
		args = append(args, likeContains(q))
	}

	var total int
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT COUNT(*) FROM music_albums WHERE library_id=? AND COALESCE(is_compilation,0)=1"+filter, args...).Scan(&total); err != nil {
		httpError(w, 500, "internal server error", "db count failed", "handler", "musicCompilations", "err", err)
		return
	}
	nav, offset := paginate(pageParam(r), total, "/music/compilations")
	nav = nav.withQuery(q)

	rows, err := h.db.QueryContext(r.Context(),
		"SELECT id, title, year, art_path FROM music_albums WHERE library_id=? AND COALESCE(is_compilation,0)=1"+filter+
			" ORDER BY year, lower(title) LIMIT ? OFFSET ?",
		append(args, listPageSize, offset)...)
	if err != nil {
		httpError(w, 500, "internal server error", "db query failed", "handler", "musicCompilations", "err", err)
		return
	}
	defer rows.Close()

	compilations := make([]compilationAlbumRow, 0, 24)
	for rows.Next() {
		var row compilationAlbumRow
		var art sql.NullString
		if err := rows.Scan(&row.ID, &row.Title, &row.Year, &art); err != nil {
			httpError(w, 500, "internal server error", "row scan failed", "handler", "musicCompilations", "err", err)
			return
		}
		row.ArtPath = scanNullString(art)
		compilations = append(compilations, row)
	}
	if err := rows.Err(); err != nil {
		httpError(w, 500, "internal server error", "rows iteration failed", "handler", "musicCompilations", "err", err)
		return
	}

	h.render(w, "music_compilations.html", map[string]any{
		"Title":        "Compilations",
		"LibraryID":    libraryID,
		"Compilations": compilations,
		"Page":         nav,
		"Search":       searchBox{Action: "/music/compilations", Q: q},
	})
}

// --- Player ---

// popularPerArtistLimit caps how many of each artist's top tracks the "Most
// Popular" shuffle pools, so the playlist is each artist's hits rather than the
// whole library, and isn't skewed entirely toward the most-famous artists.
const popularPerArtistLimit = 10

// popularIncludeAllMaxTracks: an artist represented by this few tracks (or
// fewer) in the library is one the user deliberately curated — every one of
// their songs is included in the Most Popular shuffle regardless of popularity,
// rather than dropping their popularity=0 (unmatched/obscure) tracks.
const popularIncludeAllMaxTracks = 4

// playerTrackSelect is the shared column list + joins for building a player
// queue; callers append their own WHERE/ORDER/LIMIT. Column order matches
// queryPlayerTracks' scan.
const playerTrackSelect = `
SELECT t.id, t.album_id, al.title, al.year, t.title, ar.name, ar.id, t.track_no, t.disc_no, COALESCE(NULLIF(t.mime_type,''), 'application/octet-stream')
FROM music_tracks t
JOIN music_albums al ON al.id=t.album_id
JOIN music_artists ar ON ar.id=t.artist_id`

// musicPlayer renders the queue-based player. The queue is built from a
// ?source=: a single album (default, ?album=N), the whole library (all),
// the most-played tracks (popular), or a year range (era, ?from=&to=). All of
// them reuse the player's existing client-side queue/shuffle/stream/lyrics; the
// collection playlists pass &shuffle=1.
// playerQueue is an ordered, source-resolved track queue plus its display
// metadata — the single shape the now-playing view (musicPlayer) and the JSON
// queue endpoint (musicQueue) both consume, built once by buildPlayerQueue.
type playerQueue struct {
	Title   string
	BackURL string
	Tracks  []trackRow
}

// buildPlayerQueue resolves the ?source= switch (single album / all / popular /
// era) into an ordered queue. notFound signals invalid params (→ 404); err is a
// server error. It is the one owner of player queue-building; both the HTML
// now-playing view and the /music/queue JSON endpoint route through it so the
// queue never grows a second, drifting copy.
func (h *Handler) buildPlayerQueue(r *http.Request) (q playerQueue, notFound bool, err error) {
	source := strings.TrimSpace(r.URL.Query().Get("source"))
	q.BackURL = "/music"

	switch source {
	case "all":
		libraryID := h.resolveMusicLibraryID(r)
		q.Tracks, err = h.queryPlayerTracks(r.Context(),
			playerTrackSelect+` WHERE t.library_id=? ORDER BY al.year, lower(al.title), t.disc_no, t.track_no`, libraryID)
		q.Title = "All Songs"
	case "popular":
		// Each artist's most popular songs (global ListenBrainz listen counts,
		// filled by the match popularity phase), pooled: the top per-artist tracks
		// across the library. A many-track artist contributes their top
		// popularPerArtistLimit songs with popularity>0; an artist represented by
		// <= popularIncludeAllMaxTracks songs is one the user curated, so ALL their
		// tracks are pooled regardless of popularity.
		libraryID := h.resolveMusicLibraryID(r)
		q.Tracks, err = h.queryPlayerTracks(r.Context(),
			playerTrackSelect+` JOIN (
  SELECT id,
    ROW_NUMBER() OVER (PARTITION BY artist_id ORDER BY popularity DESC, id) AS rn,
    COUNT(*) OVER (PARTITION BY artist_id) AS artist_total
  FROM music_tracks WHERE library_id=?
) pop ON pop.id=t.id
WHERE pop.artist_total<=? OR (t.popularity>0 AND pop.rn<=?)
ORDER BY t.popularity DESC, t.id`, libraryID, popularIncludeAllMaxTracks, popularPerArtistLimit)
		q.Title = "Most Popular"
	case "era":
		libraryID := h.resolveMusicLibraryID(r)
		from, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("from")))
		to, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("to")))
		if from <= 0 || to <= 0 || to < from {
			return q, true, nil
		}
		q.Tracks, err = h.queryPlayerTracks(r.Context(),
			playerTrackSelect+` WHERE t.library_id=? AND al.year BETWEEN ? AND ? ORDER BY al.year, lower(al.title), t.disc_no, t.track_no`, libraryID, from, to)
		q.Title = fmt.Sprintf("%d–%d", from, to)
	default: // single album
		albumID, perr := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("album")), 10, 64)
		if perr != nil || albumID <= 0 {
			return q, true, nil
		}
		if qerr := h.db.QueryRowContext(r.Context(),
			"SELECT title FROM music_albums WHERE id=?", albumID).Scan(&q.Title); qerr != nil {
			return q, true, nil
		}
		q.BackURL = fmt.Sprintf("/music/album/%d", albumID)
		q.Tracks, err = h.queryPlayerTracks(r.Context(),
			playerTrackSelect+` WHERE t.album_id=? ORDER BY t.disc_no, t.track_no, lower(t.title)`, albumID)
	}
	return q, false, err
}

// musicPlayer renders the now-playing view — a static shell that player.js binds
// to the persistent (Turbo-permanent) audio element. It carries no server-built
// queue: playback is started by data-play controls that POST nothing and fetch
// /music/queue, so the audio survives navigation. When loaded with queue params
// directly (an old deep link, or the data-play href fallback when JS is off),
// AutoloadQuery is handed to player.js to load that queue on arrival.
func (h *Handler) musicPlayer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	autoload := ""
	if q := r.URL.RawQuery; q != "" && (strings.Contains(q, "album=") || strings.Contains(q, "source=")) {
		autoload = q
	}
	h.render(w, "player.html", map[string]any{
		"Title":         "Player",
		"AutoloadQuery": autoload,
		"LyricsEnabled": h.effectiveLyricsEnabled(r.Context()),
	})
}

// musicQueue returns the ordered player queue as JSON for player.js, built from
// the same ?source= params as the now-playing view via buildPlayerQueue.
func (h *Handler) musicQueue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	q, notFound, err := h.buildPlayerQueue(r)
	if notFound {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		httpError(w, 500, "internal server error", "load player queue failed", "handler", "musicQueue", "err", err)
		return
	}
	type queueTrackJSON struct {
		ID      int64  `json:"id"`
		AlbumID int64  `json:"albumId"`
		Album   string `json:"album"`
		Title   string `json:"title"`
		Artist  string `json:"artist"`
	}
	out := struct {
		Title   string           `json:"title"`
		BackURL string           `json:"backUrl"`
		Tracks  []queueTrackJSON `json:"tracks"`
	}{Title: q.Title, BackURL: q.BackURL, Tracks: make([]queueTrackJSON, 0, len(q.Tracks))}
	for _, t := range q.Tracks {
		out.Tracks = append(out.Tracks, queueTrackJSON{ID: t.ID, AlbumID: t.AlbumID, Album: t.AlbumTitle, Title: t.Title, Artist: t.Artist})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// queryPlayerTracks runs a playerTrackSelect-shaped query and scans the rows
// into the player's QueueTracks.
func (h *Handler) queryPlayerTracks(ctx context.Context, query string, args ...any) ([]trackRow, error) {
	rows, err := h.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tracks := make([]trackRow, 0, 32)
	for rows.Next() {
		var t trackRow
		if err := rows.Scan(&t.ID, &t.AlbumID, &t.AlbumTitle, &t.AlbumYear, &t.Title, &t.Artist, &t.ArtistID, &t.TrackNo, &t.DiscNo, &t.MIME); err != nil {
			return nil, err
		}
		t.ArtistDisplay = t.Artist
		tracks = append(tracks, t)
	}
	return tracks, rows.Err()
}

// --- Stream Track ---

func (h *Handler) streamTrack(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	trackID, err := pathID(r, "/stream/track/")
	if err != nil {
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
		httpError(w, 500, "internal server error", "open track file failed", "handler", "streamTrack", "err", err)
		return
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		httpError(w, 500, "internal server error", "stat track file failed", "handler", "streamTrack", "err", err)
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
	if r := []rune(in.Source); len(r) > 32 {
		in.Source = strings.TrimSpace(string(r[:32]))
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
		httpError(w, 500, "internal server error", "db query failed", "handler", "musicPlayEvent", "err", err)
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
		httpError(w, 500, "internal server error", "db insert failed", "handler", "musicPlayEvent", "err", err)
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
		httpError(w, 500, "internal server error", "find duplicates failed", "handler", "musicDuplicates", "err", err)
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
		httpError(w, 400, "bad request", "parse form failed", "handler", "musicDuplicatesMerge", "err", err)
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
		httpError(w, 500, "internal server error", "merge albums failed", "handler", "musicDuplicatesMerge", "err", err)
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
	albumID, err := pathID(r, "/art/album/")
	if err != nil {
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
	// Manually-uploaded art is user-controlled bytes; prevent any content-type
	// sniffing (e.g. a PNG/JS polyglot) from being interpreted as anything but
	// the declared image type.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = io.Copy(w, f)
}

func (h *Handler) artistArt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	artistID, err := pathID(r, "/art/artist/")
	if err != nil {
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
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = io.Copy(w, f)
}

func (h *Handler) serveStaticImageFallback(w http.ResponseWriter, r *http.Request, fileName, contentType string) {
	f, err := h.staticFS.Open(fileName)
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
