package main

import (
	"bytes"
	"context"
	"errors"
	"math"
	"strings"
	"testing"
)

// stopAfter is a writer that fails once it has taken n bytes, which is how these
// tests bound an otherwise endless generator: the real one stops the same way,
// when the mixer goes away and the pipe breaks.
type stopAfter struct {
	buf bytes.Buffer
	n   int
}

func (s *stopAfter) Write(p []byte) (int, error) {
	if s.buf.Len() >= s.n {
		return 0, errors.New("pipe closed")
	}
	return s.buf.Write(p)
}

// pcmStats reads interleaved int16 stereo and returns RMS and peak, both 0..1.
func pcmStats(b []byte) (rms, peak float64) {
	var sum float64
	n := 0
	for i := 0; i+3 < len(b); i += 4 {
		for _, s := range []float64{
			float64(int16(uint16(b[i])|uint16(b[i+1])<<8)) / 32768,
			float64(int16(uint16(b[i+2])|uint16(b[i+3])<<8)) / 32768,
		} {
			sum += s * s
			if a := math.Abs(s); a > peak {
				peak = a
			}
			n++
		}
	}
	if n == 0 {
		return 0, 0
	}
	return math.Sqrt(sum / float64(n)), peak
}

// TestEventGenDensity pins the arrival process. Poisson arrivals are what make
// rain sound like rain rather than a metronome, and the count is the only thing
// that says the generator produced anything at all — a silent layer and a sparse
// one are indistinguishable by ear.
func TestEventGenDensity(t *testing.T) {
	// Averaged over several seeds, not asserted on one. Arrivals are Poisson, so a
	// single realization of ~500 events carries a standard deviation of ~22 and a
	// one-seed threshold is a coin flip with a tight tolerance — which is exactly
	// how this test first failed on a change that altered the RNG consumption
	// pattern without touching the arrival process at all.
	const secs = 4
	const rate = 120
	totalEvents, totalFrames := 0, 0
	for _, seed := range []int64{1, 2, 3, 7, 11} {
		w := &stopAfter{n: noiseSampleRate * secs * 4}
		st, err := generateEvents(context.Background(), w, eventGenSpec{Rate: rate, DecayMs: 9, Amp: 0.5, Seed: seed})
		if err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		if st.Frames < noiseSampleRate*secs {
			t.Errorf("seed %d: only %d frames for %ds", seed, st.Frames, secs)
		}
		if st.Clipped != 0 {
			t.Errorf("seed %d: %d clipped samples at the shipped amplitude — silent distortion", seed, st.Clipped)
		}
		// Per-seed rate, against the frames actually produced rather than the
		// nominal window: the writer stops on a chunk boundary, not a second one.
		got := float64(st.Events) / (float64(st.Frames) / noiseSampleRate)
		t.Logf("seed %2d: %.1f events/s", seed, got)
		totalEvents += st.Events
		totalFrames += st.Frames
	}
	// Against the frames actually emitted, not the nominal window. Dividing by the
	// nominal seconds inflated this by 9% and read as a generator bias when the
	// only fault was the denominator — the writer stops on a chunk boundary, so it
	// overshoots the window it was asked for.
	mean := float64(totalEvents) / (float64(totalFrames) / noiseSampleRate)
	if math.Abs(mean-rate)/rate > 0.1 {
		t.Errorf("mean %.1f events/s across seeds, want ~%d (±10%%)", mean, rate)
	}
}

// TestEventGenLevelRegimes pins the level contract, which has TWO regimes with a
// sharp, computable boundary — and the boundary is not a wart, it is the price of
// clip safety.
//
// Let R = rate x decay (in seconds): the number of events expected to overlap
// within one time constant. The normalization asks for peak = Amp*k/sqrt(R), so:
//
//	R >= 1  the normalization governs and LEVEL IS DENSITY-INVARIANT — rate and
//	        decay are texture knobs, which is what keeps a preset's calibration
//	        from drifting when its texture is retuned.
//	R <  1  eventPeakMax governs instead, because the normalization is asking for
//	        an amplitude above full scale. Level then RISES with density, and the
//	        peak stays put. Most sparse presets live here (a cave drip is
//	        R = 0.036), which is exactly why they are balanced by lowering their
//	        BED rather than lifting the events — see the nature suite's comment.
//
// This test straddled the boundary before the ceiling existed and read as a
// regression when the ceiling was added; the ceiling was right and the test was
// under-specified.
func TestEventGenLevelRegimes(t *testing.T) {
	// Above the boundary: invariant. R = 4.3 and 8.6 at 9 ms.
	lo, loPk := levelAt(t, 480, 9)
	hi, hiPk := levelAt(t, 960, 9)
	if dB := 20 * math.Log10(hi/lo); math.Abs(dB) > 2 {
		t.Errorf("in the normalized regime 2x the density moved the level %.2f dB; rate must not be a volume knob there", dB)
	}
	// Decay is the second texture knob and must be just as neutral there. A first
	// cut normalized by event count alone, which put the peaks at -0.2 dBFS and
	// the level 4.5 dB hot the moment a decay was retuned.
	short, shortPk := levelAt(t, 300, 12) // R = 3.6
	long, longPk := levelAt(t, 300, 40)   // R = 12
	if d := 20 * math.Log10(long/short); math.Abs(d) > 2 {
		t.Errorf("in the normalized regime 3.3x the decay moved the level %.2f dB", d)
	}
	// Below the boundary: level rises with density, and that is correct — a few
	// drips at a fixed peak ARE quieter than many. What must NOT move is the peak.
	sparse, sparsePk := levelAt(t, 9, 4)  // R = 0.036, a fire crackle
	denser, denserPk := levelAt(t, 90, 4) // R = 0.36, still clamped
	if denser <= sparse {
		t.Errorf("under the ceiling, 10x the density should be louder: %.5f -> %.5f", sparse, denser)
	}
	for _, pk := range []struct {
		what string
		v    float64
	}{{"480/s 9ms", loPk}, {"960/s 9ms", hiPk}, {"300/s 12ms", shortPk},
		{"300/s 40ms", longPk}, {"9/s 4ms", sparsePk}, {"90/s 4ms", denserPk}} {
		if pk.v > eventPeakMax+0.01 {
			t.Errorf("%s peaked at %.3f, above the %.2f ceiling", pk.what, pk.v, eventPeakMax)
		}
	}
}

// levelAt renders a few seconds at one rate/decay and returns its RMS and peak.
func levelAt(t *testing.T, rate, decayMs float64) (float64, float64) {
	t.Helper()
	w := &stopAfter{n: noiseSampleRate * 3 * 4}
	if _, err := generateEvents(context.Background(), w, eventGenSpec{Rate: rate, DecayMs: decayMs, Amp: 0.5, Seed: 11}); err != nil {
		t.Fatalf("rate %g decay %g: %v", rate, decayMs, err)
	}
	return pcmStats(w.buf.Bytes())
}

// TestEventGenSparseDoesNotClip pins the ceiling that makes the sparse end safe.
// The 1/sqrt(rate x tau) normalization holds average loudness constant, which at a
// drip every two seconds demands an amplitude ABOVE full scale — three of the eight
// nature presets (campfire, cave, distant storm) clipped on precisely this, and the
// measurement that caught it was a per-preset level check, not a unit test.
func TestEventGenSparseDoesNotClip(t *testing.T) {
	for _, tc := range []struct{ rate, decay float64 }{
		{0.12, 1500}, // thunder: one every eight seconds, a 1.5 s rumble
		{0.25, 90},   // the deeper cave drip
		{0.6, 60},    // the cave drip
		{1.2, 25},    // a fire pop
		{9, 4},       // fire crackle
	} {
		w := &stopAfter{n: noiseSampleRate * 4 * 4}
		st, err := generateEvents(context.Background(), w,
			eventGenSpec{Rate: tc.rate, DecayMs: tc.decay, Amp: 0.5, Seed: 3})
		if err != nil {
			t.Fatalf("%g/s %gms: %v", tc.rate, tc.decay, err)
		}
		if st.Clipped != 0 {
			t.Errorf("%g/s %gms clipped %d samples — a sparse preset must not distort", tc.rate, tc.decay, st.Clipped)
		}
		_, pk := pcmStats(w.buf.Bytes())
		if pk > eventPeakMax+0.01 {
			t.Errorf("%g/s %gms peaked at %.3f, above the %.2f ceiling", tc.rate, tc.decay, pk, eventPeakMax)
		}
	}
}

// TestEventGenDeterministicWithSeed: an explicit seed reproduces exactly, which is
// what makes the tests above meaningful; without one the stream must differ, since
// an event layer exists precisely so a session is not identical to the last.
func TestEventGenDeterministicWithSeed(t *testing.T) {
	run := func(seed int64) []byte {
		w := &stopAfter{n: 48000}
		if _, err := generateEvents(context.Background(), w, eventGenSpec{Rate: 200, DecayMs: 8, Amp: 0.5, Seed: seed}); err != nil {
			t.Fatal(err)
		}
		return w.buf.Bytes()
	}
	if !bytes.Equal(run(42), run(42)) {
		t.Error("the same seed produced different audio")
	}
	if bytes.Equal(run(42), run(43)) {
		t.Error("different seeds produced identical audio")
	}
}

// TestEventGenStopsAndRefuses pins the two exits: a broken pipe is the normal end
// of a session and must not be an error, and a rate of zero is a refusal rather
// than a silent stream of nothing.
func TestEventGenStopsAndRefuses(t *testing.T) {
	w := &stopAfter{n: 1} // fails on the first write
	if _, err := generateEvents(context.Background(), w, eventGenSpec{Rate: 100, DecayMs: 5, Amp: 0.5, Seed: 1}); err != nil {
		t.Errorf("a closed pipe is how this ends normally, not an error: %v", err)
	}
	if _, err := generateEvents(context.Background(), &bytes.Buffer{}, eventGenSpec{Rate: 0}); err == nil {
		t.Error("a zero rate should be refused")
	}
	// A cancelled context stops it too, without an error.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := generateEvents(ctx, &bytes.Buffer{}, eventGenSpec{Rate: 100, DecayMs: 5, Amp: 0.5, Seed: 1}); err != nil {
		t.Errorf("a cancelled context should stop cleanly: %v", err)
	}
}

// TestEventGenArgsMatchTheStage pins the single-construction property: the runner
// and `--print` build the generator command from the same function, so the printed
// pipeline cannot drift from the played one. Only the binary path differs.
func TestEventGenArgsMatchTheStage(t *testing.T) {
	l := noiseLayer{EventsPerSec: 120, DecayMs: 9}
	if got := strings.Join(eventGenArgs(l), " "); got != "noise-events --rate 120 --decay 9" {
		t.Errorf("generator argv: %q", got)
	}
	runner := genCmdFor(l, "/usr/bin/ffmpeg", "/usr/bin/hesplay")
	printed := genCmdFor(l, "ffmpeg", "hesplay")
	if strings.Join(runner[1:], " ") != strings.Join(printed[1:], " ") {
		t.Errorf("printed flags differ from the runner's:\n %v\n %v", runner, printed)
	}
	if runner[0] != "/usr/bin/hesplay" || printed[0] != "hesplay" {
		t.Errorf("path flavours wrong: %q / %q", runner[0], printed[0])
	}
	// A noise layer needs no generator at all; an ffmpeg colour needs ffmpeg.
	if got := genCmdFor(noiseLayer{Type: "brownnoise", Speed: 0.02}, "ffmpeg", "hesplay"); got != nil {
		t.Errorf("a SoX colour needs no generator, got %v", got)
	}
	if got := genCmdFor(noiseLayer{Type: "velvetnoise"}, "ffmpeg", "hesplay"); len(got) == 0 || got[0] != "ffmpeg" {
		t.Errorf("an ffmpeg colour should front with ffmpeg, got %v", got)
	}
}
