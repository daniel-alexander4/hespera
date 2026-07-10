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

	"hespera/internal/jobs"
	"hespera/internal/pathguard"
	"hespera/internal/video"
)

// Trickplay: seek-bar preview sprites, generated per file into
// DataDir/cache/trickplay/<TrickplayKey>/ (content-addressed on
// path+mtime+size, so a changed file regenerates under a new key and the old
// key becomes an orphan the job's sweep removes). Generation is a scan-chained
// job (measured ~15s per full movie); serving is a thin cache-dir file server
// that touches the dir so size-cap eviction is LRU. Sprites are durable —
// unlike HLS segments they get NO age TTL (a TTL would delete sets the next
// scan immediately regenerates, an eviction↔regeneration churn loop).

func (h *Handler) trickplayCacheRoot() string {
	return filepath.Join(h.cfg.DataDir, "cache", "trickplay")
}

// generateTrickplayMissing generates sprites for every library file that
// doesn't have a current set (manifest missing for the file's content key),
// then sweeps orphaned cache dirs. The row query is streamed and only
// missing-manifest files are kept — on a big library the full row set dwarfs
// the (usually empty) todo list. Generation stops when the cache root reaches
// the size cap: sets the pruner would evict next sweep aren't worth the
// ffmpeg. table is tv_series_files or movie_files.
func (h *Handler) generateTrickplayMissing(ctx context.Context, table string, jobID, libraryID int64) error {
	mediaRoot := filepath.Clean(h.cfg.MediaRoot)
	root := h.trickplayCacheRoot()
	type target struct {
		id          int64
		absPath     string
		mtime, size int64
	}
	// is_extra=0: sprite generation is the expensive chained job (~15s/file) and
	// extras are rarely scrubbed — the hover preview degrades to timestamp-only.
	rows, err := h.db.QueryContext(ctx,
		"SELECT id, abs_path, mtime_unix, file_size_bytes FROM "+table+" WHERE library_id=? AND is_extra=0", libraryID)
	if err != nil {
		return fmt.Errorf("query files for trickplay: %w", err)
	}
	var todo []target
	for rows.Next() {
		var t target
		if err := rows.Scan(&t.id, &t.absPath, &t.mtime, &t.size); err != nil {
			rows.Close()
			return fmt.Errorf("scan file row: %w", err)
		}
		dir := filepath.Join(root, video.TrickplayKey(t.absPath, time.Unix(t.mtime, 0), t.size))
		if _, err := os.Stat(filepath.Join(dir, "manifest.json")); os.IsNotExist(err) {
			todo = append(todo, t)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate files: %w", err)
	}
	_, _ = h.db.ExecContext(ctx, "UPDATE scan_jobs SET progress_total=? WHERE id=?", len(todo), jobID)

	budget := h.cfg.TrickplayCacheMaxBytes
	used := dirTreeSize(root)
	generated, failed := 0, 0
	for i, t := range todo {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		// Yield to a waiting interactive job (scan/match/probe/integrity) rather
		// than block it behind this slow sweep — re-enqueued to finish the rest
		// once the interactive work runs. The check is a cheap EXISTS vs ~15s of
		// ffmpeg per file, so polling every file gives the tightest hand-off.
		if i > 0 && h.jobs.HasQueuedInteractive() {
			// Flush real progress before the worker requeues this row, so the
			// paused row is honest.
			_, _ = h.db.ExecContext(ctx, "UPDATE scan_jobs SET progress_current=? WHERE id=?", i, jobID)
			return jobs.ErrYielded
		}
		if budget > 0 && used >= budget {
			slog.Warn("trickplay cache at size cap — skipping remaining files",
				"cap_bytes", budget, "used_bytes", used, "skipped", len(todo)-i)
			_, _ = h.db.ExecContext(ctx, "UPDATE scan_jobs SET progress_current=? WHERE id=?", i, jobID)
			break
		}
		resolved, err := pathguard.ResolveExistingUnderRoot(mediaRoot, t.absPath)
		if err != nil {
			continue // gone/moved — next scan's relink/prune owns it
		}
		dir := filepath.Join(root, video.TrickplayKey(t.absPath, time.Unix(t.mtime, 0), t.size))
		if err := video.GenerateTrickplay(ctx, resolved, dir); err != nil {
			failed++
			slog.Warn("trickplay generate", "file_id", t.id, "path", resolved, "err", err)
			continue
		}
		generated++
		used += dirTreeSize(dir)
		if (i+1)%5 == 0 || i+1 == len(todo) {
			_, _ = h.db.ExecContext(ctx, "UPDATE scan_jobs SET progress_current=? WHERE id=?", i+1, jobID)
		}
	}
	slog.Info("trickplay generation complete",
		"library_id", libraryID, "table", table, "missing", len(todo), "generated", generated, "failed", failed)

	h.sweepTrickplayOrphans(ctx, root)
	return nil
}

// sweepTrickplayOrphans removes cache dirs whose content key no current TV or
// movie file claims — sprites for files since deleted or changed (a byte
// change moves the file to a new key). This sweep, not an age TTL, is what
// reclaims dead sets: the keys are unhashable back to paths, so validity is
// derived by marking. Claims are collected across BOTH tables and ALL
// libraries — the cache root is shared, so a per-library mark set would reap
// other libraries' live sprites. Memory is bounded by the cache dir count,
// never library size. Fail-safe: any claim-query error skips the sweep (never
// sweep against a partial claim set). An age guard spares dirs younger than
// an hour (racing writers).
func (h *Handler) sweepTrickplayOrphans(ctx context.Context, root string) {
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) == 0 {
		return
	}
	unclaimed := make(map[string]bool, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			unclaimed[e.Name()] = true
		}
	}
	for _, table := range []string{"tv_series_files", "movie_files"} {
		rows, err := h.db.QueryContext(ctx,
			"SELECT abs_path, mtime_unix, file_size_bytes FROM "+table)
		if err != nil {
			return
		}
		for rows.Next() {
			var p string
			var mt, sz int64
			if rows.Scan(&p, &mt, &sz) != nil {
				rows.Close()
				return
			}
			delete(unclaimed, video.TrickplayKey(p, time.Unix(mt, 0), sz))
		}
		rows.Close()
		if rows.Err() != nil {
			return
		}
	}
	removed := 0
	for name := range unclaimed {
		p := filepath.Join(root, name)
		if st, err := os.Stat(p); err != nil || time.Since(st.ModTime()) < time.Hour {
			continue
		}
		if os.RemoveAll(p) == nil {
			removed++
		}
	}
	if removed > 0 {
		slog.Info("trickplay orphan sweep", "removed_dirs", removed)
	}
}

// dirTreeSize sums the file bytes under root (0 when absent).
func dirTreeSize(root string) int64 {
	var size int64
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			size += info.Size()
		}
		return nil
	})
	return size
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
		// Refresh the dir mtime so size-cap eviction (PruneCache oldest-first)
		// is LRU by last use, not generation time.
		now := time.Now()
		_ = os.Chtimes(dir, now, now)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "max-age=86400")
		http.ServeFile(w, r, filepath.Join(dir, asset))
	}
}
