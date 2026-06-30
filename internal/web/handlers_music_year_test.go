package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hespera/internal/billboard"
	"hespera/internal/config"
	isodb "hespera/internal/db"
)

// enableBillboard1968 turns the chart-data feature on and builds a tiny
// fabricated 1968 fixture into the handler's DataDir (the real dataset is no
// longer shipped — it's fetched at runtime), so the year page renders. The
// fixture owns "Hey Jude" (matched by seedHeyJude) and includes un-owned songs
// so the un-owned/YouTube path renders too.
func enableBillboard1968(t *testing.T, h *Handler, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec("INSERT INTO app_settings (key,value) VALUES ('billboard_enabled','1') ON CONFLICT(key) DO UPDATE SET value='1'"); err != nil {
		t.Fatalf("enable billboard: %v", err)
	}
	csv := "chart_date,current_position,title,performer,previous_position,peak_position,weeks_on_chart\n" +
		"1968-09-28,2,Harper Valley P.T.A.,Jeannie C. Riley,1,1,8\n" +
		"1968-09-28,1,Hey Jude,The Beatles,3,1,3\n" +
		"1968-10-05,1,Hey Jude,The Beatles,1,1,4\n" +
		"1968-10-05,2,Fire,Arthur Brown,4,2,6\n"
	csvPath := filepath.Join(t.TempDir(), "fix.csv")
	if err := os.WriteFile(csvPath, []byte(csv), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if err := billboard.BuildIndex(h.cfg.DataDir, csvPath); err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
}

// seedHeyJude builds a small library owning "Hey Jude" by The Beatles — a #1
// song on the real embedded 1968 Hot 100 — so the weekly view reconciles it as
// owned and the journey queue plays it. Returns the library id.
func seedHeyJude(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO libraries (name, type, root_path) VALUES ('M','music','/m')`)
	if err != nil {
		t.Fatalf("library: %v", err)
	}
	libID, _ := res.LastInsertId()

	res, err = db.Exec(`INSERT INTO music_artists (library_id, name) VALUES (?, 'The Beatles')`, libID)
	if err != nil {
		t.Fatalf("artist: %v", err)
	}
	artistID, _ := res.LastInsertId()

	res, err = db.Exec(`INSERT INTO music_albums (library_id, artist_id, album_artist_id, title, year, is_compilation) VALUES (?,?,?,?,1968,0)`,
		libID, artistID, artistID, "Singles")
	if err != nil {
		t.Fatalf("album: %v", err)
	}
	albumID, _ := res.LastInsertId()
	if _, err := db.Exec(`INSERT INTO music_tracks (library_id, artist_id, album_id, title, track_no, disc_no, abs_path, mime_type) VALUES (?,?,?,?,1,1,'/m/heyjude.mp3','audio/mpeg')`,
		libID, artistID, albumID, "Hey Jude"); err != nil {
		t.Fatalf("track: %v", err)
	}
	return libID
}

func TestMusicYearWeekly(t *testing.T) {
	h, db := newTestHandler(t)
	seedHeyJude(t, db)
	enableBillboard1968(t, h, db)
	router := h.Router()

	req := httptest.NewRequest(http.MethodGet, "/music/year?y=1968", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /music/year = %d", rec.Code)
	}
	body := rec.Body.String()

	// Hey Jude is owned (count >= 1) — confirms title+artist reconcile against the
	// embedded chart entry. Counts are "owned/total".
	if !strings.Contains(body, `id="counts">1/`) {
		t.Fatalf("expected owned count 1, got: %s", firstLine(body))
	}
	// Weekly sections rendered from the embedded grid.
	if !strings.Contains(body, `class="wk" data-date="1968-`) {
		t.Fatalf("no weekly chart sections rendered")
	}
	// Hey Jude appears owned somewhere in the grid.
	if !strings.Contains(body, `data-owned="true">Hey Jude<`) {
		t.Fatalf("Hey Jude not marked owned in the grid")
	}
	// Play + direction-toggle controls present (hrefs built from literal text +
	// values, so & stays literal).
	if !strings.Contains(body, `href="/music/player?source=journey&y=1968"`) {
		t.Fatalf("play control missing")
	}
	if !strings.Contains(body, `id="dir" href="/music/year?y=1968&dir=top"`) {
		t.Fatalf("direction toggle missing/!top")
	}
}

func TestJourneyQueueOwnedOnlyNoKey(t *testing.T) {
	h, db := newTestHandler(t)
	seedHeyJude(t, db)
	enableBillboard1968(t, h, db)
	router := h.Router()

	// No YouTube key configured → the journey queue is owned-only.
	req := httptest.NewRequest(http.MethodGet, "/music/queue?source=journey&y=1968", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /music/queue?source=journey = %d", rec.Code)
	}
	var out struct {
		Title  string `json:"title"`
		Tracks []struct {
			Kind  string `json:"kind"`
			Title string `json:"title"`
		} `json:"tracks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode queue: %v (%s)", err, rec.Body.String())
	}
	if out.Title != "Rediscover 1968" {
		t.Fatalf("queue title = %q", out.Title)
	}
	// Exactly the one owned debut song, played locally (no yt entries without a key).
	if len(out.Tracks) != 1 {
		t.Fatalf("queue has %d tracks, want 1 owned-only: %+v", len(out.Tracks), out.Tracks)
	}
	if out.Tracks[0].Title != "Hey Jude" || out.Tracks[0].Kind != "" {
		t.Fatalf("unexpected queue track: %+v", out.Tracks[0])
	}
}

// TestMusicYearRealTemplate renders the page through the REAL web/templates so a
// field/method typo in music_year.html fails here.
func TestMusicYearRealTemplate(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	root := filepath.Join(wd, "..", "..") // internal/web → repo root (has web/templates)
	withChdir(t, root)

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.sqlite")
	db, err := isodb.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := isodb.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	h, err := New(Deps{Cfg: config.Config{DataDir: dir, MediaRoot: dir}, DB: db})
	if err != nil {
		t.Fatalf("New with real templates: %v", err)
	}
	seedHeyJude(t, db)
	enableBillboard1968(t, h, db)

	req := httptest.NewRequest(http.MethodGet, "/music/year?y=1968", nil)
	rec := httptest.NewRecorder()
	h.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /music/year = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		"Rediscover 1968",
		"Play 1968",
		`class="chart-week"`,
		`class="chart-row`,
		"Hey Jude",
		`class="js-play js-yt`, // an un-owned debut song → YouTube control
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("real template missing %q", want)
		}
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// TestLoadJourneyArtCascade covers the un-owned cover-art path: an uncached song
// is queued for the iTunes backfill, a cached hit shows its cover, and a cached
// miss stays a placeholder without being re-queried.
func TestLoadJourneyArtCascade(t *testing.T) {
	h, db := newTestHandler(t)
	libID := seedHeyJude(t, db)
	enableBillboard1968(t, h, db)
	ctx := context.Background()

	// Before any cache: the two un-owned songs are queued for backfill; the owned
	// one (Hey Jude) is not.
	first := h.loadJourney(ctx, libID, 1968, false)
	if got := needArtTitles(first.needsArt); !sameSet(got, []string{"Fire", "Harper Valley P.T.A."}) {
		t.Fatalf("needsArt before cache = %v, want the two un-owned songs", got)
	}

	// Cache a hit for Fire and a miss for Harper.
	if _, err := db.Exec("INSERT INTO itunes_art (query_key, art_url) VALUES (?,?),(?,?)",
		taKey("Fire", "Arthur Brown"), "https://img/600x600bb.jpg",
		taKey("Harper Valley P.T.A.", "Jeannie C. Riley"), ""); err != nil {
		t.Fatalf("seed itunes_art: %v", err)
	}

	// After caching both: nothing is re-queued.
	second := h.loadJourney(ctx, libID, 1968, false)
	if len(second.needsArt) != 0 {
		t.Fatalf("needsArt after caching both = %v, want empty (no re-query)", needArtTitles(second.needsArt))
	}
	var fireArt, harperArt string
	for _, wk := range second.Weeks {
		for _, c := range wk.Cards {
			switch c.Title {
			case "Fire":
				fireArt = c.ArtURL
			case "Harper Valley P.T.A.":
				harperArt = c.ArtURL
			}
		}
	}
	if fireArt != "https://img/600x600bb.jpg" {
		t.Fatalf("Fire art = %q, want cached iTunes cover", fireArt)
	}
	if harperArt != "" {
		t.Fatalf("Harper (cached miss) art = %q, want placeholder", harperArt)
	}
}

func needArtTitles(qs []artQuery) []string {
	var out []string
	for _, q := range qs {
		out = append(out, q.Title)
	}
	return out
}

func sameSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	m := map[string]int{}
	for _, s := range got {
		m[s]++
	}
	for _, s := range want {
		if m[s] == 0 {
			return false
		}
		m[s]--
	}
	return true
}
