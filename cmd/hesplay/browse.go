// browse.go — the remote's A-Z library browser.
//
// Hespera has no browse API: /music, /music/albums, /music/artist/{id} and
// /music/album/{id} are all HTML, and the only machine-readable music surfaces
// are /search, /music/playlists and /music/queue. /search cannot stand in for an
// A-Z index either — it needs two characters minimum, caps at 50 rows a section,
// and its rows carry no ids at all.
//
// So the browsing happens HERE, in the player, rather than in the server. That
// is not just expedience: it means the feature works against whatever Hespera is
// already running (nothing newer than the /music/queue that shipped long ago),
// instead of being blocked behind upgrading it. The one bulk read costs ~2 MB on
// a 14k-track library, and it happens between this box and the server on the
// LAN — the phone only ever receives the filtered slice it asked for.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// browseIndexTTL is how long the artist index is trusted. A newly scanned
// artist shows up on the next refresh rather than instantly, which is the right
// trade for not re-reading the whole catalog on every screen open.
const browseIndexTTL = 10 * time.Minute

// browseFetchTimeout covers the one bulk read. It has its own budget because the
// client's ordinary 15s is tuned for interactive calls, and this response grows
// with the library — 2 MB at 14k tracks, and the server does not compress it.
const browseFetchTimeout = 90 * time.Second

type artistRef struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Letter string `json:"letter"`
	sort   string // browseKey(Name); unexported, never serialized
}

type letterCount struct {
	Letter string `json:"letter"`
	Count  int    `json:"count"`
}

// browseIndex is the cached artist list. Albums and tracks are NOT cached —
// they come from a per-artist / per-album queue read, which is small, already
// correctly ordered by the server, and always current.
type browseIndex struct {
	mu      sync.Mutex
	http    *http.Client
	artists []artistRef
	letters []letterCount
	base    string    // the server the cache was built from
	built   time.Time // zero = never
}

func newBrowseIndex() *browseIndex {
	return &browseIndex{http: &http.Client{Timeout: browseFetchTimeout}}
}

// browseKey is the sort/bucket key: lowercased, with a leading article removed.
// Without the strip, "The …" swamps one letter — on a real 976-artist library
// 100 artists (10%) begin "The ", putting 137 of them behind T while B holds 83.
// Dropping the article gives S=126, B=89, A=83 and T falls out of the top five.
// It does mean the phone's order differs from the web UI's raw lower(name) sort.
func browseKey(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	for _, art := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(s, art) {
			return strings.TrimSpace(s[len(art):])
		}
	}
	return s
}

// browseLetter buckets an artist. Everything that does not start with a letter
// — numbers, symbols, non-Latin scripts — shares "#", so no artist is
// unreachable from the A-Z strip.
func browseLetter(name string) string {
	k := browseKey(name)
	if k == "" {
		return "#"
	}
	r := []rune(strings.ToUpper(k))[0]
	if r >= 'A' && r <= 'Z' {
		return string(r)
	}
	return "#"
}

// ensure (re)builds the index when it is stale, missing, or was built against a
// different server. force is the remote's explicit refresh.
func (bi *browseIndex) ensure(base string, force bool) error {
	bi.mu.Lock()
	fresh := !bi.built.IsZero() && bi.base == base && time.Since(bi.built) < browseIndexTTL
	bi.mu.Unlock()
	if fresh && !force {
		return nil
	}

	// The whole catalog is the only enumeration Hespera offers. Tracks are
	// decoded and dropped; only the distinct artists are kept (~40 KB of the
	// ~2 MB read).
	u := base + "/music/queue?" + url.Values{"source": {"all"}}.Encode()
	resp, err := bi.http.Get(u)
	if err != nil {
		return fmt.Errorf("reading the library: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("reading the library: server said %s", resp.Status)
	}
	var q queue
	if err := json.NewDecoder(resp.Body).Decode(&q); err != nil {
		return fmt.Errorf("reading the library: %w", err)
	}

	seen := make(map[int64]string, 1024)
	for _, t := range q.Tracks {
		if t.ArtistID > 0 && t.Artist != "" {
			seen[t.ArtistID] = t.Artist
		}
	}
	artists := make([]artistRef, 0, len(seen))
	counts := map[string]int{}
	for id, name := range seen {
		l := browseLetter(name)
		artists = append(artists, artistRef{ID: id, Name: name, Letter: l, sort: browseKey(name)})
		counts[l]++
	}
	sort.Slice(artists, func(i, j int) bool {
		if artists[i].sort != artists[j].sort {
			return artists[i].sort < artists[j].sort
		}
		return artists[i].ID < artists[j].ID
	})

	letters := make([]letterCount, 0, 27)
	for r := 'A'; r <= 'Z'; r++ {
		letters = append(letters, letterCount{Letter: string(r), Count: counts[string(r)]})
	}
	letters = append(letters, letterCount{Letter: "#", Count: counts["#"]})

	bi.mu.Lock()
	bi.artists, bi.letters, bi.base, bi.built = artists, letters, base, time.Now()
	bi.mu.Unlock()
	return nil
}

func (bi *browseIndex) snapshot() ([]artistRef, []letterCount) {
	bi.mu.Lock()
	defer bi.mu.Unlock()
	return bi.artists, bi.letters
}

// byLetter returns one bucket. An empty letter returns every artist, which is
// what the remote asks for when it wants the whole list.
func (bi *browseIndex) byLetter(letter string) []artistRef {
	all, _ := bi.snapshot()
	if letter == "" {
		return all
	}
	out := make([]artistRef, 0, 64)
	for _, a := range all {
		if a.Letter == letter {
			out = append(out, a)
		}
	}
	return out
}

// --- HTTP ---------------------------------------------------------------

func (ct *controller) handleLetters(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, errGETOnly)
		return
	}
	base := ct.upstream().base
	if err := ct.browse.ensure(base, r.URL.Query().Get("refresh") == "1"); err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	artists, letters := ct.browse.snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "letters": letters, "total": len(artists),
	})
}

func (ct *controller) handleArtists(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, errGETOnly)
		return
	}
	base := ct.upstream().base
	if err := ct.browse.ensure(base, false); err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	rows := ct.browse.byLetter(strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("letter"))))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "artists": rows})
}

// handleArtist lists one artist's albums, derived from that artist's own queue
// — the server already orders it by album year then title, so the order albums
// first appear in is the order to show them. Year and cover-count are not in the
// queue JSON (they live only in the HTML handler), so they are simply not shown;
// the covers themselves come from /art/album/{id}, which the remote proxies.
func (ct *controller) handleArtist(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, errGETOnly)
		return
	}
	id, err := idParam(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	q, err := ct.upstream().fetchQueue(queueParams("artist", id, false))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	type albumRef struct {
		ID    int64  `json:"id"`
		Title string `json:"title"`
		Count int    `json:"count"`
	}
	order := make([]int64, 0, 16)
	byID := map[int64]*albumRef{}
	for _, t := range q.Tracks {
		if t.AlbumID <= 0 {
			continue
		}
		if _, ok := byID[t.AlbumID]; !ok {
			byID[t.AlbumID] = &albumRef{ID: t.AlbumID, Title: t.Album}
			order = append(order, t.AlbumID)
		}
		byID[t.AlbumID].Count++
	}
	albums := make([]albumRef, 0, len(order))
	for _, id := range order {
		albums = append(albums, *byID[id])
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "id": id, "name": q.Title, "tracks": len(q.Tracks), "albums": albums,
	})
}

// handleAlbum lists an album's tracks, in the server's own disc/track order.
func (ct *controller) handleAlbum(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, errGETOnly)
		return
	}
	id, err := idParam(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	q, err := ct.upstream().fetchQueue(queueParams("album", id, false))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	type trackRef struct {
		Index  int    `json:"index"` // 1-based position within the album queue
		ID     int64  `json:"id"`
		Title  string `json:"title"`
		Artist string `json:"artist"`
	}
	tracks := make([]trackRef, 0, len(q.Tracks))
	for i, t := range q.Tracks {
		tracks = append(tracks, trackRef{Index: i + 1, ID: t.ID, Title: t.Title, Artist: t.Artist})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "id": id, "title": q.Title, "tracks": tracks,
	})
}
