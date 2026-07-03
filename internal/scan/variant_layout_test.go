package scan

import (
	"context"
	"testing"
)

// TestVariantMergeLayoutMatrix drives the co-location variant merge through
// synthetic directory layouts — the realistic ways a rip splits one
// compilation into subfolders. The merge operates purely on DB path strings
// (albumTrackDirs never touches the filesystem), so a layout is just crafted
// abs_path values on seeded rows.
//
// Each case seeds an untagged VA compilation "Mixed" (2010): a multi-artist
// candidate (two tracks, two artists) plus a per-artist fragment row (third
// artist) whose track lives in the layout's other subfolder. `merge` states
// the DESIRED outcome: disc-synonym subfolders (Disc/CD/Vol/Volume/Part/Pt/
// Side, numbered or lettered sides) normalize onto the album folder and the
// fragment is absorbed; arbitrary subdirs (per-artist, bonus/) deliberately
// stay split — folding those is the over-merge/data-loss direction the
// co-location fix exists to prevent, and a split compilation is visible and
// fixable by tagging, while a silently absorbed distinct album is not.
func TestVariantMergeLayoutMatrix(t *testing.T) {
	cases := []struct {
		name  string
		cand1 string // candidate track 1 (artist one)
		cand2 string // candidate track 2 (artist two)
		frag  string // fragment track (artist three)
		merge bool   // desired: fragment absorbed into the compilation
	}{
		{"flat", "comp/a.mp3", "comp/b.mp3", "comp/c.mp3", true},
		{"disc-n", "comp/Disc 1/a.mp3", "comp/Disc 1/b.mp3", "comp/Disc 2/c.mp3", true},
		{"cd-nospace", "comp/CD1/a.mp3", "comp/CD1/b.mp3", "comp/CD2/c.mp3", true},
		{"vol-n", "comp/Vol 1/a.mp3", "comp/Vol 1/b.mp3", "comp/Vol 2/c.mp3", true},
		{"vol-dot-n", "comp/Vol. 1/a.mp3", "comp/Vol. 1/b.mp3", "comp/Vol. 2/c.mp3", true},
		{"volume-n", "comp/Volume 1/a.mp3", "comp/Volume 1/b.mp3", "comp/Volume 2/c.mp3", true},
		{"part-n", "comp/Part 1/a.mp3", "comp/Part 1/b.mp3", "comp/Part 2/c.mp3", true},
		{"pt-dot-n", "comp/Pt. 1/a.mp3", "comp/Pt. 1/b.mp3", "comp/Pt. 2/c.mp3", true},
		{"side-letter", "comp/Side A/a.mp3", "comp/Side A/b.mp3", "comp/Side B/c.mp3", true},
		{"side-n", "comp/Side 1/a.mp3", "comp/Side 1/b.mp3", "comp/Side 2/c.mp3", true},
		// Arbitrary subdirs: NOT disc synonyms — stay split by design.
		{"per-artist-subdirs", "comp/Artist One/a.mp3", "comp/Artist Two/b.mp3", "comp/Artist Three/c.mp3", false},
		{"bonus-subdir", "comp/a.mp3", "comp/b.mp3", "comp/bonus/c.mp3", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			libID := seedLibrary(t, db, "Music", "music", "/tmp/music")
			ctx := context.Background()

			a1 := seedArtist(t, db, libID, "Artist One")
			a2 := seedArtist(t, db, libID, "Artist Two")
			a3 := seedArtist(t, db, libID, "Artist Three")
			cand := seedAlbum(t, db, libID, a1, "Mixed", 2010, false)
			seedTrack(t, db, libID, a1, cand, "Song A", 1, "/tmp/music/"+tc.cand1)
			seedTrack(t, db, libID, a2, cand, "Song B", 2, "/tmp/music/"+tc.cand2)
			frag := seedAlbum(t, db, libID, a3, "Mixed", 2010, false)
			seedTrack(t, db, libID, a3, frag, "Song C", 3, "/tmp/music/"+tc.frag)

			scanner := &Scanner{DB: db}
			if err := scanner.finalizeCompilations(ctx, libID); err != nil {
				t.Fatalf("finalizeCompilations: %v", err)
			}

			var candTracks, fragTracks int
			if err := db.QueryRow("SELECT COUNT(*) FROM music_tracks WHERE album_id=?", cand).Scan(&candTracks); err != nil {
				t.Fatalf("count candidate tracks: %v", err)
			}
			if err := db.QueryRow("SELECT COUNT(*) FROM music_tracks WHERE album_id=?", frag).Scan(&fragTracks); err != nil {
				t.Fatalf("count fragment tracks: %v", err)
			}

			if tc.merge {
				if candTracks != 3 || fragTracks != 0 {
					t.Fatalf("layout %q should merge: candidate=%d (want 3) fragment=%d (want 0)", tc.name, candTracks, fragTracks)
				}
			} else {
				if candTracks != 2 || fragTracks != 1 {
					t.Fatalf("layout %q should stay split: candidate=%d (want 2) fragment=%d (want 1)", tc.name, candTracks, fragTracks)
				}
			}

			// The candidate itself is always promoted to a VA compilation.
			var isComp int
			if err := db.QueryRow("SELECT is_compilation FROM music_albums WHERE id=?", cand).Scan(&isComp); err != nil {
				t.Fatalf("candidate row: %v", err)
			}
			if isComp != 1 {
				t.Fatalf("layout %q: candidate not marked compilation", tc.name)
			}
		})
	}
}

// TestAlbumDirKeyDiscSynonyms pins the normalization vocabulary directly:
// disc-synonym subfolders collapse onto the parent, arbitrary names don't.
func TestAlbumDirKeyDiscSynonyms(t *testing.T) {
	collapse := []string{"Disc 1", "disc 12", "Disk 2", "CD1", "cd 3", "Vol 1", "Vol.1", "Vol. 2", "Volume 3", "Part 1", "part 2", "Pt. 1", "pt 2", "Side A", "side b", "Side 1"}
	for _, d := range collapse {
		if got := albumDirKey("/m/album/" + d + "/t.mp3"); got != "/m/album" {
			t.Errorf("%q should collapse onto the album dir, got %q", d, got)
		}
	}
	keep := []string{"Artist One", "bonus", "Extras", "2", "A", "Sideways", "Partly Cloudy", "cds", "vole 1", "Part"}
	for _, d := range keep {
		want := "/m/album/" + d
		if got := albumDirKey(want + "/t.mp3"); got != want {
			t.Errorf("%q should NOT collapse, got %q", d, got)
		}
	}
}
