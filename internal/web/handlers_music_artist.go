package web

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"hespera/internal/match"
)

// mbidPattern validates a MusicBrainz UUID. The chosen MBID is posted back from
// our own rendered candidate list, but POST input is never trusted: a malformed
// value is rejected before it ever reaches the DB or a MusicBrainz lookup.
var mbidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// musicArtistDisambiguate is the manual artist-disambiguation control. The
// automatic enrichment resolves an artist by name and blindly takes the top
// MusicBrainz hit, which picks the wrong artist when several share a name
// (e.g. the 1897 country "Jimmie Rodgers" over the 1933 pop singer). This lets
// a user see the candidates (with disambiguation, type, country, life span) and
// pick the right one. GET renders the chooser; POST applies the choice.
func (h *Handler) musicArtistDisambiguate(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.musicArtistDisambiguateGET(w, r)
	case http.MethodPost:
		h.musicArtistDisambiguatePOST(w, r)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (h *Handler) musicArtistDisambiguateGET(w http.ResponseWriter, r *http.Request) {
	artistID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("id")), 10, 64)
	if err != nil || artistID <= 0 {
		http.NotFound(w, r)
		return
	}

	var name, currentMBID string
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT name, COALESCE(musicbrainz_id, '') FROM music_artists WHERE id=?", artistID,
	).Scan(&name, &currentMBID); err != nil {
		http.NotFound(w, r)
		return
	}

	matcher := match.New(h.db, h.cfg.DataDir, h.effectiveFanartKey(r.Context()), h.effectiveAudioDBKey(r.Context()), h.effectiveLastfmKey(r.Context()))
	// Bound the MusicBrainz round-trip so this interactive GET can't hang the page
	// on a slow/unreachable provider (it already degrades to a retry on error).
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	candidates, err := matcher.ResolveArtistCandidates(ctx, name)
	if err != nil {
		// MusicBrainz is unreachable (DNS/timeout/outage). Degrade to a readable
		// page with a retry rather than a raw 502 Bad Gateway in the browser.
		slog.Warn("musicbrainz artist search failed",
			"handler", "musicArtistDisambiguate", "artist_id", artistID, "err", err)
		h.render(w, "music_artist_disambiguate.html", map[string]any{
			"Breadcrumb": []crumb{bcHome, bcMusic, bcArtist(artistID, name)},
			"Title":      "Fix artist — " + name,
			"ArtistID":   artistID,
			"ArtistName": name,
			"Error":      true,
		})
		return
	}

	type candView struct {
		match.ArtistCandidate
		IsCurrent bool
	}
	views := make([]candView, 0, len(candidates))
	for _, c := range candidates {
		views = append(views, candView{
			ArtistCandidate: c,
			IsCurrent:       currentMBID != "" && c.MBID == currentMBID,
		})
	}

	h.render(w, "music_artist_disambiguate.html", map[string]any{
		"Breadcrumb":  []crumb{bcHome, bcMusic, bcArtist(artistID, name)},
		"Title":       "Fix artist — " + name,
		"ArtistID":    artistID,
		"ArtistName":  name,
		"CurrentMBID": currentMBID,
		"Candidates":  views,
	})
}

func (h *Handler) musicArtistDisambiguatePOST(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		httpError(w, 400, "bad request", "parse form failed", "handler", "musicArtistDisambiguate", "err", err)
		return
	}
	artistID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("artist_id")), 10, 64)
	if err != nil || artistID <= 0 {
		http.Error(w, "invalid artist_id", http.StatusBadRequest)
		return
	}
	mbid := strings.TrimSpace(r.FormValue("mbid"))
	if !mbidPattern.MatchString(mbid) {
		http.Error(w, "invalid mbid", http.StatusBadRequest)
		return
	}

	// Confirm the artist exists before mutating.
	var exists int
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT 1 FROM music_artists WHERE id=?", artistID).Scan(&exists); err != nil {
		http.NotFound(w, r)
		return
	}

	// Set the chosen identity and clear the stale enrichment so it re-fetches for
	// the new MBID. This touches only music_artists — per-album
	// artist_musicbrainz_id stays untouched (it was resolved per release-group and
	// is individually correct). enrich_checked_at is cleared too, so if the
	// synchronous re-enrich below fails (network), the next automatic Match
	// retries immediately rather than waiting out the TTL.
	if _, err := h.db.ExecContext(r.Context(), `
		UPDATE music_artists SET
			musicbrainz_id=?,
			bio='',
			bio_source_name='',
			bio_source_url='',
			art_path='',
			enrich_checked_at=''
		WHERE id=?
	`, mbid, artistID); err != nil {
		httpError(w, 500, "internal server error", "db update failed", "handler", "musicArtistDisambiguate", "err", err)
		return
	}

	// Re-enrich synchronously for the chosen MBID so the corrected bio/image show
	// immediately. Non-fatal on error — the identity is already corrected, and the
	// next Match run's enrichment fills the gaps (it re-enriches any artist whose
	// bio/art is empty without re-resolving the now-set MBID).
	matcher := match.New(h.db, h.cfg.DataDir, h.effectiveFanartKey(r.Context()), h.effectiveAudioDBKey(r.Context()), h.effectiveLastfmKey(r.Context()))
	if meta, err := matcher.ReEnrichArtist(r.Context(), mbid); err != nil {
		slog.Warn("re-enrich artist failed", "artist_id", artistID, "mbid", mbid, "err", err)
	} else {
		if meta.Bio != "" {
			_, _ = h.db.ExecContext(r.Context(),
				"UPDATE music_artists SET bio=?, bio_source_name=?, bio_source_url=? WHERE id=?",
				meta.Bio, meta.BioSourceName, meta.BioSourceURL, artistID)
		}
		if meta.ImagePath != "" {
			_, _ = h.db.ExecContext(r.Context(),
				"UPDATE music_artists SET art_path=? WHERE id=?", meta.ImagePath, artistID)
		}
	}

	http.Redirect(w, r, fmt.Sprintf("/music/artist/%d", artistID), http.StatusSeeOther)
}
