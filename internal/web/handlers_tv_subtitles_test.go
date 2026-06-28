package web

import "testing"

func TestIsOpenSubtitlesHost(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"https://www.opensubtitles.com/download/abc/sub.srt", true},
		{"https://opensubtitles.com/x", true},
		{"https://cdn.opensubtitles.com/file", true},
		{"http://www.opensubtitles.com/x", false},       // not https
		{"https://opensubtitles.com.evil.com/x", false}, // suffix trick
		{"https://evil.com/opensubtitles.com", false},
		{"https://api.opensubtitles.org/x", false}, // .org, not .com
		{"not a url at all ::::", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isOpenSubtitlesHost(tt.url); got != tt.want {
			t.Errorf("isOpenSubtitlesHost(%q) = %v, want %v", tt.url, got, tt.want)
		}
	}
}

func TestNormalizeLang(t *testing.T) {
	tests := []struct{ in, want string }{
		{"en", "en"},
		{"EN", "en"},
		{" pt-br ", "pt-br"},
		{"", "en"},
		{"english", "en"}, // not a code → default
		{"../etc", "en"},  // path-traversal attempt → default
		{"e", "en"},       // too short → default
		{"zho", "zho"},
	}
	for _, tt := range tests {
		if got := normalizeLang(tt.in); got != tt.want {
			t.Errorf("normalizeLang(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestSubtitleGetURL(t *testing.T) {
	if got := subtitleGetURL(123, "en"); got != "/tv/subtitles/get?file_id=123&lang=en" {
		t.Errorf("subtitleGetURL = %q", got)
	}
}
