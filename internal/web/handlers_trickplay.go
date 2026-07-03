package web

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"hespera/internal/pathguard"
	"hespera/internal/video"
)

// Trickplay: seek-bar preview sprites, generated per file into
// DataDir/cache/trickplay/<TrickplayKey>/ (content-addressed on
// path+mtime+size, so a changed file regenerates under a new key and the
// orphan ages out via the prune loop). Generation is a scan-chained job
// (measured ~15s per full movie); serving is a thin cache-dir file server.

func (h *Handler) trickplayCacheRoot() string {
	return filepath.Join(h.cfg.DataDir, "cache", "trickplay")
}

// trickplayCacheMaxBytes bounds the sprite cache. Measured ~1.8MB per full
// movie → 2GiB holds ~1000 titles; a fixed constant, deliberately not another
// env knob.
const trickplayCacheMaxBytes = 2 << 30

// generateTrickplayMissing generates sprites for every library file that
// doesn't have a current set (manifest missing for the file's content key).
// The reprobe-job shape: snapshot candidates, best-effort per file, ffmpeg
// gated inside GenerateTrickplay. table is tv_series_files or movie_files.
func (h *Handler) generateTrickplayMissing(ctx context.Context, table string, jobID, libraryID int64) error {
	mediaRoot := filepath.Clean(h.cfg.MediaRoot)
	rows, err := h.db.QueryContext(ctx,
		"SELECT id, abs_path, mtime_unix, file_size_bytes FROM "+table+" WHERE library_id=?", libraryID)
	if err != nil {
		return fmt.Errorf("query files for trickplay: %w", err)
	}
	type target struct {
		id          int64
		absPath     string
		mtime, size int64
	}
	var targets []target
	for rows.Next() {
		var t target
		if err := rows.Scan(&t.id, &t.absPath, &t.mtime, &t.size); err != nil {
			rows.Close()
			return fmt.Errorf("scan file row: %w", err)
		}
		targets = append(targets, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate files: %w", err)
	}

	// Filter to the ones actually missing a manifest, then report honest totals.
	var todo []target
	for _, t := range targets {
		dir := filepath.Join(h.trickplayCacheRoot(), video.TrickplayKey(t.absPath, time.Unix(t.mtime, 0), t.size))
		if _, err := os.Stat(filepath.Join(dir, "manifest.json")); os.IsNotExist(err) {
			todo = append(todo, t)
		}
	}
	_, _ = h.db.ExecContext(ctx, "UPDATE scan_jobs SET progress_total=? WHERE id=?", len(todo), jobID)

	generated, failed := 0, 0
	for i, t := range todo {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		resolved, err := pathguard.ResolveExistingUnderRoot(mediaRoot, t.absPath)
		if err != nil {
			continue // gone/moved — next scan's relink/prune owns it
		}
		dir := filepath.Join(h.trickplayCacheRoot(), video.TrickplayKey(t.absPath, time.Unix(t.mtime, 0), t.size))
		if err := video.GenerateTrickplay(ctx, resolved, dir); err != nil {
			failed++
			slog.Warn("trickplay generate", "file_id", t.id, "path", resolved, "err", err)
			continue
		}
		generated++
		if (i+1)%5 == 0 || i+1 == len(todo) {
			_, _ = h.db.ExecContext(ctx, "UPDATE scan_jobs SET progress_current=? WHERE id=?", i+1, jobID)
		}
	}
	slog.Info("trickplay generation complete",
		"library_id", libraryID, "table", table, "missing", len(todo), "generated", generated, "failed", failed)
	return nil
}

// trickplayAssetRe whitelists servable asset names — the manifest or one
// sprite sheet, nothing else.
var trickplayAssetRe = regexp.MustCompile(`^(manifest\.json|sprite\d{5}\.jpg)$`)

// streamTrickplay serves /stream/{tv,movie}-trickplay/{fileID}/{asset} from
// the file's current-content cache dir. A missing set is a plain 404 — the
// player degrades to no previews, silently.
func (h *Handler) streamTrickplay(table, prefix string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, prefix)
		idStr, asset, ok := strings.Cut(rest, "/")
		if !ok || !trickplayAssetRe.MatchString(asset) {
			http.NotFound(w, r)
			return
		}
		fileID, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil || fileID <= 0 {
			http.NotFound(w, r)
			return
		}
		var absPath string
		var mtime, size int64
		if err := h.db.QueryRowContext(r.Context(),
			"SELECT abs_path, mtime_unix, file_size_bytes FROM "+table+" WHERE id=?", fileID,
		).Scan(&absPath, &mtime, &size); err != nil {
			http.NotFound(w, r)
			return
		}
		dir := filepath.Join(h.trickplayCacheRoot(), video.TrickplayKey(absPath, time.Unix(mtime, 0), size))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "max-age=86400")
		http.ServeFile(w, r, filepath.Join(dir, asset))
	}
}
