package introskip

import "testing"

// gen produces a deterministic high-entropy point sequence (splitmix64-ish); distinct
// seeds yield streams whose points are far apart in Hamming distance, so unrelated
// filler never spuriously "matches" within bitThreshold.
func gen(seed uint64, n int) []uint32 {
	out := make([]uint32, n)
	x := seed
	for i := range out {
		x += 0x9E3779B97F4A7C15
		z := x
		z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
		z = (z ^ (z >> 27)) * 0x94D049BB133111EB
		z ^= z >> 31
		out[i] = uint32(z)
	}
	return out
}

func cat(parts ...[]uint32) []uint32 {
	var out []uint32
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func TestMatchRunRecoversSharedBlockAtOffset(t *testing.T) {
	shared := gen(99, 200)
	a := cat(gen(1, 100), shared, gen(2, 150)) // shared at index 100
	b := cat(gen(3, 40), shared, gen(4, 220))  // shared at index 40
	span, shift, startA := matchRun(a, b)
	if span < 195 || span > 205 {
		t.Fatalf("span = %d, want ~200", span)
	}
	if shift != -60 { // b index - a index = 40 - 100
		t.Errorf("shift = %d, want -60", shift)
	}
	if startA < 99 || startA > 101 {
		t.Errorf("startA = %d, want ~100", startA)
	}
}

func TestMatchRunNoSharedRegion(t *testing.T) {
	a := gen(1, 300)
	b := gen(2, 300)
	if span, _, _ := matchRun(a, b); span > 10 {
		t.Fatalf("unrelated fingerprints matched span=%d, want ~0", span)
	}
}

func TestDetectIntrosPerEpisodeOffsets(t *testing.T) {
	shared := gen(99, 200) // 200 pts ÷ 8/s = 25s ≥ minIntroSec
	a := cat(gen(1, 100), shared, gen(2, 150))
	b := cat(gen(3, 40), shared, gen(4, 220))
	got := DetectIntros([]Episode{
		{FileID: 10, Points: a, Rate: 8},
		{FileID: 20, Points: b, Rate: 8},
	})
	if len(got) != 2 {
		t.Fatalf("got %d segments, want 2: %+v", len(got), got)
	}
	// A's intro ≈ [100/8, 300/8] = [12.5, 37.5]; B's ≈ [40/8, 240/8] = [5, 30].
	if s := got[10]; s.StartSec < 11 || s.StartSec > 14 || s.EndSec < 36 || s.EndSec > 39 {
		t.Errorf("file 10 segment = %+v, want ~[12.5,37.5]", s)
	}
	if s := got[20]; s.StartSec < 4 || s.StartSec > 7 || s.EndSec < 28 || s.EndSec > 31 {
		t.Errorf("file 20 segment = %+v, want ~[5,30]", s)
	}
}

func TestDetectIntrosShortMatchIgnored(t *testing.T) {
	// A shared block of only 50 pts ÷ 8 = 6.25s < minIntroSec → no intro credited.
	shared := gen(99, 50)
	a := cat(gen(1, 100), shared, gen(2, 150))
	b := cat(gen(3, 40), shared, gen(4, 220))
	if got := DetectIntros([]Episode{{FileID: 1, Points: a, Rate: 8}, {FileID: 2, Points: b, Rate: 8}}); len(got) != 0 {
		t.Fatalf("expected no intros for a sub-threshold match, got %+v", got)
	}
}

func TestDetectIntrosNoFalsePositiveOnUnrelated(t *testing.T) {
	got := DetectIntros([]Episode{
		{FileID: 1, Points: gen(1, 400), Rate: 8},
		{FileID: 2, Points: gen(2, 400), Rate: 8},
		{FileID: 3, Points: gen(3, 400), Rate: 8},
	})
	if len(got) != 0 {
		t.Fatalf("unrelated episodes produced intros: %+v", got)
	}
}
