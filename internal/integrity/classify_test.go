package integrity

import (
	"strings"
	"testing"
)

// TestClassify pins the flagged/degraded split: decode errors or a failed
// container repair are unplayable-grade damage (flagged); an audio gap alone
// on a sound container is playable residue (degraded).
func TestClassify(t *testing.T) {
	cases := []struct {
		name                                    string
		status, detail, gapDetail, decodeDetail string
		wantStatus                              string
		wantInDetail                            []string
	}{
		{
			name:   "clean container, no findings",
			status: "ok", wantStatus: "ok",
		},
		{
			name:   "repaired container, no findings",
			status: "repaired", detail: "container remuxed (2 errors dropped)",
			wantStatus:   "repaired",
			wantInDetail: []string{"container remuxed"},
		},
		{
			name:   "gap only on repaired container -> degraded",
			status: "repaired", detail: "container remuxed (6 errors dropped)",
			gapDetail:    "audio gap 3.9s (missing audio)",
			wantStatus:   "degraded",
			wantInDetail: []string{"container remuxed", "audio gap 3.9s", "silence-fills"},
		},
		{
			name:   "gap only on clean container -> degraded",
			status: "ok", gapDetail: "audio gap 2.0s (missing audio)",
			wantStatus:   "degraded",
			wantInDetail: []string{"audio gap 2.0s", "silence-fills"},
		},
		{
			name:   "decode errors -> flagged",
			status: "ok", decodeDetail: "bitstream corruption (4 decode errors)",
			wantStatus:   "flagged",
			wantInDetail: []string{"bitstream corruption"},
		},
		{
			name:   "gap AND decode errors -> flagged, both named",
			status: "repaired", detail: "container remuxed (2 errors dropped)",
			gapDetail:    "audio gap 1.0s (missing audio)",
			decodeDetail: "bitstream corruption (9 decode errors)",
			wantStatus:   "flagged",
			wantInDetail: []string{"container remuxed", "audio gap 1.0s", "bitstream corruption"},
		},
		{
			name:   "container repair itself failed -> stays flagged even with gap only",
			status: "flagged", detail: "container corruption (repair failed verification)",
			gapDetail:    "audio gap 2.5s (missing audio)",
			wantStatus:   "flagged",
			wantInDetail: []string{"repair failed", "audio gap 2.5s"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, detail := classify(c.status, c.detail, c.gapDetail, c.decodeDetail)
			if status != c.wantStatus {
				t.Fatalf("status = %q, want %q (detail %q)", status, c.wantStatus, detail)
			}
			for _, w := range c.wantInDetail {
				if !strings.Contains(detail, w) {
					t.Fatalf("detail %q missing %q", detail, w)
				}
			}
			if status == "flagged" && strings.Contains(detail, "silence-fills") {
				t.Fatalf("flagged detail carries the degraded suffix: %q", detail)
			}
		})
	}
}
