package music

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/bogem/id3v2/v2"
	"github.com/gcottom/audiometa/v3"
	"github.com/gcottom/flacmeta"
	"github.com/gcottom/mp4meta"
)

// TagWriteFields contains the fields to write into audio file tags.
type TagWriteFields struct {
	Artist, AlbumArtist, Album, Title string
	Year, TrackNo, DiscNo             int
	AlbumMBID, ArtistMBID             string
}

// WriteTrackTags writes metadata tags to an audio file.
// Dispatches by file extension. Returns an error for unsupported formats.
func WriteTrackTags(absPath string, fields TagWriteFields) error {
	ext := strings.ToLower(filepath.Ext(absPath))
	switch ext {
	case ".mp3":
		return writeMP3Tags(absPath, fields)
	case ".flac", ".ogg", ".opus", ".m4a", ".mp4":
		return writeMultiFormatTags(absPath, fields)
	case ".wav", ".aac":
		return fmt.Errorf("tag writing not supported for %s files", ext)
	default:
		return fmt.Errorf("unknown audio format: %s", ext)
	}
}

func writeMP3Tags(absPath string, fields TagWriteFields) error {
	tag, err := id3v2.Open(absPath, id3v2.Options{Parse: true})
	if err != nil {
		return fmt.Errorf("open mp3: %w", err)
	}
	defer tag.Close()

	tag.SetDefaultEncoding(id3v2.EncodingUTF8)

	if fields.Title != "" {
		tag.SetTitle(sanitizeText(fields.Title))
	}
	if fields.Artist != "" {
		tag.SetArtist(sanitizeText(fields.Artist))
	}
	if fields.Album != "" {
		tag.SetAlbum(sanitizeText(fields.Album))
	}
	if fields.AlbumArtist != "" {
		tag.DeleteFrames("TPE2")
		tag.AddFrame("TPE2", id3v2.TextFrame{
			Encoding: id3v2.EncodingUTF8,
			Text:     sanitizeText(fields.AlbumArtist),
		})
	}
	if fields.Year > 0 {
		tag.SetYear(strconv.Itoa(fields.Year))
	}
	if fields.TrackNo > 0 {
		tag.DeleteFrames("TRCK")
		tag.AddFrame("TRCK", id3v2.TextFrame{
			Encoding: id3v2.EncodingUTF8,
			Text:     strconv.Itoa(fields.TrackNo),
		})
	}
	if fields.DiscNo > 0 {
		tag.DeleteFrames("TPOS")
		tag.AddFrame("TPOS", id3v2.TextFrame{
			Encoding: id3v2.EncodingUTF8,
			Text:     strconv.Itoa(fields.DiscNo),
		})
	}

	// MusicBrainz IDs as TXXX frames.
	if fields.AlbumMBID != "" {
		setTXXX(tag, "MusicBrainz Release Group Id", fields.AlbumMBID)
	}
	if fields.ArtistMBID != "" {
		setTXXX(tag, "MusicBrainz Artist Id", fields.ArtistMBID)
	}

	return tag.Save()
}

func setTXXX(tag *id3v2.Tag, desc, value string) {
	// Remove existing TXXX frames with this description.
	existing := tag.GetFrames("TXXX")
	tag.DeleteFrames("TXXX")
	for _, f := range existing {
		if uf, ok := f.(id3v2.UserDefinedTextFrame); ok {
			if strings.EqualFold(uf.Description, desc) {
				continue
			}
		}
		tag.AddFrame("TXXX", f)
	}
	tag.AddFrame("TXXX", id3v2.UserDefinedTextFrame{
		Encoding:    id3v2.EncodingUTF8,
		Description: desc,
		Value:       value,
	})
}

func writeMultiFormatTags(absPath string, fields TagWriteFields) error {
	f, err := os.Open(absPath)
	if err != nil {
		return fmt.Errorf("open file: %w", err)
	}

	tag, err := audiometa.OpenTag(f)
	f.Close()
	if err != nil {
		return fmt.Errorf("open tag: %w", err)
	}

	// Standard fields via the Tag interface.
	if fields.Title != "" {
		tag.SetTitle(sanitizeText(fields.Title))
	}
	if fields.Artist != "" {
		tag.SetArtist(sanitizeText(fields.Artist))
	}
	if fields.Album != "" {
		tag.SetAlbum(sanitizeText(fields.Album))
	}
	if fields.AlbumArtist != "" {
		tag.SetAlbumArtist(sanitizeText(fields.AlbumArtist))
	}
	if fields.TrackNo > 0 {
		tag.SetTrackNumber(fields.TrackNo)
	}
	if fields.DiscNo > 0 {
		tag.SetDiscNumber(fields.DiscNo)
	}

	// Year and format-specific fields via type assertion.
	switch t := tag.(type) {
	case *flacmeta.FLACTag:
		if fields.Year > 0 {
			t.SetDate(strconv.Itoa(fields.Year))
		}
	case *mp4meta.MP4Tag:
		if fields.Year > 0 {
			t.SetYear(fields.Year)
		}
	}
	// OGG/Opus: no year API available in the library.

	// Atomic write: temp file + rename.
	dir := filepath.Dir(absPath)
	tmp, err := os.CreateTemp(dir, ".tagwrite-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()

	if err := tag.Save(tmp); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("save tags: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close temp: %w", err)
	}

	// Preserve original file permissions.
	if info, statErr := os.Stat(absPath); statErr == nil {
		_ = os.Chmod(tmpName, info.Mode())
	}

	if err := os.Rename(tmpName, absPath); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("rename: %w", err)
	}

	return nil
}

// sanitizeText validates UTF-8, normalizes curly quotes, and strips control characters.
func sanitizeText(s string) string {
	if !utf8.ValidString(s) {
		// Replace invalid bytes.
		var b strings.Builder
		b.Grow(len(s))
		for i := 0; i < len(s); {
			r, size := utf8.DecodeRuneInString(s[i:])
			if r == utf8.RuneError && size == 1 {
				i++
				continue
			}
			b.WriteRune(r)
			i += size
		}
		s = b.String()
	}

	// Normalize curly quotes to straight quotes.
	s = strings.NewReplacer(
		"\u2018", "'", // left single
		"\u2019", "'", // right single
		"\u201C", "\"", // left double
		"\u201D", "\"", // right double
	).Replace(s)

	// Strip control characters (except newline and tab).
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			continue
		}
		b.WriteRune(r)
	}

	return strings.TrimSpace(b.String())
}
