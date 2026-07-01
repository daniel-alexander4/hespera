package web

import (
	"fmt"
	"math/rand"
	"net/http"
	"strconv"
	"strings"

	"hespera/internal/billboard"
)

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
