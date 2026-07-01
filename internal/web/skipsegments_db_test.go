package web

import (
	"context"
	"testing"
)

func TestDBTVSkipSegments(t *testing.T) {
	h, db := newTestHandler(t)
	if _, err := db.Exec(`INSERT INTO libraries (id, name, type, root_path) VALUES (1, 'TV', 'tv', '/media')`); err != nil {
		t.Fatalf("seed library: %v", err)
	}
	res, err := db.Exec(`INSERT INTO tv_series_files (library_id, abs_path) VALUES (1, '/media/a.mkv')`)
	if err != nil {
		t.Fatalf("seed file: %v", err)
	}
	fid, _ := res.LastInsertId()

	if _, err := db.Exec(`INSERT INTO tv_skip_segments (file_id, kind, start_sec, end_sec, source) VALUES (?, 'intro', 18.7, 51.8, 'fingerprint')`, fid); err != nil {
		t.Fatalf("insert intro: %v", err)
	}
	// A degenerate row (end <= start) must be filtered out by the merge query.
	if _, err := db.Exec(`INSERT INTO tv_skip_segments (file_id, kind, start_sec, end_sec, source) VALUES (?, 'credits', 100, 100, 'fingerprint')`, fid); err != nil {
		t.Fatalf("insert degenerate: %v", err)
	}

	segs := h.dbTVSkipSegments(context.Background(), fid)
	if len(segs) != 1 {
		t.Fatalf("got %d segments, want 1 (degenerate filtered): %+v", len(segs), segs)
	}
	if segs[0].Kind != "intro" || segs[0].StartSec != 18.7 || segs[0].EndSec != 51.8 {
		t.Errorf("segment = %+v, want intro 18.7-51.8", segs[0])
	}

	// A file with no detected segments yields none.
	if got := h.dbTVSkipSegments(context.Background(), 999); len(got) != 0 {
		t.Errorf("unknown file should have no segments, got %+v", got)
	}
}
