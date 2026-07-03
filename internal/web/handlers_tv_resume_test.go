package web

import (
	"testing"
)

func TestParseStartParam(t *testing.T) {
	tests := []struct {
		raw      string
		duration float64
		want     float64
	}{
		{"", 0, 0},
		{"abc", 100, 0},
		{"-5", 100, 0},
		{"0", 100, 0},
		{"100", 200, 100},
		{"300", 200, 199}, // clamp to duration-1
		{"100", 0, 100},   // unknown duration: no upper clamp
	}
	for _, tt := range tests {
		if got := parseStartParam(tt.raw, tt.duration); got != tt.want {
			t.Errorf("parseStartParam(%q, %v) = %v, want %v", tt.raw, tt.duration, got, tt.want)
		}
	}
}
