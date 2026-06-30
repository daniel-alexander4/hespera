package web

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"hespera/internal/itunes"
)

// artQuery is one un-owned song needing cover art (artist + title), collected by
// loadJourney and resolved by the background iTunes backfill.
type artQuery struct {
	Artist string
	Title  string
}

// itunesArtMinInterval paces the keyless backfill so it stays gentle on the
// iTunes Search API (which throttles bursts). A full year is a few hundred
// songs, so the grid fills progressively across views; each song is cached
// after one attempt, hit or miss.
const itunesArtMinInterval = 250 * time.Millisecond

// itunesArtIndex maps a song's reconcile key (taKey) to its cached iTunes cover
// URL — including cached misses (empty string), so callers can tell "resolved
// to nothing" from "never tried" via the comma-ok and avoid re-enqueuing it.
func (h *Handler) itunesArtIndex(ctx context.Context) map[string]string {
	out := map[string]string{}
	rows, err := h.db.QueryContext(ctx, "SELECT query_key, art_url FROM itunes_art")
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var key, url string
		if rows.Scan(&key, &url) == nil {
			out[key] = url
		}
	}
	return out
}

// enqueueItunesArtFetch kicks a one-time background backfill of cover art for a
// year's un-owned, not-yet-cached songs. Keyless and deduped per year, so a
// page view fires at most one job per year while it's queued/running.
func (h *Handler) enqueueItunesArtFetch(ctx context.Context, year int, songs []artQuery) {
	if len(songs) == 0 {
		return
	}
	dedupeKey := fmt.Sprintf("itunes:art:%d", year)
	if _, busy := h.metaFetch.LoadOrStore(dedupeKey, true); busy {
		return
	}
	client := itunes.New()
	_, err := h.jobs.Enqueue("itunes_art_fetch", 0, "system", func(jctx context.Context, jobID, libID int64) error {
		defer h.metaFetch.Delete(dedupeKey)
		return h.fetchItunesArt(jctx, client, songs)
	})
	if err != nil {
		h.metaFetch.Delete(dedupeKey)
		slog.Warn("enqueue itunes art fetch", "year", year, "err", err)
	}
}

// fetchItunesArt resolves each song's cover via iTunes and caches the outcome
// (hit or genuine miss) in itunes_art so it's never re-queried. A rate-limit
// response stops the batch — the still-missing songs are picked up by the next
// page view's re-enqueue; a transient error skips one song, leaving it uncached
// to retry later.
func (h *Handler) fetchItunesArt(ctx context.Context, client *itunes.Client, songs []artQuery) error {
	for _, s := range songs {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		key := taKey(s.Title, s.Artist)
		var exists int
		if h.db.QueryRowContext(ctx, "SELECT 1 FROM itunes_art WHERE query_key=?", key).Scan(&exists) == nil {
			continue // already cached by a prior/concurrent run
		}
		url, err := client.Search(ctx, s.Artist, s.Title)
		if errors.Is(err, itunes.ErrRateLimited) {
			return nil // back off; the remainder retries on the next view
		}
		if err != nil {
			continue // transient — leave uncached so it retries later
		}
		_, _ = h.db.ExecContext(ctx,
			"INSERT INTO itunes_art (query_key, art_url) VALUES (?, ?) ON CONFLICT(query_key) DO UPDATE SET art_url=excluded.art_url",
			key, url)
		time.Sleep(itunesArtMinInterval)
	}
	return nil
}
