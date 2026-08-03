package web

// Podcasts — Hespera's first outward-facing vertical.
//
// Everything else here serves bytes the user owns: a scanner walks MEDIA_ROOT,
// rows land in SQLite, a handler streams a local file. A podcast owns nothing
// locally. There is no scanner, no library_id, no abs_path, and
// internal/playback is bypassed entirely — its decision layer needs a probed
// local file to reason about, and an episode is a URL.
//
// The single new capability is fetching a host the user chose, and it is
// confined to internal/podcast.Client. Nothing in this file performs an
// outbound request itself.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"hespera/internal/podcast"
)

// podcastEpisodeCap bounds how many episodes one feed contributes. Long-running
// daily shows carry thousands of items; storing every one makes a subscribe
// take minutes and a show page unrenderable, and nobody scrolls to 2013.
// Newest-first, so the cap drops the tail.
const podcastEpisodeCap = 300

// podcastFetcher is the seam between the handlers and the one place that talks
// to a host the user chose. A field on Handler rather than a direct
// construction, for the same reason tmdbValidate and powerOff are fields: a
// test must never make a real outbound request, and here that matters more than
// usual — the whole point of this vertical is fetching arbitrary hosts.
type podcastFetcher interface {
	FetchFeed(ctx context.Context, rawURL string) (*podcast.Feed, error)
	Get(ctx context.Context, rawURL, rangeHdr string) (*http.Response, error)
	SearchDirectory(ctx context.Context, term string) ([]podcast.DirectoryResult, error)
}

// podcastClient returns the fetcher, building the real guarded one on first use.
// The User-Agent names the app and version the way the MusicBrainz client does —
// podcast hosts block unlabelled clients, and an honest name is what lets one
// complain to the right place.
func (h *Handler) podcastClient() podcastFetcher {
	if h.podcasts != nil {
		return h.podcasts
	}
	v := h.version
	if v == "" {
		v = "dev"
	}
	return podcast.NewClient("Hespera/" + v)
}

// --- rows ------------------------------------------------------------------

type podcastRow struct {
	ID       int64
	Title    string
	Author   string
	ImageURL string
	Episodes int
	LastErr  string
}

type episodeRow struct {
	ID           int64
	Title        string
	Description  string
	Duration     int
	DurationText string
	Published    string
	ImageURL     string
	ProgressPct  int
	Completed    bool
}

// --- list ------------------------------------------------------------------

func (h *Handler) podcastsHome(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	rows, err := h.loadPodcasts(r.Context())
	if err != nil {
		httpError(w, 500, "internal server error", "db query failed", "handler", "podcastsHome", "err", err)
		return
	}
	h.render(w, "podcasts_home.html", map[string]any{
		"Title":    "Podcasts",
		"Podcasts": rows,
		"Error":    strings.TrimSpace(r.URL.Query().Get("error")),
	})
}

func (h *Handler) loadPodcasts(ctx context.Context) ([]podcastRow, error) {
	rows, err := h.db.QueryContext(ctx, `
SELECT p.id, p.title, p.author, p.image_url, p.last_error,
       (SELECT COUNT(*) FROM podcast_episodes e WHERE e.podcast_id = p.id)
FROM podcasts p
ORDER BY lower(p.title)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []podcastRow
	for rows.Next() {
		var p podcastRow
		if err := rows.Scan(&p.ID, &p.Title, &p.Author, &p.ImageURL, &p.LastErr, &p.Episodes); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// --- explore ---------------------------------------------------------------

// podcastsHome and podcastExplore share a page; explore just fills the results.
// A GET with a ?q= rather than a POST, so a search is linkable and survives a
// reload — it changes nothing on the server.
func (h *Handler) podcastExplore(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	term := strings.TrimSpace(r.URL.Query().Get("q"))

	subs, err := h.loadPodcasts(r.Context())
	if err != nil {
		httpError(w, 500, "internal server error", "db query failed", "handler", "podcastExplore", "err", err)
		return
	}
	// Which results are already subscribed, so the page can say so instead of
	// offering a Subscribe that silently no-ops (subscribing is idempotent, but
	// a button that appears to do nothing reads as broken).
	have := map[string]bool{}
	for _, u := range h.subscribedFeedURLs(r.Context()) {
		have[u] = true
	}

	data := map[string]any{
		"Title":    "Find podcasts",
		"Podcasts": subs,
		"Query":    term,
		"Explore":  true,
	}
	if term != "" {
		res, err := h.podcastClient().SearchDirectory(r.Context(), term)
		if err != nil {
			data["Error"] = err.Error()
		} else {
			rows := make([]directoryRow, 0, len(res))
			for _, x := range res {
				rows = append(rows, directoryRow{DirectoryResult: x, Subscribed: have[x.FeedURL]})
			}
			data["Results"] = rows
			data["NoResults"] = len(rows) == 0
		}
	}
	h.render(w, "podcasts_home.html", data)
}

// directoryRow is a search result plus whether it is already followed.
type directoryRow struct {
	podcast.DirectoryResult
	Subscribed bool
}

func (h *Handler) subscribedFeedURLs(ctx context.Context) []string {
	rows, err := h.db.QueryContext(ctx, "SELECT feed_url FROM podcasts")
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err == nil {
			out = append(out, u)
		}
	}
	return out
}

// --- subscribe -------------------------------------------------------------

// podcastSubscribe takes a feed URL, fetches it once synchronously, and stores
// what came back.
//
// Synchronous on purpose, unlike every other remote fetch in Hespera (which are
// background jobs): the user just typed a URL and needs to know whether it is a
// podcast. A background job would redirect to a page showing nothing, with the
// failure buried in a log. The fetch is bounded by the client's own timeout.
func (h *Handler) podcastSubscribe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	raw := strings.TrimSpace(r.FormValue("feed_url"))
	if raw == "" {
		http.Redirect(w, r, "/podcasts?error="+url.QueryEscape("enter a feed URL"), http.StatusSeeOther)
		return
	}
	clean, err := podcast.ValidateURL(raw)
	if err != nil {
		http.Redirect(w, r, "/podcasts?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}

	id, err := h.subscribeFeed(r.Context(), clean)
	if err != nil {
		// The message is shown to whoever typed the URL; podcast.Client's errors
		// are already written for that audience (blocked address, upstream
		// status, not-a-feed) rather than being transport noise.
		http.Redirect(w, r, "/podcasts?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/podcasts/show?id=%d", id), http.StatusSeeOther)
}

func (h *Handler) subscribeFeed(ctx context.Context, feedURL string) (int64, error) {
	feed, err := h.podcastClient().FetchFeed(ctx, feedURL)
	if err != nil {
		return 0, err
	}
	if len(feed.Episodes) == 0 {
		return 0, errors.New("that feed has no playable episodes")
	}

	// Idempotent: re-subscribing to a feed already held refreshes it in place
	// rather than failing on the UNIQUE or creating a second copy.
	if _, err := h.db.ExecContext(ctx, `
INSERT INTO podcasts (feed_url, title, description, author, link, image_url, last_fetched_at, last_error)
VALUES (?, ?, ?, ?, ?, ?, ?, '')
ON CONFLICT(feed_url) DO UPDATE SET
  title=excluded.title, description=excluded.description, author=excluded.author,
  link=excluded.link, image_url=excluded.image_url,
  last_fetched_at=excluded.last_fetched_at, last_error=''`,
		feedURL, feed.Title, feed.Description, feed.Author, feed.Link, feed.ImageURL, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return 0, err
	}
	// Deliberately NOT LastInsertId. When the upsert takes its DO UPDATE branch
	// nothing is inserted, and LastInsertId then reports whatever this
	// connection last inserted — which after a previous subscribe is a
	// podcast_episodes rowid. Using it silently wrote episodes under a
	// nonexistent podcast id and tripped the foreign key. The unique key is the
	// only honest way to identify the row.
	var id int64
	if err := h.db.QueryRowContext(ctx, "SELECT id FROM podcasts WHERE feed_url=?", feedURL).Scan(&id); err != nil {
		return 0, err
	}
	if err := h.storeEpisodes(ctx, id, feed); err != nil {
		return 0, err
	}
	return id, nil
}

// storeEpisodes upserts a feed's items. Existing rows keep their id — which is
// what preserves playback progress across a refresh, since progress hangs off
// episode_id.
func (h *Handler) storeEpisodes(ctx context.Context, podcastID int64, feed *podcast.Feed) error {
	eps := feed.Episodes
	if len(eps) > podcastEpisodeCap {
		eps = eps[:podcastEpisodeCap]
	}
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, e := range eps {
		var published string
		if !e.Published.IsZero() {
			published = e.Published.UTC().Format(time.RFC3339)
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO podcast_episodes
  (podcast_id, guid, title, description, audio_url, audio_type, audio_bytes, duration_seconds, published_at, episode_no, season_no, image_url)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(podcast_id, guid) DO UPDATE SET
  title=excluded.title, description=excluded.description,
  audio_url=excluded.audio_url, audio_type=excluded.audio_type,
  audio_bytes=excluded.audio_bytes, duration_seconds=excluded.duration_seconds,
  published_at=excluded.published_at, episode_no=excluded.episode_no,
  season_no=excluded.season_no, image_url=excluded.image_url`,
			podcastID, e.GUID, e.Title, e.Description, e.AudioURL, e.AudioType,
			e.AudioBytes, e.Duration, published, e.EpisodeNo, e.SeasonNo, e.ImageURL); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// --- show ------------------------------------------------------------------

func (h *Handler) podcastShow(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("id")), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}

	var p podcastRow
	var desc, link, feedURL string
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT id, title, author, image_url, description, link, feed_url, last_error FROM podcasts WHERE id=?", id,
	).Scan(&p.ID, &p.Title, &p.Author, &p.ImageURL, &desc, &link, &feedURL, &p.LastErr); err != nil {
		http.NotFound(w, r)
		return
	}

	eps, err := h.loadEpisodes(r.Context(), id)
	if err != nil {
		httpError(w, 500, "internal server error", "db query failed", "handler", "podcastShow", "err", err)
		return
	}
	h.render(w, "podcast_show.html", map[string]any{
		"Title":       p.Title,
		"Podcast":     p,
		"Description": desc,
		"Link":        link,
		"FeedURL":     feedURL,
		"Episodes":    eps,
	})
}

func (h *Handler) loadEpisodes(ctx context.Context, podcastID int64) ([]episodeRow, error) {
	// Undated episodes sort last rather than first: published_at is empty for
	// them, and empty sorts below any RFC3339 stamp under DESC.
	rows, err := h.db.QueryContext(ctx, `
SELECT e.id, e.title, e.description, e.duration_seconds, e.published_at, e.image_url,
       COALESCE(pr.position_seconds,0), COALESCE(pr.duration_seconds,0), COALESCE(pr.completed,0)
FROM podcast_episodes e
LEFT JOIN podcast_playback_progress pr ON pr.episode_id = e.id
WHERE e.podcast_id = ?
ORDER BY e.published_at DESC, e.id DESC`, podcastID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []episodeRow
	for rows.Next() {
		var e episodeRow
		var pos, dur float64
		var completed int
		var published string
		if err := rows.Scan(&e.ID, &e.Title, &e.Description, &e.Duration, &published, &e.ImageURL, &pos, &dur, &completed); err != nil {
			return nil, err
		}
		e.Published = humanDate(published)
		// Precomputed rather than a template func: the FuncMap is deliberately
		// tiny (staticv, humanBytes, mult), and audiobooks already formats its
		// durations on the row for the same reason.
		e.DurationText = audiobookDurationText(float64(e.Duration))
		e.Completed = completed == 1
		if dur > 0 && pos > 0 {
			if pct := int(pos / dur * 100); pct > 0 {
				e.ProgressPct = min(pct, 100)
			}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// humanDate renders a stored RFC3339 stamp for display, degrading to empty
// rather than showing a parse error or a zero date.
func humanDate(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return ""
	}
	return t.Format("2 Jan 2006")
}

// --- refresh / unsubscribe -------------------------------------------------

// podcastRefresh re-reads one feed. A background job, unlike subscribe: nobody
// is waiting on the answer, and a slow feed should not hold a request open.
func (h *Handler) podcastRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("id")), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	var feedURL string
	if err := h.db.QueryRowContext(r.Context(), "SELECT feed_url FROM podcasts WHERE id=?", id).Scan(&feedURL); err != nil {
		http.NotFound(w, r)
		return
	}
	h.enqueuePodcastRefresh(id, feedURL)
	http.Redirect(w, r, fmt.Sprintf("/podcasts/show?id=%d", id), http.StatusSeeOther)
}

// enqueuePodcastRefresh runs one feed refresh in the background job queue, so
// it is serialized against the scanners and inherits their nice/ionice.
func (h *Handler) enqueuePodcastRefresh(id int64, feedURL string) {
	key := fmt.Sprintf("podcast-refresh:%d", id)
	if _, busy := h.metaFetch.LoadOrStore(key, true); busy {
		return
	}
	go func() {
		defer h.metaFetch.Delete(key)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		feed, err := h.podcastClient().FetchFeed(ctx, feedURL)
		if err != nil {
			// Recorded on the row rather than only logged: a feed that has gone
			// away should say so on its own page, not require reading a journal.
			_, _ = h.db.ExecContext(ctx, "UPDATE podcasts SET last_error=?, last_fetched_at=? WHERE id=?",
				err.Error(), time.Now().UTC().Format(time.RFC3339), id)
			slog.Warn("podcast refresh", "id", id, "err", err)
			return
		}
		if err := h.storeEpisodes(ctx, id, feed); err != nil {
			slog.Warn("podcast store episodes", "id", id, "err", err)
			return
		}
		_, _ = h.db.ExecContext(ctx, `
UPDATE podcasts SET title=?, description=?, author=?, link=?, image_url=?, last_fetched_at=?, last_error='' WHERE id=?`,
			feed.Title, feed.Description, feed.Author, feed.Link, feed.ImageURL, time.Now().UTC().Format(time.RFC3339), id)
	}()
}

// podcastUnsubscribe drops a show. The episode and progress rows go with it via
// ON DELETE CASCADE — there is nothing on disk to reap, which is the one
// simplification streaming-only buys.
func (h *Handler) podcastUnsubscribe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("id")), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if _, err := h.db.ExecContext(r.Context(), "DELETE FROM podcasts WHERE id=?", id); err != nil {
		httpError(w, 500, "internal server error", "delete failed", "handler", "podcastUnsubscribe", "err", err)
		return
	}
	http.Redirect(w, r, "/podcasts", http.StatusSeeOther)
}

// hasPodcasts reports whether any subscription exists, for the home card.
func (h *Handler) hasPodcasts(ctx context.Context) bool {
	var n int
	if err := h.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM podcasts").Scan(&n); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false
	}
	return n > 0
}
