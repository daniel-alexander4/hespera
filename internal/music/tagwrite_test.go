package music

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSanitizeText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "Hello World", "Hello World"},
		{"curly single quotes", "it\u2018s a test\u2019s", "it's a test's"},
		{"curly double quotes", "\u201CHello\u201D", "\"Hello\""},
		{"control chars stripped", "Hello\x00\x01World", "HelloWorld"},
		{"tabs preserved", "Hello\tWorld", "Hello\tWorld"},
		{"newlines preserved", "Hello\nWorld", "Hello\nWorld"},
		{"leading trailing space", "  Hello  ", "Hello"},
		{"empty string", "", ""},
		{"mixed curly quotes", "\u201CIt\u2019s\u201D", "\"It's\""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeText(tt.in)
			if got != tt.want {
				t.Fatalf("sanitizeText(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestWriteTrackTagsUnsupported(t *testing.T) {
	for _, ext := range []string{".wav", ".aac"} {
		t.Run(ext, func(t *testing.T) {
			err := WriteTrackTags("/fake/file"+ext, TagWriteFields{Title: "test"})
			if err == nil {
				t.Fatal("expected error for unsupported format")
			}
		})
	}
}

func TestWriteTrackTagsUnknownFormat(t *testing.T) {
	err := WriteTrackTags("/fake/file.xyz", TagWriteFields{Title: "test"})
	if err == nil {
		t.Fatal("expected error for unknown format")
	}
}

func TestWriteTrackTagsMissingFile(t *testing.T) {
	err := WriteTrackTags("/nonexistent/path/test.mp3", TagWriteFields{Title: "test"})
	if err == nil {
		t.Fatal("expected error for missing mp3 file")
	}
}

func TestWriteTrackTagsFlacPermissionError(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "test.flac")
	if err := os.WriteFile(p, []byte("not a real flac"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := WriteTrackTags(p, TagWriteFields{Title: "test"})
	if err == nil {
		t.Fatal("expected error for invalid flac file")
	}
}
