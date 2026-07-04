package video

import (
	"reflect"
	"testing"
)

// TestFrameGrabLadder pins the fallback sequence: the requested offset first,
// then toward the start — never a duplicate, never skipping the from-zero try.
func TestFrameGrabLadder(t *testing.T) {
	tests := []struct {
		seek float64
		want []float64
	}{
		{250, []float64{250, 1, 0}},
		{1, []float64{1, 0}},
		{0.5, []float64{0.5, 0}},
		{0, []float64{0}},
	}
	for _, tc := range tests {
		if got := frameGrabLadder(tc.seek); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("frameGrabLadder(%v) = %v, want %v", tc.seek, got, tc.want)
		}
	}
}
