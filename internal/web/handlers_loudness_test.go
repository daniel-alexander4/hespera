package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

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
		got := levelGainDB(c.lufs, c.tp)
		if diff := got - c.want; diff > 0.001 || diff < -0.001 {
			t.Fatalf("%s: levelGainDB(%v, %v) = %v, want %v", c.name, c.lufs, c.tp, got, c.want)
		}
	}
}

// The whole point of the peak cap: no track, however quiet, is ever boosted to a
// peak above the ceiling. Swept across the real range of both measurements.
func TestLevelGainNeverClips(t *testing.T) {
	for lufs := -30.0; lufs <= -3.0; lufs += 0.1 {
		for tp := -20.0; tp <= 1.0; tp += 0.1 {
			if peak := tp + levelGainDB(lufs, tp); peak > truePeakCeilingDBTP+0.001 && peak > tp+0.001 {
				t.Fatalf("lufs=%.1f tp=%.1f → gain %.2f lifts the peak to %.2f dBTP (ceiling %.1f)",
					lufs, tp, levelGainDB(lufs, tp), peak, truePeakCeilingDBTP)
			}
		}
	}
}

// The queue JSON carries each track's leveling gain, computed from the stored
// loudness and true-peak measurements: -20 LUFS wants a +6 dB lift toward the
// -14 target, and its -8 dBTP peak has the 7 dB of headroom to take it.
func TestMusicQueueCarriesGain(t *testing.T) {
	h, db := newTestHandler(t)
	router := h.Router()
	_, _, albumID, trackID := seedMusicData(t, db)
	if _, err := db.Exec("UPDATE music_tracks SET loudness_lufs=-20.0, loudness_tp=-8.0 WHERE id=?", trackID); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/music/queue?album=%d", albumID), nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("queue: %d — %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Tracks []struct {
			ID     int64   `json:"id"`
			GainDB float64 `json:"gainDb"`
		} `json:"tracks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("parse queue json: %v", err)
	}
	if len(out.Tracks) != 1 || out.Tracks[0].GainDB != 6.0 {
		t.Fatalf("expected gainDb=+6 for -20 LUFS at -8 dBTP, got %+v", out.Tracks)
	}
}
