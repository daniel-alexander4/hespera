package photoscan

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"hespera/internal/video"
)

// ReprobeMissing re-probes video clips whose stream_info_json is empty — a
// scan-time probe failure (often a transient ffmpeg-semaphore acquire
// timeout). The movie/TV twin: chained after every photos scan, near-free
// when nothing is missing. Stills never probe.
func (s *Scanner) ReprobeMissing(ctx context.Context, jobID, libraryID int64) error {
	rows, err := s.DB.QueryContext(ctx, `
SELECT id, abs_path, mtime_unix FROM photos
WHERE library_id=? AND kind='video' AND (stream_info_json='' OR stream_info_json='{}')
`, libraryID)
	if err != nil {
		return err
	}
	type cand struct {
		id      int64
		absPath string
		mtime   int64
	}
	var cands []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.id, &c.absPath, &c.mtime); err != nil {
			rows.Close()
			return err
		}
		cands = append(cands, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(cands) == 0 {
		return nil
	}
	_, _ = s.DB.ExecContext(ctx, "UPDATE scan_jobs SET progress_total=? WHERE id=?", len(cands), jobID)

	for i, c := range cands {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		probeResult, probeErr := video.Probe(ctx, c.absPath)
		if probeErr != nil {
			slog.Warn("photoscan reprobe", "path", c.absPath, "err", probeErr)
			continue
		}
		b, _ := json.Marshal(probeResult)
		// A successful late probe may also recover the real capture time for a
		// clip that fell back to mtime at scan.
		if ct := strings.TrimSpace(probeResult.Format.CreationTime); ct != "" {
			if t, err := time.Parse(time.RFC3339Nano, ct); err == nil {
				if _, err := s.DB.ExecContext(ctx,
					"UPDATE photos SET stream_info_json=?, taken_at=?, taken_source='probe' WHERE id=? AND taken_source='mtime'",
					string(b), t.Local().Format(takenAtLayout), c.id); err != nil {
					return err
				}
			}
		}
		if _, err := s.DB.ExecContext(ctx, "UPDATE photos SET stream_info_json=? WHERE id=?", string(b), c.id); err != nil {
			return err
		}
		if (i+1)%25 == 0 || i+1 == len(cands) {
			_, _ = s.DB.ExecContext(ctx, "UPDATE scan_jobs SET progress_current=? WHERE id=?", i+1, jobID)
		}
	}
	return nil
}
