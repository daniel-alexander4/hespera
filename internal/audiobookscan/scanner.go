// Package audiobookscan scans audiobook libraries (the `audiobooks` vertical).
// Audiobooks are the fourth thin clone of the movie playback layer, audio-only:
// no provider matching — title/author come from the container tags (m4b
// convention: album = book title, artist = author, falling back to the
// filename), chapters/duration/codecs from the stored ffprobe result, which
// feeds the playback session exactly as tv_series_files/movie_files do.
// Single files only (the chaptered m4b shape); multi-file folder grouping is
// Tier 2 — each file lists as its own row until then.
package audiobookscan

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"hespera/internal/config"
	"hespera/internal/pathguard"
	"hespera/internal/video"
)

type Scanner struct {
	Cfg config.Config
	DB  *sql.DB
	// ShouldYield, when set, is polled by the long cover-thumbnail sweep so a
	// queued interactive job (scan/match) doesn't wait behind it.
	ShouldYield func() bool
}

func New(cfg config.Config, db *sql.DB) *Scanner {
	return &Scanner{Cfg: cfg, DB: db}
}

// audioExts is every audio container an audiobooks library ingests — the
// library TYPE marks intent, so any audio file in one is an audiobook.
var audioExts = map[string]bool{
	".m4b": true, ".m4a": true, ".mp3": true, ".ogg": true, ".opus": true,
	".flac": true, ".aac": true, ".wav": true, ".wma": true,
}

func isAudiobookFile(path string) bool {
	return audioExts[strings.ToLower(filepath.Ext(path))]
}

func skipDirName(name string) bool {
	return strings.HasPrefix(name, ".") || name == "@eaDir"
}

func titleFromFilename(path string) string {
	base := filepath.Base(path)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	return strings.TrimSpace(strings.ReplaceAll(base, "_", " "))
}

// ScanAudiobooks walks an audiobooks library's root and upserts every audio
// file. Probe/tag failures degrade (empty stream_info_json → the chained
// audiobook_probe backfills; filename title) and never fail the scan.
func (s *Scanner) ScanAudiobooks(ctx context.Context, jobID, libraryID int64) error {
	var root string
	if err := s.DB.QueryRowContext(ctx,
		"SELECT root_path FROM libraries WHERE id=? AND type='audiobooks'", libraryID,
	).Scan(&root); err != nil {
		return fmt.Errorf("load audiobooks library %d: %w", libraryID, err)
	}
	mediaRoot := filepath.Clean(s.Cfg.MediaRoot)
	cleanRoot, err := pathguard.ResolveExistingUnderRoot(mediaRoot, root)
	if err != nil {
		return fmt.Errorf("library root outside media root: %w", err)
	}

	if total := s.countEligibleFiles(cleanRoot); total > 0 {
		_, _ = s.DB.ExecContext(ctx, "UPDATE scan_jobs SET progress_total=? WHERE id=?", total, jobID)
	}

	processed := 0
	err = filepath.WalkDir(cleanRoot, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			slog.Warn("audiobookscan: walk error", "path", p, "err", walkErr)
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			if p != cleanRoot && skipDirName(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") || !isAudiobookFile(p) {
			return nil
		}
		clean, err := pathguard.ResolveExistingUnderRoot(mediaRoot, p)
		if err != nil {
			slog.Warn("audiobookscan: file escapes media root; skipping", "path", p)
			return nil
		}
		if err := s.scanFile(ctx, libraryID, clean); err != nil {
			slog.Warn("audiobookscan: file failed; skipping", "path", clean, "err", err)
		}
		processed++
		if processed%25 == 0 {
			_, _ = s.DB.ExecContext(ctx, "UPDATE scan_jobs SET progress_current=? WHERE id=?", processed, jobID)
		}
		return nil
	})
	if err != nil {
		return err
	}
	_, _ = s.DB.ExecContext(ctx, "UPDATE scan_jobs SET progress_current=? WHERE id=?", processed, jobID)

	// Prune guard: 0 files walked + rows present = an unmounted mount point,
	// not an emptied library.
	if processed == 0 {
		var rows int
		_ = s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM audiobooks WHERE library_id=?", libraryID).Scan(&rows)
		if rows > 0 {
			slog.Warn("audiobookscan: no files found but library has rows — root looks unmounted; skipping prune",
				"library_id", libraryID, "root", cleanRoot)
			return nil
		}
	}
	s.relinkMovedFiles(ctx, libraryID, cleanRoot)
	s.pruneMissingFiles(ctx, libraryID, cleanRoot)
	return nil
}

func (s *Scanner) countEligibleFiles(root string) int {
	n := 0
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != root && skipDirName(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasPrefix(d.Name(), ".") && isAudiobookFile(p) {
			n++
		}
		return nil
	})
	return n
}

// probeFacts is what one ffprobe pass yields for the row: the marshaled probe
// (playback session input), the duration, and the chapter count.
func probeFacts(ctx context.Context, path string) (streamJSON string, durationSec float64, chapters int) {
	probe, err := video.Probe(ctx, path)
	if err != nil {
		return "", 0, 0 // empty → the chained audiobook_probe backfills
	}
	b, err := json.Marshal(probe)
	if err != nil {
		return "", 0, 0
	}
	if d, err := strconv.ParseFloat(strings.TrimSpace(probe.Format.Duration), 64); err == nil && d > 0 {
		durationSec = d
	}
	return string(b), durationSec, len(probe.Chapters)
}

// bookIdentity resolves title/author from the container tags: album = book
// title (the m4b convention; a per-file title tag often names a chapter),
// artist = author. Anything missing falls back to the filename / empty.
func bookIdentity(ctx context.Context, path string) (title, author string) {
	title = titleFromFilename(path)
	tags, _, err := video.ProbeTags(ctx, path)
	if err != nil {
		return title, ""
	}
	if v := strings.TrimSpace(tags["album"]); v != "" {
		title = v
	} else if v := strings.TrimSpace(tags["title"]); v != "" {
		title = v
	}
	if v := strings.TrimSpace(tags["artist"]); v != "" {
		author = v
	} else if v := strings.TrimSpace(tags["album_artist"]); v != "" {
		author = v
	}
	return title, author
}

func (s *Scanner) scanFile(ctx context.Context, libraryID int64, path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	size, mtime := st.Size(), st.ModTime().Unix()

	// Unchanged fast-path: same bytes → no re-probe, no upsert.
	var have int
	err = s.DB.QueryRowContext(ctx,
		"SELECT 1 FROM audiobooks WHERE library_id=? AND abs_path=? AND file_size_bytes=? AND mtime_unix=?",
		libraryID, path, size, mtime,
	).Scan(&have)
	if err == nil && have == 1 {
		return nil
	}

	streamJSON, durationSec, chapters := probeFacts(ctx, path)
	title, author := bookIdentity(ctx, path)
	container := strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")

	_, err = s.DB.ExecContext(ctx, `
INSERT INTO audiobooks (library_id, abs_path, container, title, author, duration_seconds, chapter_count, file_size_bytes, mtime_unix, stream_info_json, thumb_path)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '')
ON CONFLICT(library_id, abs_path) DO UPDATE SET
  container=excluded.container,
  title=excluded.title,
  author=excluded.author,
  duration_seconds=excluded.duration_seconds,
  chapter_count=excluded.chapter_count,
  file_size_bytes=excluded.file_size_bytes,
  mtime_unix=excluded.mtime_unix,
  stream_info_json=excluded.stream_info_json,
  thumb_path='',
  updated_at=datetime('now')`,
		libraryID, path, container, title, author, durationSec, chapters, size, mtime, orEmptyJSON(streamJSON))
	return err
}

func orEmptyJSON(s string) string {
	if s == "" {
		return "{}"
	}
	return s
}

// ReprobeMissing backfills rows whose scan-time probe failed (empty
// stream_info_json) — the tv/movie/photo reprobe twin, chained after a scan.
func (s *Scanner) ReprobeMissing(ctx context.Context, jobID, libraryID int64) error {
	rows, err := s.DB.QueryContext(ctx,
		"SELECT id, abs_path FROM audiobooks WHERE library_id=? AND (stream_info_json='' OR stream_info_json='{}')", libraryID)
	if err != nil {
		return err
	}
	type cand struct {
		id   int64
		path string
	}
	var cands []cand
	for rows.Next() {
		var c cand
		if rows.Scan(&c.id, &c.path) == nil {
			cands = append(cands, c)
		}
	}
	rows.Close()
	if len(cands) == 0 {
		return nil
	}
	_, _ = s.DB.ExecContext(ctx, "UPDATE scan_jobs SET progress_total=? WHERE id=?", len(cands), jobID)
	for i, c := range cands {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, _ = s.DB.ExecContext(ctx, "UPDATE scan_jobs SET progress_current=? WHERE id=?", i+1, jobID)
		streamJSON, durationSec, chapters := probeFacts(ctx, c.path)
		if streamJSON == "" {
			continue // still failing — next run retries
		}
		_, _ = s.DB.ExecContext(ctx,
			"UPDATE audiobooks SET stream_info_json=?, duration_seconds=?, chapter_count=?, updated_at=datetime('now') WHERE id=?",
			streamJSON, durationSec, chapters, c.id)
	}
	return nil
}

// relinkMovedFiles pairs an orphan with a single surviving file sharing its
// (size, mtime) signature and transfers the playback progress before prune.
// Strictly 1:1.
func (s *Scanner) relinkMovedFiles(ctx context.Context, libraryID int64, root string) {
	type row struct {
		id          int64
		path        string
		size, mtime int64
	}
	type sig struct{ size, mtime int64 }

	rows, err := s.DB.QueryContext(ctx,
		"SELECT id, abs_path, file_size_bytes, mtime_unix FROM audiobooks WHERE library_id=?", libraryID)
	if err != nil {
		return
	}
	var all []row
	for rows.Next() {
		var r row
		if rows.Scan(&r.id, &r.path, &r.size, &r.mtime) == nil {
			all = append(all, r)
		}
	}
	rows.Close()

	survivors := make(map[sig][]row)
	var orphans []row
	orphanCount := make(map[sig]int)
	for _, r := range all {
		if !strings.HasPrefix(r.path, root+string(filepath.Separator)) && r.path != root {
			continue
		}
		if _, err := os.Stat(r.path); err == nil {
			k := sig{r.size, r.mtime}
			survivors[k] = append(survivors[k], r)
		} else if os.IsNotExist(err) {
			orphans = append(orphans, r)
			orphanCount[sig{r.size, r.mtime}]++
		}
	}
	for _, o := range orphans {
		k := sig{o.size, o.mtime}
		cand := survivors[k]
		if len(cand) != 1 || orphanCount[k] != 1 {
			continue
		}
		_, _ = s.DB.ExecContext(ctx, `
INSERT INTO audiobook_playback_progress (file_id, position_seconds, duration_seconds, completed, updated_at)
SELECT ?, position_seconds, duration_seconds, completed, updated_at FROM audiobook_playback_progress WHERE file_id=?
ON CONFLICT(file_id) DO UPDATE SET
  position_seconds=excluded.position_seconds,
  duration_seconds=excluded.duration_seconds,
  completed=MAX(completed, excluded.completed),
  updated_at=excluded.updated_at`,
			cand[0].id, o.id)
	}
}

func (s *Scanner) pruneMissingFiles(ctx context.Context, libraryID int64, root string) {
	rows, err := s.DB.QueryContext(ctx,
		"SELECT id, abs_path FROM audiobooks WHERE library_id=?", libraryID)
	if err != nil {
		return
	}
	var staleIDs []int64
	for rows.Next() {
		var id int64
		var p string
		if rows.Scan(&id, &p) != nil {
			continue
		}
		if !strings.HasPrefix(p, root+string(filepath.Separator)) && p != root {
			continue
		}
		if _, err := os.Stat(p); os.IsNotExist(err) {
			staleIDs = append(staleIDs, id)
		}
	}
	rows.Close()
	for _, id := range staleIDs {
		if _, err := s.DB.ExecContext(ctx, "DELETE FROM audiobooks WHERE id=?", id); err != nil {
			continue
		}
		for _, name := range ThumbFileNames(id) {
			_ = os.Remove(filepath.Join(s.Cfg.DataDir, "thumbs", "audiobooks", name))
		}
	}
	if len(staleIDs) > 0 {
		slog.Info("audiobookscan: pruned missing files", "library_id", libraryID, "count", len(staleIDs))
	}
}
