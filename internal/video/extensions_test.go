package video

import "testing"

func TestIsVideoExt(t *testing.T) {
	tests := []struct {
		ext  string
		want bool
	}{
		{".mkv", true},
		{".MKV", true},
		{".mp4", true},
		{".avi", true},
		{".mov", true},
		{".m2ts", true},
		{".m4v", true},
		{".wmv", true},
		{".ts", true},
		{".webm", true},
		{".mp3", false},
		{".flac", false},
		{".txt", false},
		{"", false},
		{".jpg", false},
	}
	for _, tt := range tests {
		if got := IsVideoExt(tt.ext); got != tt.want {
			t.Fatalf("IsVideoExt(%q) = %v, want %v", tt.ext, got, tt.want)
		}
	}
}
