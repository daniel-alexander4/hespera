package web

import (
	"context"
	"fmt"
	"testing"
)

// seedPhotos inserts a photos library plus the given rows.
// Each row: kind, taken_at, dir_rel — ids are assigned 1..n in order.
func seedPhotos(t *testing.T, h *Handler, rows [][3]string) {
	t.Helper()
	if _, err := h.db.Exec(
		"INSERT INTO libraries (id, name, type, root_path) VALUES (1, 'Pics', 'photos', '/pics')"); err != nil {
		t.Fatalf("insert library: %v", err)
	}
	for i, r := range rows {
		if _, err := h.db.Exec(`
INSERT INTO photos (id, library_id, abs_path, kind, taken_at, dir_rel)
VALUES (?, 1, ?, ?, ?, ?)`,
			i+1, fmt.Sprintf("/pics/f%d", i+1), r[0], r[1], r[2]); err != nil {
			t.Fatalf("insert photo %d: %v", i+1, err)
		}
	}
}

// TestPhotoNeighbors pins the neighbor resolution the viewer's ←/→ and the
// clip player's |< / >| depend on: filter respect (kind/year/dir), tie-breaks
// on identical taken_at, zero at the ends, and the asc/desc direction flip.
func TestPhotoNeighbors(t *testing.T) {
	h, _ := newTestHandler(t)
	// Display order (desc): id4 (2021) > id3 (2020, video) > id2 (2020 still,
	// tie broken by id) > id5 (2020 still, other dir) > id1 (2019, video).
	seedPhotos(t, h, [][3]string{
		{"video", "2019-05-01 10:00:00", "trip"},   // id1
		{"photo", "2020-06-01 10:00:00", "trip"},   // id2 — same taken_at as id3
		{"video", "2020-06-01 10:00:00", "trip"},   // id3
		{"photo", "2021-07-01 10:00:00", "trip"},   // id4
		{"photo", "2020-01-01 10:00:00", "other"},  // id5
	})
	ctx := context.Background()

	tests := []struct {
		name       string
		f          photoFilters
		id         int64
		takenAt    string
		prev, next int64
	}{
		// Default desc order over everything: order is 4, 3, 2, 5, 1.
		{"middle desc", photoFilters{Tab: "all", Order: "desc"}, 3, "2020-06-01 10:00:00", 4, 2},
		{"tie broken by id", photoFilters{Tab: "all", Order: "desc"}, 2, "2020-06-01 10:00:00", 3, 5},
		{"newest end", photoFilters{Tab: "all", Order: "desc"}, 4, "2021-07-01 10:00:00", 0, 3},
		{"oldest end", photoFilters{Tab: "all", Order: "desc"}, 1, "2019-05-01 10:00:00", 5, 0},
		// Videos tab: stills invisible — clips chain 3 <-> 1 directly.
		{"videos skip stills", photoFilters{Tab: "videos", Order: "desc"}, 3, "2020-06-01 10:00:00", 0, 1},
		{"videos oldest", photoFilters{Tab: "videos", Order: "desc"}, 1, "2019-05-01 10:00:00", 3, 0},
		// asc flips the walk direction: order is 5, 2, 3 among 2020 rows.
		{"asc direction", photoFilters{Tab: "all", Order: "asc"}, 2, "2020-06-01 10:00:00", 5, 3},
		// Year filter: only the 2020 rows exist; dir filter: only "trip".
		{"year filter", photoFilters{Tab: "all", Year: "2020", Order: "desc"}, 2, "2020-06-01 10:00:00", 3, 5},
		{"dir filter", photoFilters{Tab: "all", Dir: "trip", Order: "desc"}, 2, "2020-06-01 10:00:00", 3, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			prev, next := h.photoNeighbors(ctx, tc.f, tc.id, tc.takenAt)
			if prev != tc.prev || next != tc.next {
				t.Fatalf("photoNeighbors(%+v, id=%d) = (prev %d, next %d), want (%d, %d)",
					tc.f, tc.id, prev, next, tc.prev, tc.next)
			}
		})
	}
}

// TestPhotoPlayerForcesClipNeighbors pins the player's contract: whatever tab
// the launch context carried, |< / >| resolve against clips only — a still can
// never become a stepping target.
func TestPhotoPlayerForcesClipNeighbors(t *testing.T) {
	h, _ := newTestHandler(t)
	seedPhotos(t, h, [][3]string{
		{"video", "2019-05-01 10:00:00", "trip"}, // id1
		{"photo", "2020-06-01 10:00:00", "trip"}, // id2 — a still between the clips
		{"video", "2021-07-01 10:00:00", "trip"}, // id3
	})
	// The handler forces Tab=videos before resolving; mirror that here.
	f := photoFilters{Tab: "all", Order: "desc"}
	f.Tab = "videos"
	prev, next := h.photoNeighbors(context.Background(), f, 3, "2021-07-01 10:00:00")
	if prev != 0 || next != 1 {
		t.Fatalf("clip neighbors = (prev %d, next %d), want (0, 1) — a still must never be a target", prev, next)
	}
}
