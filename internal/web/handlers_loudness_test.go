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
		lufs, want float64
	}{
		{0, 0},   // unanalyzed → no gain
		{-18, 0}, // already at target
		{-14.36, -3.64},
		{-28, 10}, // quiet track boosted
		{-40, 12}, // clamped up
		{-2, -12}, // clamped down
	}
	for _, c := range cases {
		got := levelGainDB(c.lufs)
		if diff := got - c.want; diff > 0.001 || diff < -0.001 {
			t.Fatalf("levelGainDB(%v) = %v, want %v", c.lufs, got, c.want)
		}
	}
}

// The queue JSON carries each track's leveling gain, computed from the stored
// loudness measurement.
func TestMusicQueueCarriesGain(t *testing.T) {
	h, db := newTestHandler(t)
	router := h.Router()
	_, _, albumID, trackID := seedMusicData(t, db)
	if _, err := db.Exec("UPDATE music_tracks SET loudness_lufs=-14.0 WHERE id=?", trackID); err != nil {
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
	if len(out.Tracks) != 1 || out.Tracks[0].GainDB != -4.0 {
		t.Fatalf("expected gainDb=-4 for -14 LUFS, got %+v", out.Tracks)
	}
}
