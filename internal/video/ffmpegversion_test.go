package video

import "testing"

func TestParseFFmpegVersion(t *testing.T) {
	tests := []struct {
		name      string
		out       string
		wantVer   string
		wantMajor int
	}{
		{"release", "ffmpeg version 7.1.1 Copyright (c) 2000-2025\n", "7.1.1", 7},
		{"distro", "ffmpeg version 6.1.1-3ubuntu5 Copyright\n", "6.1.1-3ubuntu5", 6},
		{"ubuntu 4.4", "ffmpeg version 4.4.2-0ubuntu0.22.04.1 Copyright\n", "4.4.2-0ubuntu0.22.04.1", 4},
		{"n-prefix", "ffmpeg version n7.1 Copyright\n", "n7.1", 7},
		{"git snapshot", "ffmpeg version N-109421-g abc Copyright\n", "N-109421-g", 0},
		{"empty", "", "", 0},
		{"no version line", "some other output\n", "", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ver := parseFFmpegVersion(tc.out)
			if ver != tc.wantVer {
				t.Fatalf("parseFFmpegVersion = %q, want %q", ver, tc.wantVer)
			}
			if got := majorOf(ver); got != tc.wantMajor {
				t.Fatalf("majorOf(%q) = %d, want %d", ver, got, tc.wantMajor)
			}
		})
	}
}
