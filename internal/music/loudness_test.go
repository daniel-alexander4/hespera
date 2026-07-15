package music

import "testing"

func TestLevelGainDB(t *testing.T) {
	cases := []struct {
		name           string
		lufs, tp, want float64
	}{
		{"unanalyzed", 0, 0, 0},
		{"already at target", -14, -3, 0},
		{"loud master attenuated", -8, 0.06, -6},
		{"attenuation ignores the peak", -8, -0.5, -6}, // a cut can never clip
		{"quiet track boosted into its headroom", -19.4, -4.22, 3.22},
		{"boost capped by the peak", -17.4, -1.20, 0.2},
		{"boost fully eaten — no headroom left", -16.6, 0.06, 0},
		{"boost eaten by a peak already past full scale", -16.2, 0.43, 0},
		{"unmeasured peak → attenuate, never boost", -20, 0, 0},
		{"unmeasured peak still attenuates", -6, 0, -8},
		{"clamped up", -40, -30, 12},
		{"clamped down", -1, -0.1, -12},
		{"digital silence is not amplified without limit", -70, -70, 12},
	}
	for _, c := range cases {
		got := LevelGainDB(c.lufs, c.tp)
		if diff := got - c.want; diff > 0.001 || diff < -0.001 {
			t.Fatalf("%s: LevelGainDB(%v, %v) = %v, want %v", c.name, c.lufs, c.tp, got, c.want)
		}
	}
}

// The whole point of the peak cap: no track, however quiet, is ever boosted to a
// peak above the ceiling. Swept across the real range of both measurements.
func TestLevelGainNeverClips(t *testing.T) {
	for lufs := -30.0; lufs <= -3.0; lufs += 0.1 {
		for tp := -20.0; tp <= 1.0; tp += 0.1 {
			if peak := tp + LevelGainDB(lufs, tp); peak > TruePeakCeilingDBTP+0.001 && peak > tp+0.001 {
				t.Fatalf("lufs=%.1f tp=%.1f → gain %.2f lifts the peak to %.2f dBTP (ceiling %.1f)",
					lufs, tp, LevelGainDB(lufs, tp), peak, TruePeakCeilingDBTP)
			}
		}
	}
}

// The backfill skips measuring the true peak of any track the target would only
// ever attenuate. That is only sound if such a track's gain is IDENTICAL with and
// without a measured peak — otherwise the optimization would silently change
// playback. Assert exactly that across the whole attenuated range, for every peak
// a real file could carry (including clipped masters above full scale).
func TestSkippedTracksGainIsUnaffectedByTheirPeak(t *testing.T) {
	for lufs := LoudnessTargetLUFS; lufs <= -1.0; lufs += 0.1 {
		if NeedsTruePeak(lufs) {
			t.Fatalf("lufs=%.1f is at/above the target but NeedsTruePeak says it must be measured", lufs)
		}
		unmeasured := LevelGainDB(lufs, 0)
		for tp := -20.0; tp <= 2.0; tp += 0.1 {
			if got := LevelGainDB(lufs, tp); got != unmeasured {
				t.Fatalf("lufs=%.1f: gain with peak %.2f is %v but %v unmeasured — skipping its measurement would change playback",
					lufs, tp, got, unmeasured)
			}
		}
	}
	// Conversely, a track below the target genuinely needs its peak.
	if !NeedsTruePeak(LoudnessTargetLUFS - 0.1) {
		t.Fatal("a track quieter than the target can be boosted, so its peak must be measured")
	}
}
