// Package thumbgc removes orphaned thumbnail/art files — on-disk images under a
// thumbs directory that no DB art_path column references any longer (a superseded
// cover after a rematch or a format-changing re-upload, or art left behind when a
// pruned album/series row was deleted). It is the on-disk complement to the
// scanners' DB-row prune.
package thumbgc

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Grace is how recently a file must have been modified to be spared. Art is
// written to disk and its art_path committed in a separate step a moment later,
// and a manual upload can land off the job queue; sparing freshly-written files
// guarantees that write-then-reference race can never delete a live image.
// Mirrors the HLS cache pruner's grace window.
const Grace = 10 * time.Minute

var artExts = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".webp": true}

// Sweep deletes orphaned art files directly under dir: regular image files whose
// absolute path is referenced by none of the given art_path queries and which
// were last modified more than grace ago. Returns the number removed.
//
// It is conservative on every axis — a missing dir is a no-op, an ambiguous stat
// error skips the file, only the four known image extensions are ever removed,
// and anything modified within grace is spared — so a needed image is never
// deleted. Callers run it as the final step of a match job, where the
// single-worker job queue already serializes it against every other art writer.
//
// IMPORTANT: queries MUST enumerate every art_path column whose files live under
// dir. Adding a new art_path column without adding its query here would let this
// delete referenced art. Today: thumbs/music ← music_albums + music_artists;
// thumbs/tv ← tv_series_art.
func Sweep(ctx context.Context, db *sql.DB, dir string, grace time.Duration, queries ...string) (int, error) {
	referenced, err := referencedSet(ctx, db, queries...)
	if err != nil {
		return 0, err
	}

	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}

	now := time.Now()
	deleted := 0
	for _, e := range entries {
		select {
		case <-ctx.Done():
			return deleted, ctx.Err()
		default:
		}
		if e.IsDir() || !artExts[strings.ToLower(filepath.Ext(e.Name()))] {
			continue
		}
		full := filepath.Join(dir, e.Name())
		if referenced[full] {
			continue
		}
		info, err := e.Info()
		if err != nil {
			slog.Warn("thumbgc stat", "path", full, "err", err)
			continue
		}
		if now.Sub(info.ModTime()) < grace {
			continue
		}
		if err := os.Remove(full); err != nil {
			slog.Warn("thumbgc remove", "path", full, "err", err)
			continue
		}
		deleted++
	}
	return deleted, nil
}

// referencedSet collects the non-empty art_path values returned by each query
// into a set for membership testing. Stored paths are absolute and built with
// the same filepath.Join the caller uses to build dir, so they compare directly.
func referencedSet(ctx context.Context, db *sql.DB, queries ...string) (map[string]bool, error) {
	set := make(map[string]bool)
	for _, q := range queries {
		rows, err := db.QueryContext(ctx, q)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				rows.Close()
				return nil, err
			}
			if p = strings.TrimSpace(p); p != "" {
				set[p] = true
			}
		}
		cerr := rows.Err()
		rows.Close()
		if cerr != nil {
			return nil, cerr
		}
	}
	return set, nil
}
