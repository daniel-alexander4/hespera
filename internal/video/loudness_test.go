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
	got, err := parseLoudnorm(loudnormOut)
	if err != nil || got != -14.36 {
		t.Fatalf("parseLoudnorm = %v, %v; want -14.36", got, err)
	}

	// Digital silence measures -inf → the R128 gating floor, not an error.
	if got, err := parseLoudnorm(`{"input_i" : "-inf"}`); err != nil || got != -70 {
		t.Fatalf("-inf → %v, %v; want -70", got, err)
	}
	// An exact 0.0 is nudged off the "unanalyzed" sentinel.
	if got, err := parseLoudnorm(`{"input_i" : "0.0"}`); err != nil || got != -0.01 {
		t.Fatalf("0.0 → %v, %v; want -0.01", got, err)
	}
	if _, err := parseLoudnorm("no json here"); err == nil {
		t.Fatal("garbage should error")
	}
	if _, err := parseLoudnorm(`{"input_i" : "not-a-number"}`); err == nil {
		t.Fatal("unparseable input_i should error")
	}
}
