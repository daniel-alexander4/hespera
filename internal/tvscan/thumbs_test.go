package tvscan

import (
	"context"
	"fmt"
	"testing"

	"hespera/internal/config"
)

// TestThumbOffset pins the grab-point heuristic: a fraction of the stored
// duration (else the fixed fallback), with a detected intro acting as a FLOOR
// — the point is pushed past intro end + lead only when it would otherwise
// land inside or just past the intro, never anchored to the intro's end
// (that put title cards in the thumbs).
func TestThumbOffset(t *testing.T) {
	db := openTestDB(t)
	s := New(config.Config{}, db)
	libID := seedLibrary(t, db, "TV", "tv", "/tv")
	ctx := context.Background()

	fileN := 0
	newFile := func(introStart, introEnd float64) int64 {
		fileN++
		res, err := db.Exec(
			"INSERT INTO tv_series_files (library_id, abs_path) VALUES (?, ?)",
			libID, fmt.Sprintf("/tv/show/e%d.mkv", fileN))
		if err != nil {
			t.Fatalf("insert file: %v", err)
		}
		id, _ := res.LastInsertId()
		if introEnd > 0 {
			if _, err := db.Exec(
				"INSERT INTO tv_skip_segments (file_id, kind, start_sec, end_sec) VALUES (?, 'intro', ?, ?)",
				id, introStart, introEnd); err != nil {
				t.Fatalf("insert segment: %v", err)
			}
		}
		return id
	}
	frac := 1000 * epThumbDurationFrac // 250 for a 1000s episode

	tests := []struct {
		name                 string
		introStart, introEnd float64
		duration             string
		want                 float64
	}{
		{"duration fraction, no intro", 0, 0, "1000", frac},
		{"early intro leaves the fraction alone", 30, 90, "1000", frac},
		{"intro spanning the fraction floors it", 200, 400, "1000", 400 + epThumbIntroLead},
		{"intro just before the fraction still floors (lead clearance)", 100, 240, "1000", 240 + epThumbIntroLead},
		{"invalid intro (end<=start) ignored", 90, 90, "1000", frac},
		{"fallback floored by intro when no duration", 10, 200, "", 200 + epThumbIntroLead},
		{"fixed fallback on missing duration", 0, 0, "", epThumbFallbackOffset},
		{"fixed fallback on N/A duration", 0, 0, "N/A", epThumbFallbackOffset},
		{"fixed fallback on zero duration", 0, 0, "0", epThumbFallbackOffset},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id := newFile(tc.introStart, tc.introEnd)
			if got := s.thumbOffset(ctx, id, tc.duration); got != tc.want {
				t.Fatalf("offset = %v, want %v", got, tc.want)
			}
		})
	}
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

// TestEpisodeThumbRelPaths pins the shard layout and the legacy-flat fallback
// order removal relies on.
func TestEpisodeThumbRelPaths(t *testing.T) {
	got := EpisodeThumbRelPaths(0x1ab)
	want := []string{"ab/ep_427.webp", "ep_427.webp"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("EpisodeThumbRelPaths = %v, want %v", got, want)
	}
}

// TestThumbCandidateDurationExtraction pins the candidate query's json_extract:
// only the duration string leaves SQLite, never the full probe blob.
func TestThumbCandidateDurationExtraction(t *testing.T) {
	db := openTestDB(t)
	libID := seedLibrary(t, db, "TV", "tv", "/tv")
	if _, err := db.Exec(
		"INSERT INTO tv_series_files (library_id, abs_path, stream_info_json) VALUES (?, '/tv/a.mkv', ?), (?, '/tv/b.mkv', '{}')",
		libID, `{"format":{"duration":"2520.04","tags":{"x":"y"}},"streams":[{}]}`, libID); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rows, err := db.Query(
		`SELECT abs_path, COALESCE(json_extract(stream_info_json, '$.format.duration'), '')
		 FROM tv_series_files WHERE library_id=? AND is_extra=0 AND thumb_path='' ORDER BY abs_path`, libID)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var p, d string
		if err := rows.Scan(&p, &d); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[p] = d
	}
	if got["/tv/a.mkv"] != "2520.04" || got["/tv/b.mkv"] != "" {
		t.Fatalf("extracted durations = %v", got)
	}
}
