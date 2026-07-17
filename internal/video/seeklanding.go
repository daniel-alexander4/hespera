package video

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// SeekLanding reports where a progressive stream started with an input `-ss t`
// on this file will actually begin. A remux stream-copies video, so ffmpeg's
// seek lands on the demuxer's seek point at or before t — the previous
// keyframe, up to one GOP earlier than requested. The playback session reports
// this to the client so it anchors streamStartOffset on the truth instead of
// the request; otherwise absolute-timed sidecar subtitle cues paint up to a
// GOP early on a resumed remux and saved progress overstates the position.
//
// Mechanism: ffprobe's -read_intervals <t>% performs the same avformat seek an
// input -ss does, so the first packet it returns IS the landing point (measured
// identical to ffmpeg's -ss landing on mp4 and mkv, ~0.1s per call). Callers
// gate this to index-backed containers — on mpegts the demuxer seek is not
// reproducible (ffprobe and ffmpeg land in different places), so no reliable
// answer exists there.
//
// The returned time is file-relative (pts minus the container start_time) —
// the timeline the player, the progress rows, and sidecar VTTs all share. Any
// failure (gate busy, timeout, non-keyframe first packet, out-of-range result)
// returns an error; callers fall back to the requested position, which is
// exactly today's behavior.
func SeekLanding(ctx context.Context, filePath string, t float64) (float64, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	release, err := acquire(ctx)
	if err != nil {
		return 0, fmt.Errorf("seek landing acquire slot: %w", err)
	}
	defer release()

	out, err := exec.CommandContext(ctx, "ffprobe",
		"-v", "error",
		"-select_streams", "v:0",
		"-read_intervals", fmt.Sprintf("%.3f%%+#1", t),
		"-show_entries", "packet=pts_time,dts_time,flags:format=start_time",
		"-print_format", "json",
		filePath,
	).Output()
	if err != nil {
		return 0, fmt.Errorf("ffprobe seek landing: %w", err)
	}
	return parseSeekLanding(out, t)
}

type seekLandingJSON struct {
	Packets []struct {
		PtsTime string `json:"pts_time"`
		DtsTime string `json:"dts_time"`
		Flags   string `json:"flags"`
	} `json:"packets"`
	Format struct {
		StartTime string `json:"start_time"`
	} `json:"format"`
}

// parseSeekLanding extracts the file-relative landing time from ffprobe's
// -read_intervals output for a requested position t. The first packet must be
// a keyframe (on an index-backed container the seek lands exactly on one —
// anything else means the demuxer seeked somewhere an input -ss won't) and the
// result must land in [0, t] — a landing after the request is the overshooting
// mpegts shape, where the requested position is the safer anchor.
func parseSeekLanding(data []byte, t float64) (float64, error) {
	var v seekLandingJSON
	if err := json.Unmarshal(data, &v); err != nil {
		return 0, fmt.Errorf("parse seek landing: %w", err)
	}
	if len(v.Packets) == 0 {
		return 0, fmt.Errorf("seek landing: no packets returned")
	}
	p := v.Packets[0]
	if !strings.Contains(p.Flags, "K") {
		return 0, fmt.Errorf("seek landing: first packet is not a keyframe (flags %q)", p.Flags)
	}
	raw := p.PtsTime
	if raw == "" || raw == "N/A" {
		raw = p.DtsTime
	}
	pts, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("seek landing: unparseable packet time %q", raw)
	}
	if st, serr := strconv.ParseFloat(v.Format.StartTime, 64); serr == nil && st > 0 {
		pts -= st
	}
	if pts < 0 {
		pts = 0
	}
	if pts > t+0.1 {
		return 0, fmt.Errorf("seek landing %.3f is past the requested %.3f", pts, t)
	}
	return pts, nil
}
