package video

import "testing"

const loudnormOut = `size=N/A time=00:03:20.00 bitrate=N/A speed= 312x
[Parsed_loudnorm_0 @ 0x5c] 
{
	"input_i" : "-14.36",
	"input_tp" : "-0.42",
	"input_lra" : "9.70",
	"input_thresh" : "-24.65",
	"output_i" : "-24.03",
	"target_offset" : "0.03"
}
`

func TestParseLoudnorm(t *testing.T) {
	lufs, tp, err := parseLoudnorm(loudnormOut)
	if err != nil || lufs != -14.36 || tp != -0.42 {
		t.Fatalf("parseLoudnorm = %v, %v, %v; want -14.36, -0.42", lufs, tp, err)
	}

	// Digital silence measures -inf → the R128 gating floor, not an error.
	if lufs, tp, err := parseLoudnorm(`{"input_i" : "-inf", "input_tp" : "-inf"}`); err != nil || lufs != -70 || tp != -70 {
		t.Fatalf("-inf → %v, %v, %v; want -70, -70", lufs, tp, err)
	}
	// An exact 0.0 is nudged off the "unanalyzed" sentinel — a real brickwalled
	// master does measure a true peak of exactly 0.00 dBTP.
	if lufs, tp, err := parseLoudnorm(`{"input_i" : "0.0", "input_tp" : "0.00"}`); err != nil || lufs != -0.01 || tp != -0.01 {
		t.Fatalf("0.0 → %v, %v, %v; want -0.01, -0.01", lufs, tp, err)
	}
	// A true peak above full scale is a real reading, not an error.
	if _, tp, err := parseLoudnorm(`{"input_i" : "-8.0", "input_tp" : "0.43"}`); err != nil || tp != 0.43 {
		t.Fatalf("+0.43 dBTP → %v, %v; want 0.43", tp, err)
	}
	if _, _, err := parseLoudnorm("no json here"); err == nil {
		t.Fatal("garbage should error")
	}
	if _, _, err := parseLoudnorm(`{"input_i" : "not-a-number", "input_tp" : "-1.0"}`); err == nil {
		t.Fatal("unparseable input_i should error")
	}
	// A missing input_tp must error rather than store 0 — 0 is the "unanalyzed"
	// sentinel, and a row written with it would be re-analyzed on every sweep.
	if _, _, err := parseLoudnorm(`{"input_i" : "-14.0"}`); err == nil {
		t.Fatal("missing input_tp should error")
	}
}
