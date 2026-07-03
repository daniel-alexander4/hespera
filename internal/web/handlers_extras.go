package web

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"hespera/internal/video"
)

// Local extras: files ingested from a title's Extras/Featurettes/Trailers/…
// dirs (is_extra=1, see tvscan.ClassifyExtra). Ownership is derived from the
// path at render time — an extra belongs to the title whose folder contains
// it — so there is no owner column to go stale on rematch/unmatch/move.

// extraRow is one playable bonus-content file on a title's detail page.
type extraRow struct {
	FileID      int64
	Title       string
	Category    string
	DurationMin int
	ResumePct   int
	Completed   bool
}

// extrasUnderDirs returns the extras rows whose path lies under one of dirs,
// with resume state joined in. table/progressTable are trusted constants
// (tv_series_files/tv_playback_progress or the movie pair). Library roots are
// dropped from dirs — a title file sitting directly under the root would
// otherwise adopt every root-level title's extras. Prefix filtering happens in
// Go (extras populations are small; avoids LIKE-escaping).
func (h *Handler) extrasUnderDirs(ctx context.Context, table, progressTable string, dirs []string) []extraRow {
	dirs = h.dropLibraryRoots(ctx, dirs)
	if len(dirs) == 0 {
		return nil
	}
	prefixes := make([]string, 0, len(dirs))
	for _, d := range dirs {
		prefixes = append(prefixes, filepath.Clean(d)+string(os.PathSeparator))
	}

	rows, err := h.db.QueryContext(ctx, `
SELECT f.id, f.abs_path, f.extra_title, f.extra_category, f.stream_info_json,
       COALESCE(p.position_seconds,0), COALESCE(p.duration_seconds,0), COALESCE(p.completed,0)
FROM `+table+` f
LEFT JOIN `+progressTable+` p ON p.file_id = f.id
WHERE f.is_extra=1`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []extraRow
	for rows.Next() {
		var e extraRow
		var absPath, streamInfo string
		var pos, dur float64
		var completed int
		if rows.Scan(&e.FileID, &absPath, &e.Title, &e.Category, &streamInfo, &pos, &dur, &completed) != nil {
			continue
		}
		clean := filepath.Clean(absPath)
		owned := false
		for _, p := range prefixes {
			if strings.HasPrefix(clean, p) {
				owned = true
				break
			}
		}
		if !owned {
			continue
		}
		e.Completed = completed == 1
		total := probeDurationSeconds(streamInfo)
		if total <= 0 {
			total = dur
		}
		if total > 0 {
			e.DurationMin = int(total/60 + 0.5)
			if e.DurationMin == 0 {
				e.DurationMin = 1
			}
			if pos > 0 {
				e.ResumePct = int(pos * 100 / total)
			}
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		return strings.ToLower(out[i].Title) < strings.ToLower(out[j].Title)
	})
	return out
}

// dropLibraryRoots removes any dir that IS a library root (cleaned), keeping
// only genuine title folders.
func (h *Handler) dropLibraryRoots(ctx context.Context, dirs []string) []string {
	if len(dirs) == 0 {
		return nil
	}
	roots := map[string]bool{}
	if rows, err := h.db.QueryContext(ctx, "SELECT root_path FROM libraries"); err == nil {
		defer rows.Close()
		for rows.Next() {
			var r string
			if rows.Scan(&r) == nil {
				roots[filepath.Clean(r)] = true
			}
		}
	}
	var out []string
	for _, d := range dirs {
		if !roots[filepath.Clean(d)] {
			out = append(out, d)
		}
	}
	return out
}

// probeDurationSeconds extracts the container duration from a stored
// stream_info_json blob; 0 when absent/unparseable.
func probeDurationSeconds(streamInfo string) float64 {
	if streamInfo == "" || streamInfo == "{}" {
		return 0
	}
	var pr video.ProbeResult
	if json.Unmarshal([]byte(streamInfo), &pr) != nil {
		return 0
	}
	d, _ := strconv.ParseFloat(pr.Format.Duration, 64)
	return d
}

// libraryRootPath returns a library's cleaned root path (” when unknown).
func (h *Handler) libraryRootPath(ctx context.Context, libraryID int64) string {
	var root string
	if h.db.QueryRowContext(ctx, "SELECT root_path FROM libraries WHERE id=?", libraryID).Scan(&root) != nil {
		return ""
	}
	return filepath.Clean(root)
}
