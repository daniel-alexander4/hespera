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
	const secs = 4
	w := &stopAfter{n: noiseSampleRate * secs * 4}
	st, err := generateEvents(context.Background(), w, eventGenSpec{Rate: 120, DecayMs: 9, Amp: 0.5, Seed: 7})
	if err != nil {
		t.Fatalf("generateEvents: %v", err)
	}
	want := 120 * secs
	if got := st.Events; math.Abs(float64(got-want))/float64(want) > 0.1 {
		t.Errorf("emitted %d events over %ds, want ~%d (±10%%)", got, secs, want)
	}
	if st.Frames < noiseSampleRate*secs {
		t.Errorf("only %d frames for %ds", st.Frames, secs)
	}
	if st.Clipped != 0 {
		t.Errorf("%d clipped samples at the shipped amplitude — that is silent distortion", st.Clipped)
	}
}

// TestEventGenLevelIsDensityInvariant pins the design property that makes rate a
// TEXTURE knob: each event is scaled by the expected overlap, so turning density
// up makes rain denser, not louder. Without it, a rate change would silently
// become a level change and every preset's calibration would drift with it.
func TestEventGenLevelIsDensityInvariant(t *testing.T) {
	level := func(rate float64) (float64, float64) {
		w := &stopAfter{n: noiseSampleRate * 3 * 4}
		if _, err := generateEvents(context.Background(), w, eventGenSpec{Rate: rate, DecayMs: 9, Amp: 0.5, Seed: 11}); err != nil {
			t.Fatalf("rate %g: %v", rate, err)
		}
		return pcmStats(w.buf.Bytes())
	}
	lo, loPk := level(60)
	hi, hiPk := level(480)
	dB := 20 * math.Log10(hi/lo)
	if math.Abs(dB) > 2 {
		t.Errorf("8x the density moved the level %.2f dB (%.4f -> %.4f); rate must not be a volume knob", dB, lo, hi)
	}
	// And density must not push it into the clipper either.
	if hiPk >= 1.0 || loPk >= 1.0 {
		t.Errorf("peaks reached full scale: %.3f at 60/s, %.3f at 480/s", loPk, hiPk)
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
