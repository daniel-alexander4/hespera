package music

import "testing"

func TestResolveTrackArtist(t *testing.T) {
	tests := []struct {
		name        string
		artist      string
		albumArtist string
		want        string
	}{
		// The target case: a placeholder track artist + a real album artist.
		{"unknown → real album artist", "Unknown Artist", "Deep Purple", "Deep Purple"},
		{"empty → real album artist", "", "Deep Purple", "Deep Purple"},
		{"unknown (lowercase) → real", "unknown artist", "Deep Purple", "Deep Purple"},
		{"bare 'Unknown' → real", "Unknown", "Pink Floyd", "Pink Floyd"},

		// A real track artist is never overridden.
		{"real artist kept", "Ian Gillan", "Deep Purple", "Ian Gillan"},
		{"real artist kept even if album artist differs", "Glenn Hughes", "Deep Purple", "Glenn Hughes"},

		// No safe target → leave the placeholder as-is.
		{"unknown + empty album artist", "Unknown Artist", "", "Unknown Artist"},
		{"unknown + unknown album artist", "Unknown Artist", "Unknown Artist", "Unknown Artist"},
		{"unknown + Various Artists album artist (genuine comp)", "Unknown Artist", "Various Artists", "Unknown Artist"},
		{"unknown + VA album artist", "Unknown Artist", "VA", "Unknown Artist"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveTrackArtist(tt.artist, tt.albumArtist); got != tt.want {
				t.Errorf("resolveTrackArtist(%q, %q) = %q, want %q", tt.artist, tt.albumArtist, got, tt.want)
			}
		})
	}
}
