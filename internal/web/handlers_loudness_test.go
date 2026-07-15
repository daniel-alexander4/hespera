package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

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
