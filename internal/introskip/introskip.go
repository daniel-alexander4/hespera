// Package introskip detects a TV season's shared intro (title theme) by comparing
// per-episode audio fingerprints — the part of the audio that's near-identical
// across episodes. It's the marker-less detection source behind the skip-intro
// feature (for files with no chapters/EDL). Pure data: the fingerprint extraction
// (ffmpeg/chromaprint) is injected by the caller, so this package has no deps and
// is fully unit-testable.
package introskip

import "math/bits"

// Tunables — calibrated against real episode fingerprints (Doctor Who S02, where the
// title theme is a ~33s region matching across episodes at offsets up to ~44s apart).
const (
	bitThreshold = 6    // max differing bits (of 32) for two fingerprint points to "match"
	gapTolerance = 3    // consecutive non-matching points tolerated mid-run (encode noise)
	minIntroSec  = 15.0 // a match shorter than this isn't a credible intro — ignored
)

// Episode is one episode's intro-window fingerprint.
type Episode struct {
	FileID int64
	Points []uint32
	Rate   float64 // points per second (index/Rate = seconds into the window)
}

// Segment is a detected intro range on the episode timeline, in seconds.
type Segment struct {
	StartSec float64
	EndSec   float64
}

// matchRun finds the longest near-identical run between fingerprints a and b across
// all alignment shifts, returning its span (in points), the winning shift (j-i, so
// b's index = a's index + shift), and its start index in a. A "match" is a per-point
// Hamming distance ≤ bitThreshold; up to gapTolerance non-matching points are
// tolerated within a run (different encodes are bit-similar, not bit-identical).
func matchRun(a, b []uint32) (span, shift, startA int) {
	na, nb := len(a), len(b)
	for s := -(nb - 1); s < na; s++ {
		lo := 0
		if -s > lo {
			lo = -s
		}
		hi := na
		if nb-s < hi {
			hi = nb - s
		}
		inRun := false
		runStart, lastMatch, gaps := 0, 0, 0
		flush := func() {
			if inRun {
				if l := lastMatch - runStart + 1; l > span {
					span, shift, startA = l, s, runStart
				}
				inRun = false
				gaps = 0
			}
		}
		for i := lo; i < hi; i++ {
			if bits.OnesCount32(a[i]^b[i+s]) <= bitThreshold {
				if !inRun {
					inRun = true
					runStart = i
				}
				lastMatch = i
				gaps = 0
			} else if inRun {
				gaps++
				if gaps > gapTolerance {
					flush()
				}
			}
		}
		flush()
	}
	return span, shift, startA
}

// DetectIntros compares every pair of episodes in a season and returns, per episode
// file id, its detected intro segment. An episode is credited the longest confident
// (≥ minIntroSec) match it participates in — at that episode's own offset, since the
// theme lands at different positions per episode. Episodes with no confident match
// get no entry (better no intro than a false skip).
func DetectIntros(eps []Episode) map[int64]Segment {
	type cand struct {
		seg  Segment
		span int
	}
	cands := map[int64][]cand{}
	for i := 0; i < len(eps); i++ {
		for j := i + 1; j < len(eps); j++ {
			a, b := eps[i], eps[j]
			if a.Rate <= 0 || b.Rate <= 0 || len(a.Points) == 0 || len(b.Points) == 0 {
				continue
			}
			span, shift, startA := matchRun(a.Points, b.Points)
			if float64(span)/a.Rate < minIntroSec {
				continue
			}
			startB := startA + shift
			cands[a.FileID] = append(cands[a.FileID], cand{Segment{float64(startA) / a.Rate, float64(startA+span) / a.Rate}, span})
			cands[b.FileID] = append(cands[b.FileID], cand{Segment{float64(startB) / b.Rate, float64(startB+span) / b.Rate}, span})
		}
	}
	out := map[int64]Segment{}
	for id, cs := range cands {
		best := cs[0]
		for _, c := range cs[1:] {
			if c.span > best.span {
				best = c
			}
		}
		if best.seg.StartSec < 0 {
			best.seg.StartSec = 0
		}
		out[id] = best.seg
	}
	return out
}
