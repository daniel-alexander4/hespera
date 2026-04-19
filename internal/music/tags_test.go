package music

import "testing"

func TestIsAudioExt(t *testing.T) {
	for _, ext := range []string{".mp3", ".flac", ".m4a", ".mp4", ".ogg", ".opus", ".wav", ".aac", ".MP3", ".Flac"} {
		if !IsAudioExt(ext) {
			t.Errorf("expected true for %q", ext)
		}
	}
	for _, ext := range []string{".txt", ".jpg", ".avi", "", ".exe"} {
		if IsAudioExt(ext) {
			t.Errorf("expected false for %q", ext)
		}
	}
}

func TestParseFilenameArtistTitle(t *testing.T) {
	tests := []struct {
		input      string
		wantArtist string
		wantTitle  string
	}{
		{"Artist - Song Title", "Artist", "Song Title"},
		{"The Band – Cool Track", "The Band", "Cool Track"},
		{"Someone — Something", "Someone", "Something"},
		{"no delimiter here", "", ""},
		{"A - B", "", ""},
		{"Short - Ok", "Short", "Ok"},
		{"Valid Name - Valid Song", "Valid Name", "Valid Song"},
		{"", "", ""},
	}
	for _, tt := range tests {
		artist, title := ParseFilenameArtistTitle(tt.input)
		if artist != tt.wantArtist || title != tt.wantTitle {
			t.Errorf("ParseFilenameArtistTitle(%q) = (%q, %q), want (%q, %q)",
				tt.input, artist, title, tt.wantArtist, tt.wantTitle)
		}
	}
}

func TestIsGenericCompilationArtist(t *testing.T) {
	for _, name := range []string{"Various Artists", "various artists", "VA", "va", "Various Artist"} {
		if !IsGenericCompilationArtist(name) {
			t.Errorf("expected true for %q", name)
		}
	}
	for _, name := range []string{"The Beatles", "Artist", "", "V.A."} {
		if IsGenericCompilationArtist(name) {
			t.Errorf("expected false for %q", name)
		}
	}
}

func TestParseTruthyString(t *testing.T) {
	for _, v := range []string{"1", "true", "yes", "on", "y", "TRUE", "Yes"} {
		if !parseTruthyString(v) {
			t.Errorf("expected true for %q", v)
		}
	}
	for _, v := range []string{"0", "false", "no", "off", "", "2", "maybe"} {
		if parseTruthyString(v) {
			t.Errorf("expected false for %q", v)
		}
	}
}

func TestParseSlashNumber(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"5", 5},
		{"3/12", 3},
		{"", 0},
		{"0", 0},
		{"-1", 0},
		{"abc", 0},
		{"7/10", 7},
	}
	for _, tt := range tests {
		got := parseSlashNumber(tt.input)
		if got != tt.want {
			t.Errorf("parseSlashNumber(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestArtFileExt(t *testing.T) {
	tests := []struct {
		mime    string
		want    string
		wantErr bool
	}{
		{"image/jpeg", ".jpg", false},
		{"image/jpg", ".jpg", false},
		{"image/png", ".png", false},
		{"image/webp", ".webp", false},
		{"image/gif", "", true},
		{"text/plain", "", true},
	}
	for _, tt := range tests {
		got, err := ArtFileExt(tt.mime)
		if tt.wantErr && err == nil {
			t.Errorf("ArtFileExt(%q) expected error", tt.mime)
		}
		if !tt.wantErr && err != nil {
			t.Errorf("ArtFileExt(%q) unexpected error: %v", tt.mime, err)
		}
		if got != tt.want {
			t.Errorf("ArtFileExt(%q) = %q, want %q", tt.mime, got, tt.want)
		}
	}
}

func TestVerifyImage(t *testing.T) {
	if err := VerifyImage("image/jpeg", nil); err == nil {
		t.Error("expected error for nil data")
	}
	if err := VerifyImage("image/jpeg", []byte{}); err == nil {
		t.Error("expected error for empty data")
	}
	// JPEG magic
	if err := VerifyImage("image/jpeg", []byte{0xFF, 0xD8, 0xFF, 0xE0}); err != nil {
		t.Errorf("unexpected error for JPEG: %v", err)
	}
	// PNG magic
	if err := VerifyImage("image/png", []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}); err != nil {
		t.Errorf("unexpected error for PNG: %v", err)
	}
}

func TestParseDurationStringMS(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"3:30", 210000},
		{"1:00:00", 3600000},
		{"", 0},
		{"180000", 180000},
		{"3.5", 3500},
	}
	for _, tt := range tests {
		got := parseDurationStringMS(tt.input)
		if got != tt.want {
			t.Errorf("parseDurationStringMS(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("", " ", "hello", "world"); got != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
	if got := firstNonEmpty("", ""); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestSynchsafeToInt(t *testing.T) {
	b := []byte{0x00, 0x00, 0x02, 0x01}
	got := synchsafeToInt(b)
	want := 0<<21 | 0<<14 | 2<<7 | 1
	if got != want {
		t.Errorf("synchsafeToInt(%v) = %d, want %d", b, got, want)
	}
}
