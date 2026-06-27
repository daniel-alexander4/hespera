package music

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/dhowden/tag"
)

// jpegSample is a tiny byte slice that http.DetectContentType recognizes as JPEG.
var jpegSample = []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0x01}

// --- synthetic ID3v2 builders (test-only) ---

func id3Tag(version byte, body []byte) []byte {
	sz := len(body)
	hdr := []byte{'I', 'D', '3', version, 0, 0,
		byte((sz >> 21) & 0x7F), byte((sz >> 14) & 0x7F), byte((sz >> 7) & 0x7F), byte(sz & 0x7F)}
	return append(hdr, body...)
}

func v23Frame(id string, payload []byte) []byte {
	sz := len(payload)
	hdr := append([]byte(id), byte(sz>>24), byte(sz>>16), byte(sz>>8), byte(sz), 0, 0)
	return append(hdr, payload...)
}

func v22Frame(id string, payload []byte) []byte {
	sz := len(payload)
	hdr := append([]byte(id), byte(sz>>16), byte(sz>>8), byte(sz))
	return append(hdr, payload...)
}

func v23Text(id, val string) []byte {
	return v23Frame(id, append([]byte{0}, []byte(val)...)) // enc 0 = ISO-8859-1
}

// apicPayload builds an APIC payload. descWithTerm must include the encoding's
// terminator (single 0x00 for latin1/UTF-8, 0x00 0x00 for UTF-16).
func apicPayload(enc byte, mime string, picType byte, descWithTerm, data []byte) []byte {
	p := []byte{enc}
	p = append(p, []byte(mime)...)
	p = append(p, 0) // MIME is ISO-8859-1, single NUL
	p = append(p, picType)
	p = append(p, descWithTerm...)
	return append(p, data...)
}

func picPayload(enc byte, format string, picType byte, descWithTerm, data []byte) []byte {
	p := []byte{enc}
	p = append(p, []byte(format)...) // 3-char format
	p = append(p, picType)
	p = append(p, descWithTerm...)
	return append(p, data...)
}

func TestExtractID3v2Picture(t *testing.T) {
	tests := []struct {
		name     string
		raw      []byte
		wantMIME string
		wantData []byte
	}{
		{
			name:     "v2.3 APIC latin1 desc",
			raw:      id3Tag(3, v23Frame("APIC", apicPayload(0, "image/jpeg", 0x03, []byte("cover\x00"), jpegSample))),
			wantMIME: "image/jpeg",
			wantData: jpegSample,
		},
		{
			name:     "v2.3 APIC empty UTF-16 desc (double NUL)",
			raw:      id3Tag(3, v23Frame("APIC", apicPayload(1, "image/jpeg", 0x03, []byte{0x00, 0x00}, jpegSample))),
			wantMIME: "image/jpeg",
			wantData: jpegSample,
		},
		{
			name:     "v2.2 PIC JPG",
			raw:      id3Tag(2, v22Frame("PIC", picPayload(0, "JPG", 0x03, []byte{0x00}, jpegSample))),
			wantMIME: "image/jpeg",
			wantData: jpegSample,
		},
		{
			name: "front cover preferred over other picture",
			raw: id3Tag(3, append(
				v23Frame("APIC", apicPayload(0, "image/png", 0x02, []byte{0x00}, []byte("OTHER-IMAGE"))),
				v23Frame("APIC", apicPayload(0, "image/jpeg", 0x03, []byte{0x00}, jpegSample))...,
			)),
			wantMIME: "image/jpeg",
			wantData: jpegSample,
		},
		{
			name:     "no picture frame",
			raw:      id3Tag(3, v23Text("TALB", "An Album")),
			wantMIME: "",
			wantData: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mime, data := extractID3v2Picture(tc.raw)
			if mime != tc.wantMIME {
				t.Errorf("mime = %q, want %q", mime, tc.wantMIME)
			}
			if !bytes.Equal(data, tc.wantData) {
				t.Errorf("data = %v, want %v", data, tc.wantData)
			}
		})
	}
}

// TestReadTrackMetaMP3FallbackExtractsArt verifies the fallback (used when
// dhowden/tag rejects a file) now recovers the embedded cover alongside the text
// frames, deterministically — it calls the fallback directly.
func TestReadTrackMetaMP3FallbackExtractsArt(t *testing.T) {
	body := bytes.Join([][]byte{
		v23Text("TPE1", "Jimmie Rodgers"),
		v23Text("TALB", "Kisses Sweeter Than Honeycomb"),
		v23Frame("APIC", apicPayload(0, "image/jpeg", 0x03, []byte("cover\x00"), jpegSample)),
	}, nil)
	path := filepath.Join(t.TempDir(), "x.mp3")
	if err := os.WriteFile(path, id3Tag(3, body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	meta, err := readTrackMetaMP3Fallback(path)
	if err != nil {
		t.Fatalf("readTrackMetaMP3Fallback: %v", err)
	}
	if !meta.HasArt {
		t.Fatal("HasArt = false, want true")
	}
	if meta.ArtMIME != "image/jpeg" {
		t.Errorf("ArtMIME = %q, want image/jpeg", meta.ArtMIME)
	}
	if !bytes.Equal(meta.ArtBytes, jpegSample) {
		t.Errorf("ArtBytes = %v, want %v", meta.ArtBytes, jpegSample)
	}
	if meta.Artist != "Jimmie Rodgers" || meta.Album != "Kisses Sweeter Than Honeycomb" {
		t.Errorf("text frames not parsed: artist=%q album=%q", meta.Artist, meta.Album)
	}
}

// TestReadTrackMetaRecoversArtWhenDhowdenFails is the end-to-end case: a
// malformed odd-length UTF-16 text frame makes dhowden/tag abort, and
// ReadTrackMeta must still surface the embedded cover via the fallback.
func TestReadTrackMetaRecoversArtWhenDhowdenFails(t *testing.T) {
	body := bytes.Join([][]byte{
		v23Text("TPE1", "Jimmie Rodgers"),
		v23Text("TALB", "Kisses Sweeter Than Honeycomb"),
		// A plain text frame (genre) with UTF-16 encoding (enc 2) and an odd
		// number of text bytes -> dhowden/tag aborts the whole parse, exactly like
		// the real files.
		v23Frame("TCON", []byte{0x02, 0x41, 0x42, 0x43}),
		v23Frame("APIC", apicPayload(0, "image/jpeg", 0x03, []byte("cover\x00"), jpegSample)),
	}, nil)
	blob := id3Tag(3, body)

	if _, err := tag.ReadFrom(bytes.NewReader(blob)); err == nil {
		t.Skip("dhowden/tag accepted the blob; fallback not exercised by this fixture")
	}

	path := filepath.Join(t.TempDir(), "y.mp3")
	if err := os.WriteFile(path, blob, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	meta, err := ReadTrackMeta(path)
	if err != nil {
		t.Fatalf("ReadTrackMeta: %v", err)
	}
	if !meta.HasArt || !bytes.Equal(meta.ArtBytes, jpegSample) {
		t.Fatalf("art not recovered via fallback: HasArt=%v bytes=%d", meta.HasArt, len(meta.ArtBytes))
	}
}
