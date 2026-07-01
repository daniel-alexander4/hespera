package web

import (
	"bytes"
	"context"
	"regexp"
	"strconv"
	"strings"

	"hespera/internal/video"
)

// extractVTT runs ffmpeg (args must target `-f webvtt pipe:1`), buffers the
// output, and returns the WebVTT with overlapping cue end-times clamped to the
// next cue's start (clampVTTOverlaps). Buffering means a mid-stream ffmpeg
// failure can't leave a half-written 200 response, and the same overlap clamp
// applies to every subtitle source (embedded extract + OpenSubtitles convert).
func extractVTT(ctx context.Context, args []string) ([]byte, error) {
	var buf bytes.Buffer
	if err := video.StreamFFmpeg(ctx, &buf, args); err != nil {
		return nil, err
	}
	return clampVTTOverlaps(buf.Bytes()), nil
}

// vttTimingRe matches a WebVTT cue timing line: "[HH:]MM:SS.mmm --> [HH:]MM:SS.mmm[ settings]".
// Group 1 = start token, group 2 = end token, group 3 = trailing settings (incl. any
// leading whitespace / trailing \r), so an unchanged line round-trips byte-for-byte.
var vttTimingRe = regexp.MustCompile(`^((?:\d+:)?\d{2}:\d{2}\.\d{3})\s+-->\s+((?:\d+:)?\d{2}:\d{2}\.\d{3})(.*)$`)

// clampVTTOverlaps clamps each cue's end time to the next cue's start time when
// they overlap, so two cues can never be active at once (the data-level cause of
// stacked subtitles). Non-overlapping input is returned byte-for-byte unchanged;
// only a clamped timing line is rewritten, reusing the next cue's start token
// verbatim as the new end so formatting is preserved. File order is assumed to be
// time order (ffmpeg's webvtt output is sorted); an out-of-order pair is skipped.
func clampVTTOverlaps(b []byte) []byte {
	lines := strings.Split(string(b), "\n")
	type cue struct {
		lineIdx          int
		startTok, settle string
		startMS, endMS   int
	}
	var cues []cue
	for i, ln := range lines {
		m := vttTimingRe.FindStringSubmatch(ln)
		if m == nil {
			continue
		}
		start, ok1 := parseVTTMillis(m[1])
		end, ok2 := parseVTTMillis(m[2])
		if !ok1 || !ok2 {
			continue
		}
		cues = append(cues, cue{lineIdx: i, startTok: m[1], settle: m[3], startMS: start, endMS: end})
	}
	changed := false
	for i := 0; i+1 < len(cues); i++ {
		next := cues[i+1]
		if next.startMS < cues[i].startMS || cues[i].endMS <= next.startMS {
			continue // out of order, or no overlap
		}
		lines[cues[i].lineIdx] = cues[i].startTok + " --> " + next.startTok + cues[i].settle
		changed = true
	}
	if !changed {
		return b
	}
	return []byte(strings.Join(lines, "\n"))
}

// parseVTTMillis converts a "[HH:]MM:SS.mmm" WebVTT timestamp to milliseconds.
func parseVTTMillis(tok string) (int, bool) {
	dot := strings.IndexByte(tok, '.')
	if dot < 0 || len(tok)-dot != 4 {
		return 0, false
	}
	ms, err := strconv.Atoi(tok[dot+1:])
	if err != nil {
		return 0, false
	}
	parts := strings.Split(tok[:dot], ":")
	var h, m, s int
	switch len(parts) {
	case 3:
		h, _ = strconv.Atoi(parts[0])
		m, _ = strconv.Atoi(parts[1])
		s, _ = strconv.Atoi(parts[2])
	case 2:
		m, _ = strconv.Atoi(parts[0])
		s, _ = strconv.Atoi(parts[1])
	default:
		return 0, false
	}
	return ((h*60+m)*60+s)*1000 + ms, true
}
