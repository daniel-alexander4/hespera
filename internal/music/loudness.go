package music

import "math"

// Volume-leveling policy. It lives here, not in the web handler, because two
// callers need it and they can't see each other: the web layer computes the
// per-track gain it hands the player, and the scanner's true-peak backfill needs
// LoudnessTargetLUFS to know which rows could ever be boosted (web imports scan,
// so scan cannot import web — a second copy of the target would be a second
// source of truth, and the two would drift).
const (
	// LoudnessTargetLUFS is the loudness every track is leveled toward — the
	// streaming reference (Spotify/YouTube/Tidal). Deliberately hotter than the
	// -18 LUFS ReplayGain reference: -18 with no pre-amp attenuated 97% of a real
	// library by a mean 5.9 dB, and at 6-10 dB down the ear sheds bass and treble
	// before mids (ISO 226), so a correctly-leveled library read as thin.
	LoudnessTargetLUFS = -14.0
	// TruePeakCeilingDBTP is the peak a boosted track may never exceed. Nothing
	// downstream renders a signal past full scale faithfully — Web Audio's
	// destination clips it — so a boost is only ever as large as the track's own
	// headroom. The 1 dB margin is the usual inter-sample allowance for lossy
	// codecs, whose decoded peaks overshoot the encoded samples.
	TruePeakCeilingDBTP = -1.0
	// LevelGainLimitDB bounds the gain in both directions against a mismeasured
	// outlier. Still load-bearing on the boost side despite the peak cap: digital
	// silence measures -inf LUFS, which the analyzer floors at -70, and that would
	// otherwise request a +56 dB lift of the noise floor with all the headroom in
	// the world to spend on it.
	LevelGainLimitDB = 12.0
)

// LevelGainDB converts a track's measured loudness and true peak into the
// playback gain (dB) that levels it toward LoudnessTargetLUFS. Attenuation is
// applied as measured; a *boost* is capped at the track's headroom, so lifting a
// quiet track can never push its peak past TruePeakCeilingDBTP. A track whose
// peak is unmeasured (truePeak == 0) is attenuated normally but never boosted,
// since its headroom is unknown — which is also why the backfill can skip
// measuring the peak of any track the target would only ever attenuate
// (see NeedsTruePeak). Both directions are clamped to ±LevelGainLimitDB.
// 0 (unanalyzed) applies no gain.
func LevelGainDB(lufs, truePeak float64) float64 {
	if lufs == 0 {
		return 0
	}
	gain := LoudnessTargetLUFS - lufs
	if gain > 0 {
		headroom := 0.0
		if truePeak != 0 {
			headroom = math.Max(0, TruePeakCeilingDBTP-truePeak)
		}
		gain = math.Min(gain, headroom)
	}
	return math.Max(-LevelGainLimitDB, math.Min(LevelGainLimitDB, gain))
}

// NeedsTruePeak reports whether a track's true peak affects its playback gain at
// all. It does so only when the track would be *boosted* (a cut can never clip,
// so LevelGainDB never reads the peak of a track at or above the target). The
// backfill uses this to skip decoding tracks whose peak it would measure and then
// never read — on a real library that is ~63% of it, and the skipped rows behave
// identically either way (an unmeasured peak already means "attenuate, never
// boost", which is exactly what they do).
func NeedsTruePeak(lufs float64) bool {
	return lufs < LoudnessTargetLUFS
}
