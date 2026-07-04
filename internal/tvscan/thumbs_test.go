package tvscan

import (
	"context"
	"fmt"
	"testing"

	"hespera/internal/config"
)

// TestThumbOffset pins the grab-point heuristic: past a detected intro when
// one exists, else a fraction of the stored duration, else the fixed fallback.
func TestThumbOffset(t *testing.T) {
	db := openTestDB(t)
	s := New(config.Config{}, db)
	libID := seedLibrary(t, db, "TV", "tv", "/tv")
	ctx := context.Background()

	newFile := func(n int) int64 {
		res, err := db.Exec(
			"INSERT INTO tv_series_files (library_id, abs_path) VALUES (?, ?)",
			libID, fmt.Sprintf("/tv/show/e%d.mkv", n))
		if err != nil {
			t.Fatalf("insert file: %v", err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	durJSON := `{"format":{"duration":"1000"}}`

	t.Run("intro end wins", func(t *testing.T) {
		id := newFile(1)
		if _, err := db.Exec(
			"INSERT INTO tv_skip_segments (file_id, kind, start_sec, end_sec) VALUES (?, 'intro', 30, 90)", id); err != nil {
			t.Fatalf("insert segment: %v", err)
		}
		if got := s.thumbOffset(ctx, id, durJSON); got != 90+epThumbIntroLead {
			t.Fatalf("offset = %v, want %v", got, 90+epThumbIntroLead)
		}
	})

	t.Run("invalid intro falls to duration fraction", func(t *testing.T) {
		id := newFile(2)
		if _, err := db.Exec(
			"INSERT INTO tv_skip_segments (file_id, kind, start_sec, end_sec) VALUES (?, 'intro', 90, 90)", id); err != nil {
			t.Fatalf("insert segment: %v", err)
		}
		if got := s.thumbOffset(ctx, id, durJSON); got != 1000*epThumbDurationFrac {
			t.Fatalf("offset = %v, want %v", got, 1000*epThumbDurationFrac)
		}
	})

	t.Run("duration fraction without intro", func(t *testing.T) {
		if got := s.thumbOffset(ctx, newFile(3), durJSON); got != 1000*epThumbDurationFrac {
			t.Fatalf("offset = %v, want %v", got, 1000*epThumbDurationFrac)
		}
	})

	t.Run("fixed fallback on missing duration", func(t *testing.T) {
		for i, blob := range []string{"", "{}", `{"format":{"duration":"N/A"}}`, `{"format":{"duration":"0"}}`} {
			if got := s.thumbOffset(ctx, newFile(4+i), blob); got != epThumbFallbackOffset {
				t.Fatalf("offset(%q) = %v, want %v", blob, got, epThumbFallbackOffset)
			}
		}
	})
}

// TestUpsertResetsThumbPath pins the invalidation contract: a byte change
// resets thumb_path (the tv_thumb job regenerates), an unchanged upsert
// preserves it.
func TestUpsertResetsThumbPath(t *testing.T) {
	db := openTestDB(t)
	s := New(config.Config{}, db)
	libID := seedLibrary(t, db, "TV", "tv", "/tv")
	ctx := context.Background()
	ident := &EpisodeIdentity{ShowTitle: "Show", SeasonNumber: 1, EpisodeNumbers: []int{1}}

	if err := s.upsertTVFile(ctx, libID, "/tv/show/e1.mkv", "mkv", 100, 200, "{}", ident, extraFields{}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := db.Exec("UPDATE tv_series_files SET thumb_path='/thumbs/episodes/ep_1.webp'"); err != nil {
		t.Fatalf("set thumb: %v", err)
	}

	// Unchanged size+mtime: thumb survives.
	if err := s.upsertTVFile(ctx, libID, "/tv/show/e1.mkv", "mkv", 100, 200, "{}", ident, extraFields{}); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	var thumb string
	if err := db.QueryRow("SELECT thumb_path FROM tv_series_files").Scan(&thumb); err != nil {
		t.Fatalf("read thumb: %v", err)
	}
	if thumb == "" {
		t.Fatal("unchanged upsert reset thumb_path")
	}

	// Changed bytes: thumb resets to pending.
	if err := s.upsertTVFile(ctx, libID, "/tv/show/e1.mkv", "mkv", 101, 200, "{}", ident, extraFields{}); err != nil {
		t.Fatalf("changed upsert: %v", err)
	}
	if err := db.QueryRow("SELECT thumb_path FROM tv_series_files").Scan(&thumb); err != nil {
		t.Fatalf("read thumb: %v", err)
	}
	if thumb != "" {
		t.Fatalf("changed upsert kept thumb_path %q, want ''", thumb)
	}
}
