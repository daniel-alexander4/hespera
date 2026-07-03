package web

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"hespera/internal/video"
)

// skipSegment is a player-skippable range on the absolute media timeline. Kind is
// one of "intro", "recap", "commercial" — the player labels its Skip button and
// (when auto-skip is toggled on) jumps past the range. Built once per playback
// session from cheap external markers: embedded chapters + an EDL sidecar.
type skipSegment struct {
	StartSec float64 `json:"start"`
	EndSec   float64 `json:"end"`
	Kind     string  `json:"kind"`
}

// Chapter-title classifiers. Conservative on purpose — a normal "Chapter 1" must
// never match, and bare "ad"/"op" are excluded to avoid false positives (the EDL
// sidecar, not chapter titles, is the reliable commercial source).
var (
	reCommercialChapter = regexp.MustCompile(`(?i)\b(commercials?|advert(isement)?s?|ad break)\b`)
	reRecapChapter      = regexp.MustCompile(`(?i)\b(recap|previously)\b`)
	reIntroChapter      = regexp.MustCompile(`(?i)\b(intro|opening( credits)?|title sequence|main title)\b`)
)

// classifyChapter maps a chapter title to a skip kind, or ("", false) if the
// chapter is not skippable (a normal content chapter).
func classifyChapter(title string) (string, bool) {
	switch {
	case reCommercialChapter.MatchString(title):
		return "commercial", true
	case reRecapChapter.MatchString(title):
		return "recap", true
	case reIntroChapter.MatchString(title):
		return "intro", true
	}
	return "", false
}

// chapterMark is one chapter start on the absolute timeline, emitted verbatim
// in the playback session for the seek-bar tick layer (unlike skip segments,
// which only carry the classified intro/recap/commercial chapters).
type chapterMark struct {
	Start float64 `json:"start"`
	Title string  `json:"title"`
}

// chapterMarks maps every probed chapter to a tick — content chapters
// included; classification is the skip system's concern, not the tick layer's.
func chapterMarks(probe *video.ProbeResult) []chapterMark {
	if probe == nil {
		return nil
	}
	var out []chapterMark
	for _, c := range probe.Chapters {
		out = append(out, chapterMark{Start: c.StartSec, Title: c.Title})
	}
	return out
}

// skipSegmentsFor collects skip ranges for a media file from its probed chapters
// (classified by title) and a sibling `<file>.edl` commercial sidecar (comskip /
// Kodi format), if present. cleanPath must be the pathguard-resolved absolute
// path; "" reads chapters only. Pure-ish (one small sidecar read) — called once
// per playback session, never on the hot path.
func skipSegmentsFor(probe *video.ProbeResult, cleanPath string) []skipSegment {
	var segs []skipSegment
	if probe != nil {
		for _, c := range probe.Chapters {
			if kind, ok := classifyChapter(c.Title); ok && c.EndSec > c.StartSec {
				segs = append(segs, skipSegment{StartSec: c.StartSec, EndSec: c.EndSec, Kind: kind})
			}
		}
	}
	if cleanPath != "" {
		if ext := filepath.Ext(cleanPath); ext != "" {
			segs = append(segs, readEDLSegments(strings.TrimSuffix(cleanPath, ext)+".edl")...)
		}
	}
	return segs
}

// dbTVSkipSegments returns skip segments detected by audio-fingerprinting (stored
// in tv_skip_segments) for a TV file, merged into the session alongside the
// chapter/EDL markers. Best-effort: a query error yields none.
func (h *Handler) dbTVSkipSegments(ctx context.Context, fileID int64) []skipSegment {
	rows, err := h.db.QueryContext(ctx, `SELECT kind, start_sec, end_sec FROM tv_skip_segments WHERE file_id = ?`, fileID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var segs []skipSegment
	for rows.Next() {
		var s skipSegment
		if err := rows.Scan(&s.Kind, &s.StartSec, &s.EndSec); err == nil && s.EndSec > s.StartSec {
			segs = append(segs, s)
		}
	}
	return segs
}

// readEDLSegments parses an EDL sidecar into commercial skip segments. Each line
// is "START END [ACTION]" (whitespace-separated seconds); action 0 (cut) and 3
// (commercial break) are commercials, 1 (mute) / 2 (scene) are ignored, absent
// defaults to 0. A missing or unreadable sidecar yields no segments.
func readEDLSegments(edlPath string) []skipSegment {
	f, err := os.Open(edlPath)
	if err != nil {
		return nil
	}
	defer f.Close()
	var segs []skipSegment
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		start, e1 := strconv.ParseFloat(fields[0], 64)
		end, e2 := strconv.ParseFloat(fields[1], 64)
		if e1 != nil || e2 != nil || end <= start {
			continue
		}
		action := 0
		if len(fields) >= 3 {
			action, _ = strconv.Atoi(fields[2])
		}
		if action != 0 && action != 3 {
			continue
		}
		segs = append(segs, skipSegment{StartSec: start, EndSec: end, Kind: "commercial"})
	}
	return segs
}
