package web

import (
	"fmt"
	"sort"
	"testing"
)

// TestTrackSongKey pins what counts as "the same song by the same artist" —
// the grouping key behind one-recording-per-song. The collapses that matter are
// the live/remaster annotations; the non-collapses that matter are a different
// artist and a title whose leading digits are part of the name.
func TestTrackSongKey(t *testing.T) {
	tests := []struct {
		name   string
		a, b   trackRow
		wantEq bool
	}{
		{"studio vs live annotation",
			trackRow{ArtistID: 1, Title: "Whole Lotta Rosie"},
			trackRow{ArtistID: 1, Title: "Whole Lotta Rosie (Live)"}, true},
		{"live show with venue and year",
			trackRow{ArtistID: 1, Title: "Problem Child"},
			trackRow{ArtistID: 1, Title: "Problem Child (Live at Atlantic Studios 1977)"}, true},
		{"remaster annotation",
			trackRow{ArtistID: 1, Title: "Back In Black"},
			trackRow{ArtistID: 1, Title: "Back In Black - 2003 Remaster"}, true},
		{"punctuation and case",
			trackRow{ArtistID: 1, Title: "Rock 'n' Roll Damnation"},
			trackRow{ArtistID: 1, Title: "rock n roll damnation"}, true},
		{"different artists never collapse",
			trackRow{ArtistID: 1, Title: "Problem Child"},
			trackRow{ArtistID: 2, Title: "Problem Child"}, false},
		{"different songs",
			trackRow{ArtistID: 1, Title: "Squealer"},
			trackRow{ArtistID: 1, Title: "Sandman"}, false},
		{"leading digits are part of the title, not a track number",
			trackRow{ArtistID: 1, Title: "99 Problems"},
			trackRow{ArtistID: 1, Title: "Problems"}, false},
		{"a filename-derived title is a missed collapse, never a wrong one",
			trackRow{ArtistID: 1, Title: "06 Back In Black"},
			trackRow{ArtistID: 1, Title: "Back In Black"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := trackSongKey(tc.a) == trackSongKey(tc.b); got != tc.wantEq {
				t.Fatalf("trackSongKey(%q)==trackSongKey(%q) = %v, want %v (%q vs %q)",
					tc.a.Title, tc.b.Title, got, tc.wantEq, trackSongKey(tc.a), trackSongKey(tc.b))
			}
		})
	}
}

// TestDedupeByTitle covers the collapse itself: first of each group wins (so the
// caller picks the winning recording by ordering its input), everything else is
// preserved in order, and short inputs are passed straight through.
func TestDedupeByTitle(t *testing.T) {
	rows := []trackRow{
		{ID: 1, ArtistID: 7, Title: "Problem Child"},
		{ID: 2, ArtistID: 7, Title: "Squealer"},
		{ID: 3, ArtistID: 7, Title: "Problem Child (Live)"},
		{ID: 4, ArtistID: 9, Title: "Problem Child"},
		{ID: 5, ArtistID: 7, Title: "Problem Child"},
	}
	got := dedupeByTitle(rows)
	want := []int64{1, 2, 4}
	ids := make([]int64, 0, len(got))
	for _, r := range got {
		ids = append(ids, r.ID)
	}
	if fmt.Sprint(ids) != fmt.Sprint(want) {
		t.Fatalf("dedupeByTitle ids = %v, want %v (first of each group, order preserved)", ids, want)
	}

	// The caller controls which recording survives purely by input order.
	reversed := []trackRow{rows[2], rows[0]}
	if out := dedupeByTitle(reversed); len(out) != 1 || out[0].ID != 3 {
		t.Fatalf("reversed input should keep the live copy, got %+v", out)
	}

	for _, short := range [][]trackRow{nil, {{ID: 1, Title: "Only"}}} {
		if out := dedupeByTitle(short); len(out) != len(short) {
			t.Fatalf("dedupeByTitle(%v) = %v, want passthrough", short, out)
		}
	}
}

// TestDrawMixOneRecordingPerSong: a mix is always generated, so it collapses
// unconditionally — and it needs it more than a sweep does, since popularity is
// credited by normalized title and the per-artist top-N window therefore pulls
// a song's duplicate rows in together. The seed must still open the mix, and
// must win its own group rather than being replaced by its live copy.
func TestDrawMixOneRecordingPerSong(t *testing.T) {
	pool := []trackRow{
		{ID: 1, ArtistID: 7, Title: "Problem Child"},        // the seed
		{ID: 2, ArtistID: 7, Title: "Problem Child (Live)"}, // same song as the seed
		{ID: 3, ArtistID: 7, Title: "Squealer"},
		{ID: 4, ArtistID: 8, Title: "Radar Love"},
		{ID: 5, ArtistID: 8, Title: "Radar Love - 2010 Remaster"},
	}
	weights := map[int64]int{7: 2, 8: 1}

	for i := 0; i < 50; i++ {
		out := drawMix(pool, weights, 7, 1)
		if len(out) == 0 || out[0].ID != 1 {
			t.Fatalf("the seed must open the mix and survive the collapse, got %+v", out)
		}
		if len(out) != 3 {
			t.Fatalf("mix = %d tracks, want 3 (seed + Squealer + one Radar Love)", len(out))
		}
		seen := map[string]bool{}
		for _, tr := range out {
			if k := trackSongKey(tr); seen[k] {
				t.Fatalf("mix played the same song twice: %+v", out)
			} else {
				seen[k] = true
			}
		}
	}

	// A seedless mix (artist radio) collapses the same way.
	out := drawMix(pool, weights, 7, 0)
	seen := map[string]bool{}
	for _, tr := range out {
		if k := trackSongKey(tr); seen[k] {
			t.Fatalf("seedless mix played the same song twice: %+v", out)
		} else {
			seen[k] = true
		}
	}
	if len(out) != 3 {
		t.Fatalf("seedless mix = %d tracks, want 3", len(out))
	}
}

// TestMusicQueueShuffleDedupe is the behavioral gate: a shuffled catalog sweep
// plays one recording per song, while every deliberate ordering — an ordered
// sweep, a playlist, an album — keeps all of them.
func TestMusicQueueShuffleDedupe(t *testing.T) {
	h, db := newTestHandler(t)
	router := h.Router()
	libID, artistID, albumID, trackID := seedMusicData(t, db)

	// A live album that re-covers the studio track, plus one song only it has.
	res, err := db.Exec(`INSERT INTO music_albums (library_id, artist_id, album_artist_id, title, year, is_compilation) VALUES (?, ?, ?, 'Live Album', 2025, 0)`, libID, artistID, artistID)
	if err != nil {
		t.Fatalf("insert live album: %v", err)
	}
	liveID, _ := res.LastInsertId()
	for i, title := range []string{"Track 1 (Live)", "Live Only"} {
		if _, err := db.Exec(`INSERT INTO music_tracks (library_id, artist_id, album_id, title, track_no, disc_no, abs_path, mime_type) VALUES (?, ?, ?, ?, ?, 1, ?, 'audio/mpeg')`,
			libID, artistID, liveID, title, i+1, fmt.Sprintf("/test/live%d.mp3", i+1)); err != nil {
			t.Fatalf("insert live track: %v", err)
		}
	}

	sorted := func(in []string) []string {
		out := append([]string(nil), in...)
		sort.Strings(out)
		return out
	}
	artistQ := fmt.Sprintf("source=artist&artist=%d", artistID)

	// Ordered: everything, including both recordings of Track 1.
	_, ordered := queueTitles(t, router, artistQ)
	if want := []string{"Live Only", "Track 1", "Track 1 (Live)"}; fmt.Sprint(sorted(ordered)) != fmt.Sprint(want) {
		t.Fatalf("ordered artist queue = %v, want %v", sorted(ordered), want)
	}

	// Shuffled: one recording per song. Which one wins is random, so assert on
	// the count and on the non-duplicate surviving — never on a fixed pick.
	for i := 0; i < 20; i++ {
		_, shuffled := queueTitles(t, router, artistQ+"&shuffle=1")
		if len(shuffled) != 2 {
			t.Fatalf("shuffled artist queue = %v, want 2 tracks (one per song)", shuffled)
		}
		seen := map[string]bool{}
		for _, s := range shuffled {
			seen[s] = true
		}
		if !seen["Live Only"] {
			t.Fatalf("shuffled artist queue dropped the unique track: %v", shuffled)
		}
		if seen["Track 1"] == seen["Track 1 (Live)"] {
			t.Fatalf("shuffled artist queue should keep exactly one Track 1 recording, got %v", shuffled)
		}
	}

	// A playlist is the user's own curation — both recordings stay, even shuffled.
	res, err = db.Exec(`INSERT INTO playlists (name) VALUES ('Mine')`)
	if err != nil {
		t.Fatalf("insert playlist: %v", err)
	}
	playlistID, _ := res.LastInsertId()
	var liveTrackID int64
	if err := db.QueryRow(`SELECT id FROM music_tracks WHERE title='Track 1 (Live)'`).Scan(&liveTrackID); err != nil {
		t.Fatalf("find live track: %v", err)
	}
	for pos, id := range []int64{trackID, liveTrackID} {
		if _, err := db.Exec(`INSERT INTO playlist_tracks (playlist_id, track_id, position) VALUES (?, ?, ?)`, playlistID, id, pos+1); err != nil {
			t.Fatalf("insert playlist track: %v", err)
		}
	}
	_, pl := queueTitles(t, router, fmt.Sprintf("source=playlist&playlist=%d&shuffle=1", playlistID))
	if want := []string{"Track 1", "Track 1 (Live)"}; fmt.Sprint(sorted(pl)) != fmt.Sprint(want) {
		t.Fatalf("shuffled playlist = %v, want %v — curation is exempt", sorted(pl), want)
	}

	// An album is a sequenced work; shuffling one must not drop anything either.
	if _, err := db.Exec(`INSERT INTO music_tracks (library_id, artist_id, album_id, title, track_no, disc_no, abs_path, mime_type) VALUES (?, ?, ?, 'Track 1 (Live)', 2, 1, '/test/track1b.mp3', 'audio/mpeg')`,
		libID, artistID, albumID); err != nil {
		t.Fatalf("insert album dup: %v", err)
	}
	_, alb := queueTitles(t, router, fmt.Sprintf("album=%d&shuffle=1", albumID))
	if len(alb) != 2 {
		t.Fatalf("shuffled album = %v, want both tracks — an album is a sequenced work", alb)
	}
}
