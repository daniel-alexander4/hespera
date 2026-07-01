package web

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"strconv"
	"strings"

	"hespera/internal/billboard"
)

// billboardEnabled reports whether the user has opted into the chart-data
// feature. It's off by default; the Settings toggle that enables it carries a
// licensing notice (the chart data is a third party's intellectual property and
// is fetched at runtime, never shipped).
func (h *Handler) billboardEnabled(ctx context.Context) bool {
	var v string
	_ = h.db.QueryRowContext(ctx, "SELECT value FROM app_settings WHERE key='billboard_enabled'").Scan(&v)
	return strings.TrimSpace(v) == "1"
}

// enqueueBillboardFetch kicks the one-time runtime fetch of the weekly chart
// index into DataDir (keyless, deduped). No-op if a fetch is already in flight.
func (h *Handler) enqueueBillboardFetch(ctx context.Context) {
	if _, busy := h.metaFetch.LoadOrStore("billboard:fetch", true); busy {
		return
	}
	_, err := h.jobs.Enqueue("billboard_fetch", 0, "user", func(jctx context.Context, jobID, libID int64) error {
		defer h.metaFetch.Delete("billboard:fetch")
		return billboard.BuildIndex(h.cfg.DataDir, "")
	})
	if err != nil {
		h.metaFetch.Delete("billboard:fetch")
		slog.Warn("enqueue billboard fetch", "err", err)
	}
}

// Top 100 (Billboard Hot 100 charts) is a YouTube-sourced playlist surface: each
// entry is a yt-kind track resolved + played via YouTube — a popout window that
// iframes the video by default, or the in-app hidden-audio engine in Test Audio
// mode. The data is the runtime-fetched weekly chart grid (the same opt-in,
// licensing-noticed billboard feed as the retired week-by-week page); the
// per-year list is "every song that charted that year" derived from it.
const (
	// top100ShuffleAllPerYear caps how many of each year's top songs feed the
	// all-years shuffle; top100ShuffleAllCap bounds the whole pooled queue so its
	// lazy YouTube resolves stay reasonable.
	top100ShuffleAllPerYear = 40
	top100ShuffleAllCap     = 300
)

// top100QueueTracks builds the Top-100 player queue from the query params:
// ?y=YYYY → that year's chart in peak order (#1 first; &dir=rev reverses to
// bottom-up), no ?y= → an all-years shuffle. Every track is yt-kind. Returns an
// empty queue (not an error) when the chart data isn't present.
func (h *Handler) top100QueueTracks(r *http.Request) ([]trackRow, string, error) {
	if year, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("y"))); year > 0 {
		songs := billboard.YearChart(h.cfg.DataDir, year)
		if r.URL.Query().Get("dir") == "rev" {
			for i, j := 0, len(songs)-1; i < j; i, j = i+1, j-1 {
				songs[i], songs[j] = songs[j], songs[i]
			}
		}
		tracks := make([]trackRow, 0, len(songs))
		for _, s := range songs {
			tracks = append(tracks, trackRow{Kind: "yt", Title: s.Title, Artist: s.Artist})
		}
		return tracks, fmt.Sprintf("Top 100 — %d", year), nil
	}

	// No year → Shuffle All across every covered year.
	minY, maxY, ok := billboard.Years(h.cfg.DataDir)
	if !ok {
		return nil, "Top 100", nil
	}
	pool := make([]trackRow, 0, 4096)
	for y := minY; y <= maxY; y++ {
		songs := billboard.YearChart(h.cfg.DataDir, y)
		if len(songs) > top100ShuffleAllPerYear {
			songs = songs[:top100ShuffleAllPerYear]
		}
		for _, s := range songs {
			pool = append(pool, trackRow{Kind: "yt", Title: s.Title, Artist: s.Artist})
		}
	}
	rand.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
	if len(pool) > top100ShuffleAllCap {
		pool = pool[:top100ShuffleAllCap]
	}
	return pool, "Top 100 — Shuffle All", nil
}

// musicPlaylists renders the Playlists hub: a "My Music" card (Shuffle All /
// Shuffle Most Popular, played in-app from the local library) and a "Top 100"
// card (Billboard Hot 100 charts, YouTube-sourced — a popout iframe by default,
// or the in-app hidden engine in Test Audio mode). It replaces the retired
// week-by-week "Rediscover a Year" page. The Top-100 card gates on the
// runtime-fetched chart data (off / building / ready) and on a YouTube key.
func (h *Handler) musicPlaylists(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	ctx := r.Context()
	libraryID := h.resolveMusicLibraryID(r)

	data := map[string]any{
		"Title":          "Playlists",
		"LibraryID":      libraryID,
		"HasYouTubeKey":  h.effectiveYouTubeKey(ctx) != "",
		"TestAudio":      h.effectiveYouTubeInApp(ctx),
		"BillboardOn":    h.billboardEnabled(ctx),
		"BillboardReady": false,
	}

	if h.billboardEnabled(ctx) {
		if minY, maxY, ok := billboard.Years(h.cfg.DataDir); ok {
			years := make([]int, 0, maxY-minY+1)
			for y := maxY; y >= minY; y-- { // newest first in the picker
				years = append(years, y)
			}
			data["BillboardReady"] = true
			data["Years"] = years
			data["DefaultYear"] = maxY
		} else {
			// Opted in but the data hasn't landed yet — kick the one-time fetch.
			h.enqueueBillboardFetch(ctx)
			data["BillboardBuilding"] = true
		}
	}

	h.render(w, "music_playlists.html", data)
}
