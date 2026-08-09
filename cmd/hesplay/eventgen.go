package main

// The event generator: the one sound source in this engine that hesplay
// synthesizes itself rather than asking SoX or ffmpeg for.
//
// It exists because everything else here is CONTINUOUS. A banded noise with a
// slow tremolo can be a sea, a wind or a bed, but it can never be a rain drop, a
// crackle or a distant thump — those are discrete events at irregular times, and
// neither SoX's `synth` nor ffmpeg's `anoisesrc` has any notion of "fire a short
// sound at random intervals". SoX can make one transient; it cannot schedule a
// thousand of them, and a fixed pattern looped by `repeat` would be heard as the
// loop it is.
//
// So the generator writes raw PCM into the same per-stage pipe an ffmpeg colour
// uses, and SoX shapes it exactly like any other layer: band-pass, reverb, gain.
// Two properties of that arrangement are load-bearing and were measured before it
// was written:
//
//   - SoX reads our raw int16 stream and shapes it normally.
//   - `play -m` runs to its LONGEST input, so a stage that ends early does not
//     stop the mix — it goes quiet. A generator that dies therefore loses its
//     events while the bed plays on, silently, which is why the runner warns when
//     a generator exits before the mixer does and why this code counts what it
//     emitted.
//
// It runs as a hidden `noise-events` verb — hesplay exec'ing itself, the same
// shape as the `complete` verb backing shell completion. That is not ceremony: it
// reuses the runner's existing generator-process plumbing untouched, and it keeps
// `noise --print` paste-able, which is a property this engine deliberately holds.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"os"
	"strconv"
	"strings"
)

const (
	// eventChunkSamples is how many frames are built per write. Big enough that
	// the syscall cost is nothing, small enough that a stop is prompt.
	eventChunkSamples = 4096

	// eventTailFactor is how many time constants of an envelope are worth
	// emitting. e^-6 is 0.25%, inaudible under anything, and it bounds how many
	// events can be alive at once.
	eventTailFactor = 6
)

// eventVoice is one live transient: a noise burst with an exponential decay and a
// fixed stereo position.
type eventVoice struct {
	env   float64 // current amplitude
	left  float64 // 0..1 pan weight for the left channel
	left2 float64
	rem   int // frames remaining before it is dropped
}

// eventGenSpec is what the verb parses and the generator plays.
type eventGenSpec struct {
	Rate    float64 // events per second (Poisson mean)
	DecayMs float64 // one time constant of the envelope
	Amp     float64 // peak amplitude of an average event, 0..1
	Seed    int64   // 0 = seed from the clock; anything else is reproducible
}

// eventGenStats is what the generator reports once, at exit — never per event.
// The sample loop runs 48 000 times a second and must not allocate, format, or
// read a clock, so these are plain ints incremented in scope.
type eventGenStats struct {
	Events  int
	Clipped int
	Frames  int
	Seed    int64 // the seed actually used, so a session can be reproduced
}

// resolveSeed turns a zero into a per-process seed and leaves anything else alone,
// so the value printed at start and the value used are the same one. A fixed
// default would make every session identical, which is the one thing an event
// layer exists to avoid.
func resolveSeed(seed int64) int64 {
	if seed == 0 {
		return int64(os.Getpid())*2654435761 + 1
	}
	return seed
}

// generateEvents writes interleaved 16-bit stereo PCM until the context ends or
// the reader hangs up. Poisson arrivals: the gap to the next event is
// -ln(U)/rate, which is what makes rain sound like rain rather than a metronome —
// events cluster and gap the way independent ones do.
func generateEvents(ctx context.Context, w io.Writer, spec eventGenSpec) (eventGenStats, error) {
	var st eventGenStats
	if spec.Rate <= 0 {
		return st, errors.New("events per second must be positive")
	}
	seed := resolveSeed(spec.Seed)
	st.Seed = seed
	rnd := rand.New(rand.NewSource(seed))

	tau := spec.DecayMs / 1000 * noiseSampleRate
	if tau < 1 {
		tau = 1
	}
	decay := math.Exp(-1 / tau)
	life := int(tau * eventTailFactor)
	// Amplitude is scaled by how many events are expected to overlap, so density
	// alone cannot drive the sum into the clipper: raising the rate makes rain
	// denser, not louder.
	overlap := math.Max(1, spec.Rate*float64(life)/noiseSampleRate)
	peak := spec.Amp / math.Sqrt(overlap)

	voices := make([]eventVoice, 0, 64)
	buf := make([]byte, 0, eventChunkSamples*4)
	bw := bufio.NewWriterSize(w, eventChunkSamples*8)
	nextIn := 0.0 // frames until the next arrival

	for {
		if err := ctx.Err(); err != nil {
			bw.Flush()
			return st, nil
		}
		buf = buf[:0]
		for i := 0; i < eventChunkSamples; i++ {
			if nextIn <= 0 {
				gap := -math.Log(math.Max(rnd.Float64(), 1e-12)) / spec.Rate * noiseSampleRate
				nextIn += math.Max(1, gap)
				// 0.4..1.0 of peak: real drops are not identical, and the spread
				// is most of what stops a dense stream reading as hiss.
				a := peak * (0.4 + 0.6*rnd.Float64())
				pan := rnd.Float64()
				voices = append(voices, eventVoice{env: a, left: 1 - pan, left2: pan, rem: life})
				st.Events++
			}
			nextIn--

			var l, r float64
			live := voices[:0]
			for _, v := range voices {
				n := rnd.Float64()*2 - 1 // the burst itself: white, shaped by env
				l += n * v.env * v.left
				r += n * v.env * v.left2
				v.env *= decay
				v.rem--
				if v.rem > 0 {
					live = append(live, v)
				}
			}
			voices = live

			li, lc := clampSample(l)
			ri, rc := clampSample(r)
			st.Clipped += lc + rc
			buf = append(buf,
				byte(li), byte(li>>8),
				byte(ri), byte(ri>>8))
			st.Frames++
		}
		if _, err := bw.Write(buf); err != nil {
			// A closed pipe is how this ends normally: the mixer went away, so
			// the generator has nothing left to feed.
			bw.Flush()
			return st, nil
		}
	}
}

// clampSample converts to int16 and reports whether it had to clamp. Clipping
// here would be silent distortion, so it is counted rather than hoped about.
func clampSample(v float64) (int16, int) {
	s := v * 32767
	switch {
	case s > 32767:
		return 32767, 1
	case s < -32768:
		return -32768, 1
	}
	return int16(s), 0
}

// eventGenArgs is the argv the runner (and `--print`) use to invoke this verb, so
// the printed pipeline and the played one can never disagree.
func eventGenArgs(l noiseLayer) []string {
	return []string{"noise-events",
		"--rate", num(l.EventsPerSec),
		"--decay", num(l.DecayMs),
	}
}

// cmdNoiseEvents is the hidden `noise-events` verb. Hidden for the same reason
// `complete` is: it exists so another program can call it — here hesplay itself —
// and a user has no reason to run it except to reproduce a printed pipeline.
func cmdNoiseEvents(ctx context.Context, args []string) error {
	spec := eventGenSpec{Rate: 40, DecayMs: 12, Amp: 0.5}
	for i := 0; i < len(args); i++ {
		val := func() (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s: expected a value", args[i])
			}
			i++
			return args[i], nil
		}
		// No help branch: dispatch intercepts every help form in argv[1] before this
		// verb is reached, so one here would be unreachable code pretending to be a
		// feature. A hidden verb's usage is the printed pipeline it appears in.
		switch a := args[i]; {
		case a == "--rate", a == "--decay", a == "--amp", a == "--seed":
			s, err := val()
			if err != nil {
				return err
			}
			f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
			if err != nil {
				return fmt.Errorf("%s: %q is not a number", a, s)
			}
			switch a {
			case "--rate":
				spec.Rate = f
			case "--decay":
				spec.DecayMs = f
			case "--amp":
				spec.Amp = f
			case "--seed":
				spec.Seed = int64(f)
			}
		default:
			return fmt.Errorf("noise-events: unknown argument %q", a)
		}
	}
	// Announced at START, not only at exit: in a mix the runner SIGKILLs generators
	// on teardown, so anything printed on the way out is lost. This line is what
	// says the layer began and at what rate — and echoing the seed is what makes a
	// session you liked reproducible (`--seed` replays it).
	spec.Seed = resolveSeed(spec.Seed)
	fmt.Fprintf(os.Stderr, "noise-events: %g/s, %gms decay, seed %d\n", spec.Rate, spec.DecayMs, spec.Seed)
	st, err := generateEvents(ctx, os.Stdout, spec)
	// Reported once, on stderr, so it cannot corrupt the PCM on stdout — and so a
	// silent layer can be told apart from a sparse one after the fact.
	fmt.Fprintf(os.Stderr, "noise-events: %d events, %d frames, %d clipped\n", st.Events, st.Frames, st.Clipped)
	return err
}
