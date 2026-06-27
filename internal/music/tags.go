package music

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/dhowden/tag"
)

// AudioExtensions lists supported audio file extensions.
const AudioExtensions = ".mp3,.flac,.m4a,.mp4,.ogg,.opus,.wav,.aac"

// IsAudioExt returns true if ext (including dot) is a supported audio extension.
func IsAudioExt(ext string) bool {
	switch strings.ToLower(ext) {
	case ".mp3", ".flac", ".m4a", ".mp4", ".ogg", ".opus", ".wav", ".aac":
		return true
	default:
		return false
	}
}

type TrackMeta struct {
	Artist      string
	AlbumArtist string
	Album       string
	Title       string
	Year        int
	Track       int
	Disc        int
	DurationMS  int

	IsCompilation          bool
	ExplicitNotCompilation bool

	MIMEType string
	HasArt   bool
	ArtMIME  string
	ArtBytes []byte
}

func ReadTrackMeta(path string) (TrackMeta, error) {
	f, err := os.Open(path)
	if err != nil {
		return TrackMeta{}, err
	}
	defer f.Close()

	m, err := tag.ReadFrom(f)
	if err != nil {
		if strings.EqualFold(filepath.Ext(path), ".mp3") {
			if fallback, ferr := readTrackMetaMP3Fallback(path); ferr == nil {
				return fallback, nil
			}
		}
		return TrackMeta{}, err
	}

	artist := strings.TrimSpace(m.Artist())
	if artist == "" {
		artist = "Unknown Artist"
	}
	albumArtist := parseAlbumArtist(m.Raw())
	if albumArtist == "" {
		albumArtist = artist
	}
	album := strings.TrimSpace(m.Album())
	if album == "" {
		album = "Unknown Album"
	}
	title := strings.TrimSpace(m.Title())
	base := filepath.Base(path)
	baseNoExt := strings.TrimSuffix(base, filepath.Ext(base))
	if title == "" {
		title = baseNoExt
		if title == "" {
			title = "Unknown Title"
		}
	}

	track, _ := m.Track()
	disc, _ := m.Disc()
	if track <= 0 {
		track = parseTrackIndexFromRaw(m.Raw(), []string{"TRCK", "TRACKNUMBER", "TRACK"})
	}
	if disc <= 0 {
		disc = parseTrackIndexFromRaw(m.Raw(), []string{"TPOS", "DISCNUMBER", "DISC"})
	}
	year := m.Year()
	mt := sniffMIME(path)

	explicitNotComp := isExplicitlyNotCompilation(m.Raw())
	meta := TrackMeta{
		Artist:                 artist,
		AlbumArtist:            albumArtist,
		Album:                  album,
		Title:                  title,
		Year:                   year,
		Track:                  track,
		Disc:                   disc,
		MIMEType:               mt,
		DurationMS:             extractDurationMSFromRaw(m.Raw()),
		IsCompilation:          !explicitNotComp && (isCompilationFromRaw(m.Raw()) || strings.EqualFold(strings.TrimSpace(albumArtist), "Various Artists")),
		ExplicitNotCompilation: explicitNotComp,
	}

	if IsGenericCompilationArtist(artist) {
		if parsedArtist, parsedTitle := ParseFilenameArtistTitle(baseNoExt); parsedArtist != "" {
			meta.Artist = parsedArtist
			if strings.TrimSpace(m.Title()) == "" && parsedTitle != "" {
				meta.Title = parsedTitle
			}
		}
	}

	pic := m.Picture()
	if pic != nil && len(pic.Data) > 0 {
		meta.HasArt = true
		meta.ArtMIME = pic.MIMEType
		meta.ArtBytes = pic.Data
		meta.normalizeArt()
	}

	return meta, nil
}

// SetArt attaches embedded cover art and normalizes it (derives the MIME from
// the bytes when missing, enforces the size cap). For out-of-package fallbacks
// that recover art separately from the tag read — e.g. the scanner's ffmpeg
// cover extraction when dhowden/tag rejected a non-MP3 file. An empty mimeType
// is fine; normalizeArt detects it from the bytes.
func (m *TrackMeta) SetArt(mimeType string, data []byte) {
	m.HasArt = len(data) > 0
	m.ArtMIME = mimeType
	m.ArtBytes = data
	m.normalizeArt()
}

// TrackMetaFromTags builds a TrackMeta from a flat, lowercased tag map (as
// produced by video.ProbeTags via ffprobe). It is the cross-container fallback
// for files that dhowden/tag rejects outright: MP3 has its own pure-Go ID3v2
// fallback (readTrackMetaMP3Fallback), and this maps the ffprobe-recovered tag
// dictionary for the rest (FLAC/M4A/OGG/...) through the same
// compilation/album-artist/normalization rules ReadTrackMeta uses. Cover art is
// attached separately by the caller via SetArt.
func TrackMetaFromTags(tags map[string]string, path string) TrackMeta {
	get := func(keys ...string) string {
		for _, k := range keys {
			if v := strings.TrimSpace(tags[k]); v != "" {
				return v
			}
		}
		return ""
	}

	artist := firstNonEmpty(get("artist"), "Unknown Artist")
	albumArtist := firstNonEmpty(get("album_artist", "albumartist", "album artist"), artist)
	album := firstNonEmpty(get("album"), "Unknown Album")

	base := filepath.Base(path)
	baseNoExt := strings.TrimSuffix(base, filepath.Ext(base))
	titleTag := get("title")
	title := titleTag
	if title == "" {
		title = strings.TrimSpace(baseNoExt)
		if title == "" {
			title = "Unknown Title"
		}
	}

	year := 0
	yearRaw := get("date", "year", "originaldate", "originalyear")
	if len(yearRaw) >= 4 {
		yearRaw = yearRaw[:4]
	}
	if yearRaw != "" {
		if n, err := strconv.Atoi(yearRaw); err == nil && n > 0 {
			year = n
		}
	}

	track := parseSlashNumber(get("track", "tracknumber"))
	disc := parseSlashNumber(get("disc", "discnumber"))
	compRaw := get("compilation", "itunescompilation", "cpil")
	explicitNotComp := isExplicitlyNotCompilationString(compRaw)
	isCompilation := !explicitNotComp && (parseTruthyString(compRaw) || strings.EqualFold(albumArtist, "Various Artists"))

	meta := TrackMeta{
		Artist:                 artist,
		AlbumArtist:            albumArtist,
		Album:                  album,
		Title:                  title,
		Year:                   year,
		Track:                  track,
		Disc:                   disc,
		IsCompilation:          isCompilation,
		ExplicitNotCompilation: explicitNotComp,
		MIMEType:               sniffMIME(path),
	}

	if IsGenericCompilationArtist(artist) {
		if parsedArtist, parsedTitle := ParseFilenameArtistTitle(baseNoExt); parsedArtist != "" {
			meta.Artist = parsedArtist
			if titleTag == "" && parsedTitle != "" {
				meta.Title = parsedTitle
			}
		}
	}

	return meta
}

// normalizeArt validates and normalizes the embedded-art fields: it derives the
// MIME from the bytes when the declared type is missing or bogus, and drops art
// that exceeds the 15 MiB cap. Shared by the dhowden/tag path and the MP3
// fallback so both apply the same rules.
func (m *TrackMeta) normalizeArt() {
	if !m.HasArt || len(m.ArtBytes) == 0 {
		m.HasArt = false
		m.ArtBytes = nil
		m.ArtMIME = ""
		return
	}
	pm := strings.ToLower(strings.TrimSpace(strings.Split(m.ArtMIME, ";")[0]))
	if pm == "" || strings.Contains(pm, "(null)") || pm == "image/" || !strings.HasPrefix(pm, "image/") {
		m.ArtMIME = http.DetectContentType(m.ArtBytes)
	} else {
		m.ArtMIME = pm
	}
	if len(m.ArtBytes) > 15*1024*1024 {
		m.HasArt = false
		m.ArtBytes = nil
		m.ArtMIME = ""
	}
}

func readTrackMetaMP3Fallback(path string) (TrackMeta, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return TrackMeta{}, err
	}
	frames, err := parseID3v2TextFrames(raw)
	if err != nil {
		return TrackMeta{}, err
	}

	artist := firstNonEmpty(frames["TPE1"], "Unknown Artist")
	albumArtist := firstNonEmpty(frames["TPE2"], artist)
	album := firstNonEmpty(frames["TALB"], "Unknown Album")
	title := strings.TrimSpace(frames["TIT2"])
	if title == "" {
		base := filepath.Base(path)
		baseNoExt := strings.TrimSuffix(base, filepath.Ext(base))
		title = strings.TrimSpace(baseNoExt)
		if title == "" {
			title = "Unknown Title"
		}
	}

	year := 0
	yearRaw := strings.TrimSpace(firstNonEmpty(frames["TDRC"], frames["TYER"]))
	if len(yearRaw) >= 4 {
		yearRaw = yearRaw[:4]
	}
	if yearRaw != "" {
		if n, err := strconv.Atoi(yearRaw); err == nil && n > 0 {
			year = n
		}
	}

	track := parseSlashNumber(strings.TrimSpace(frames["TRCK"]))
	disc := parseSlashNumber(strings.TrimSpace(frames["TPOS"]))
	explicitNotComp := isExplicitlyNotCompilationString(strings.TrimSpace(frames["TCMP"]))
	isCompilation := !explicitNotComp && (parseTruthyString(strings.TrimSpace(frames["TCMP"])) || strings.EqualFold(strings.TrimSpace(albumArtist), "Various Artists"))

	meta := TrackMeta{
		Artist:                 artist,
		AlbumArtist:            albumArtist,
		Album:                  album,
		Title:                  title,
		Year:                   year,
		Track:                  track,
		Disc:                   disc,
		IsCompilation:          isCompilation,
		ExplicitNotCompilation: explicitNotComp,
		MIMEType:               sniffMIME(path),
	}

	// Recover the embedded cover art too. dhowden/tag aborts the whole parse on
	// a single malformed text frame (e.g. an odd-length UTF-16 TXXX), which is
	// why this fallback runs at all — but the intact APIC/PIC picture frame is
	// still recoverable, so pull it directly rather than losing the cover.
	if mimeType, data := extractID3v2Picture(raw); len(data) > 0 {
		meta.HasArt = true
		meta.ArtMIME = mimeType
		meta.ArtBytes = data
		meta.normalizeArt()
	}

	return meta, nil
}

func parseSlashNumber(v string) int {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if slash := strings.IndexByte(v, '/'); slash > 0 {
		v = strings.TrimSpace(v[:slash])
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// --- ID3v2 binary parser ---

// forEachID3v2Frame walks the frames of an ID3v2.2/2.3/2.4 tag, invoking fn(id,
// payload) for each (id is 3 chars for v2.2, 4 for v2.3/2.4). fn returns true to
// stop the walk early. It returns an error only for a missing/invalid tag
// header; at the frame level it is deliberately tolerant — it stops at the first
// unparseable frame rather than failing. That tolerance is the whole point of
// this hand-rolled path: it recovers what dhowden/tag rejects.
func forEachID3v2Frame(raw []byte, fn func(id string, payload []byte) bool) error {
	if len(raw) < 10 || string(raw[:3]) != "ID3" {
		return errors.New("no id3v2 header")
	}
	version := int(raw[3])
	if version != 2 && version != 3 && version != 4 {
		return errors.New("unsupported id3v2 version")
	}
	tagSize := synchsafeToInt(raw[6:10])
	if tagSize <= 0 || 10+tagSize > len(raw) {
		return errors.New("invalid id3v2 size")
	}
	body := raw[10 : 10+tagSize]

	// v2.2 frames have a 6-byte header (3-char id + 3-byte size); v2.3/2.4 have a
	// 10-byte header (4-char id + 4-byte size + 2-byte flags). v2.4 sizes are
	// synchsafe; v2.3 plain big-endian.
	hdrLen := 10
	idLen := 4
	if version == 2 {
		hdrLen = 6
		idLen = 3
	}
	for pos := 0; pos+hdrLen <= len(body); {
		hdr := body[pos : pos+hdrLen]
		if bytes.Equal(hdr, make([]byte, hdrLen)) {
			break // padding
		}
		id := string(hdr[:idLen])
		if version == 2 {
			if !isFrameIDv22(id) {
				break
			}
		} else if !isFrameID(id) {
			break
		}
		var sz int
		switch {
		case version == 2:
			sz = int(hdr[3])<<16 | int(hdr[4])<<8 | int(hdr[5])
		case version == 4:
			sz = synchsafeToInt(hdr[4:8])
		default:
			sz = int(binary.BigEndian.Uint32(hdr[4:8]))
		}
		pos += hdrLen
		if sz <= 0 || pos+sz > len(body) {
			break
		}
		payload := body[pos : pos+sz]
		pos += sz
		if fn(id, payload) {
			break
		}
	}
	return nil
}

func parseID3v2TextFrames(raw []byte) (map[string]string, error) {
	out := map[string]string{}
	err := forEachID3v2Frame(raw, func(id string, payload []byte) bool {
		// Normalize v2.2 3-char ids to their v2.3/2.4 names; skip non-text and
		// user-defined (TXX/TXXX) frames; keep the first occurrence of each.
		key := id
		switch {
		case len(id) == 3: // v2.2
			if !strings.HasPrefix(id, "T") || id == "TXX" {
				return false
			}
			if mapped := mapID3v22ToV23(id); mapped != "" {
				key = mapped
			}
		default: // v2.3/2.4
			if !strings.HasPrefix(id, "T") || id == "TXXX" {
				return false
			}
		}
		if _, ok := out[key]; ok {
			return false
		}
		if s := decodeID3TextPayload(payload); s != "" {
			out[key] = s
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// extractID3v2Picture returns the embedded cover art (MIME, bytes) from an
// ID3v2 tag's APIC (v2.3/2.4) or PIC (v2.2) frame, preferring a front cover
// (picture type 0x03) over any other embedded image. It returns ("", nil) when
// there is no usable picture frame. Used only by the MP3 fallback: when
// dhowden/tag rejects a file, its art is otherwise lost even though the picture
// frame is intact.
func extractID3v2Picture(raw []byte) (mimeType string, data []byte) {
	var firstMIME string
	var firstData []byte
	_ = forEachID3v2Frame(raw, func(id string, payload []byte) bool {
		var m string
		var picType byte
		var d []byte
		switch id {
		case "APIC": // v2.3 / v2.4
			m, picType, d = parseAPICFrame(payload)
		case "PIC": // v2.2
			m, picType, d = parsePICFrame(payload)
		default:
			return false
		}
		if len(d) == 0 {
			return false
		}
		if picType == 0x03 { // front cover — prefer it, stop early
			firstMIME, firstData = m, d
			return true
		}
		if firstData == nil { // otherwise keep the first picture as a fallback
			firstMIME, firstData = m, d
		}
		return false
	})
	return firstMIME, firstData
}

// parseAPICFrame parses a v2.3/2.4 APIC payload:
// enc(1) | mime(ISO-8859-1, NUL-terminated) | picType(1) | desc(enc-terminated) | image.
func parseAPICFrame(payload []byte) (mimeType string, picType byte, data []byte) {
	if len(payload) < 4 {
		return "", 0, nil
	}
	enc := payload[0]
	rest := payload[1:]
	nul := bytes.IndexByte(rest, 0) // MIME is always ISO-8859-1, single NUL.
	if nul < 0 {
		return "", 0, nil
	}
	mimeType = string(rest[:nul])
	rest = rest[nul+1:]
	if len(rest) < 1 {
		return "", 0, nil
	}
	picType = rest[0]
	rest = rest[1:]
	d := descriptorEnd(rest, enc)
	if d < 0 {
		return "", 0, nil
	}
	return mimeType, picType, rest[d:]
}

// parsePICFrame parses a v2.2 PIC payload:
// enc(1) | format(3 chars, e.g. "JPG") | picType(1) | desc(enc-terminated) | image.
func parsePICFrame(payload []byte) (mimeType string, picType byte, data []byte) {
	if len(payload) < 6 {
		return "", 0, nil
	}
	enc := payload[0]
	format := strings.ToUpper(strings.TrimRight(string(payload[1:4]), "\x00"))
	picType = payload[4]
	rest := payload[5:]
	d := descriptorEnd(rest, enc)
	if d < 0 {
		return "", 0, nil
	}
	switch format {
	case "JPG", "JPEG":
		mimeType = "image/jpeg"
	case "PNG":
		mimeType = "image/png"
	}
	// An empty/unknown mimeType is fine — normalizeArt detects it from the bytes.
	return mimeType, picType, rest[d:]
}

// descriptorEnd returns the offset in b just past the encoding-terminated
// description string (i.e. where the picture data begins), or -1 if unterminated.
// ISO-8859-1/UTF-8 (enc 0/3) terminate with a single 0x00; UTF-16 (enc 1/2)
// terminate with a 0x00 0x00 pair on an even boundary.
func descriptorEnd(b []byte, enc byte) int {
	if enc == 1 || enc == 2 {
		for i := 0; i+1 < len(b); i += 2 {
			if b[i] == 0 && b[i+1] == 0 {
				return i + 2
			}
		}
		return -1
	}
	nul := bytes.IndexByte(b, 0)
	if nul < 0 {
		return -1
	}
	return nul + 1
}

func isFrameID(id string) bool {
	if len(id) != 4 {
		return false
	}
	for _, r := range id {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

func isFrameIDv22(id string) bool {
	if len(id) != 3 {
		return false
	}
	for _, r := range id {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

func mapID3v22ToV23(id string) string {
	switch id {
	case "TT2":
		return "TIT2"
	case "TP1":
		return "TPE1"
	case "TP2":
		return "TPE2"
	case "TAL":
		return "TALB"
	case "TRK":
		return "TRCK"
	case "TPA":
		return "TPOS"
	case "TYE":
		return "TYER"
	case "TCP":
		return "TCMP"
	default:
		return ""
	}
}

func synchsafeToInt(b []byte) int {
	if len(b) < 4 {
		return 0
	}
	return int(b[0]&0x7F)<<21 | int(b[1]&0x7F)<<14 | int(b[2]&0x7F)<<7 | int(b[3]&0x7F)
}

func decodeID3TextPayload(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	enc := payload[0]
	data := payload[1:]
	switch enc {
	case 0:
		return strings.TrimSpace(strings.TrimRight(string(data), "\x00"))
	case 1:
		return decodeUTF16Tolerant(data, true)
	case 2:
		return decodeUTF16Tolerant(data, false)
	case 3:
		s := strings.TrimRight(string(data), "\x00")
		if utf8.ValidString(s) {
			return strings.TrimSpace(s)
		}
		return strings.TrimSpace(strings.ToValidUTF8(s, ""))
	default:
		return strings.TrimSpace(strings.TrimRight(string(data), "\x00"))
	}
}

func decodeUTF16Tolerant(data []byte, withBOM bool) string {
	if len(data) == 0 {
		return ""
	}
	if len(data)%2 == 1 {
		data = data[:len(data)-1]
	}
	if len(data) == 0 {
		return ""
	}
	var order binary.ByteOrder = binary.BigEndian
	if withBOM && len(data) >= 2 {
		if data[0] == 0xFF && data[1] == 0xFE {
			order = binary.LittleEndian
			data = data[2:]
		} else if data[0] == 0xFE && data[1] == 0xFF {
			order = binary.BigEndian
			data = data[2:]
		}
	}
	if len(data)%2 == 1 {
		data = data[:len(data)-1]
	}
	if len(data) == 0 {
		return ""
	}
	u16 := make([]uint16, 0, len(data)/2)
	for i := 0; i+1 < len(data); i += 2 {
		u16 = append(u16, order.Uint16(data[i:i+2]))
	}
	runes := utf16.Decode(u16)
	s := strings.TrimSpace(strings.TrimRight(string(runes), "\x00"))
	return strings.ToValidUTF8(s, "")
}

// --- Tag field helpers ---

func parseTruthyString(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on", "y":
		return true
	default:
		return false
	}
}

func parseAlbumArtist(raw map[string]interface{}) string {
	if len(raw) == 0 {
		return ""
	}
	candidates := []string{
		"ALBUMARTIST", "ALBUM ARTIST", "TPE2", "aART", "albumartist", "album artist",
	}
	for _, key := range candidates {
		for k, v := range raw {
			if !strings.EqualFold(strings.TrimSpace(k), key) {
				continue
			}
			if s := coerceRawString(v); s != "" {
				return s
			}
		}
	}
	return ""
}

func isCompilationFromRaw(raw map[string]interface{}) bool {
	if len(raw) == 0 {
		return false
	}
	keys := []string{"COMPILATION", "TCMP", "cpil", "ITUNESCOMPILATION"}
	for _, key := range keys {
		for k, v := range raw {
			if !strings.EqualFold(strings.TrimSpace(k), key) {
				continue
			}
			if parseTruthyRaw(v) {
				return true
			}
		}
	}
	return false
}

func isExplicitlyNotCompilation(raw map[string]interface{}) bool {
	if len(raw) == 0 {
		return false
	}
	keys := []string{"COMPILATION", "TCMP", "cpil", "ITUNESCOMPILATION"}
	for _, key := range keys {
		for k, v := range raw {
			if !strings.EqualFold(strings.TrimSpace(k), key) {
				continue
			}
			s := strings.ToLower(strings.TrimSpace(coerceRawString(v)))
			switch s {
			case "0", "false", "no", "off":
				return true
			}
		}
	}
	return false
}

func isExplicitlyNotCompilationString(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "0", "false", "no", "off":
		return true
	default:
		return false
	}
}

func coerceRawString(v interface{}) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case []byte:
		return strings.TrimSpace(string(x))
	case fmt.Stringer:
		return strings.TrimSpace(x.String())
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func parseTruthyRaw(v interface{}) bool {
	s := strings.ToLower(strings.TrimSpace(coerceRawString(v)))
	switch s {
	case "1", "true", "yes", "on", "y":
		return true
	default:
		return false
	}
}

func parseTrackIndexFromRaw(raw map[string]interface{}, keys []string) int {
	if len(raw) == 0 || len(keys) == 0 {
		return 0
	}
	for _, key := range keys {
		for k, v := range raw {
			if !strings.EqualFold(strings.TrimSpace(k), key) {
				continue
			}
			text := strings.TrimSpace(coerceRawString(v))
			if text == "" {
				continue
			}
			if slash := strings.IndexByte(text, '/'); slash > 0 {
				text = strings.TrimSpace(text[:slash])
			}
			if n, err := strconv.Atoi(text); err == nil && n > 0 {
				return n
			}
			var digits strings.Builder
			for _, r := range text {
				if r >= '0' && r <= '9' {
					digits.WriteRune(r)
					continue
				}
				break
			}
			if digits.Len() == 0 {
				continue
			}
			if n, err := strconv.Atoi(digits.String()); err == nil && n > 0 {
				return n
			}
		}
	}
	return 0
}

func IsGenericCompilationArtist(artist string) bool {
	v := strings.ToLower(strings.TrimSpace(artist))
	return v == "various artists" || v == "various artist" || v == "va"
}

func ParseFilenameArtistTitle(baseNoExt string) (artist string, title string) {
	s := strings.TrimSpace(baseNoExt)
	if s == "" {
		return "", ""
	}
	delims := []string{" - ", " – ", " — "}
	for _, d := range delims {
		parts := strings.SplitN(s, d, 2)
		if len(parts) != 2 {
			continue
		}
		left := strings.TrimSpace(parts[0])
		right := strings.TrimSpace(parts[1])
		if left == "" || right == "" {
			continue
		}
		if len(left) < 2 || len(right) < 2 {
			continue
		}
		return left, right
	}
	return "", ""
}

func extractDurationMSFromRaw(raw map[string]interface{}) int {
	if len(raw) == 0 {
		return 0
	}
	for k, v := range raw {
		uk := strings.ToUpper(strings.TrimSpace(k))
		if uk == "" {
			continue
		}
		if !strings.Contains(uk, "TLEN") && !strings.Contains(uk, "DURATION") && !strings.Contains(uk, "LENGTH") {
			continue
		}
		if ms := coerceDurationMS(v); ms > 0 {
			return ms
		}
	}
	return 0
}

func coerceDurationMS(v interface{}) int {
	switch x := v.(type) {
	case int:
		return normalizeDurationNumber(float64(x))
	case int64:
		return normalizeDurationNumber(float64(x))
	case float64:
		return normalizeDurationNumber(x)
	case []byte:
		return parseDurationStringMS(string(x))
	case string:
		return parseDurationStringMS(x)
	default:
		return 0
	}
}

func normalizeDurationNumber(n float64) int {
	if n <= 0 {
		return 0
	}
	if n > 0 && n < 1000 {
		return int(n * 1000)
	}
	return int(n)
}

func parseDurationStringMS(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if strings.Count(s, ":") >= 1 {
		parts := strings.Split(s, ":")
		totalSeconds := 0
		mult := 1
		for i := len(parts) - 1; i >= 0; i-- {
			p := strings.TrimSpace(parts[i])
			if p == "" {
				return 0
			}
			n, err := strconv.Atoi(p)
			if err != nil || n < 0 {
				return 0
			}
			totalSeconds += n * mult
			mult *= 60
		}
		if totalSeconds > 0 {
			return totalSeconds * 1000
		}
		return 0
	}
	if n, err := strconv.ParseFloat(s, 64); err == nil {
		return normalizeDurationNumber(n)
	}
	return 0
}

func sniffMIME(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if ext != "" {
		if mt := mime.TypeByExtension(ext); mt != "" {
			return strings.Split(mt, ";")[0]
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return "application/octet-stream"
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, _ := io.ReadFull(f, buf)
	return http.DetectContentType(buf[:n])
}

func ArtFileExt(mimeType string) (string, error) {
	mimeType = strings.ToLower(strings.TrimSpace(strings.Split(mimeType, ";")[0]))
	switch mimeType {
	case "image/jpeg", "image/jpg":
		return ".jpg", nil
	case "image/png":
		return ".png", nil
	case "image/webp":
		return ".webp", nil
	default:
		return "", fmt.Errorf("unsupported art mime type: %q", mimeType)
	}
}

func VerifyImage(mimeType string, b []byte) error {
	if len(b) == 0 {
		return errors.New("empty image")
	}
	ct := http.DetectContentType(b)
	got := strings.ToLower(strings.Split(ct, ";")[0])
	if strings.HasPrefix(got, "image/") {
		return nil
	}
	if bytes.HasPrefix(b, []byte{0xFF, 0xD8, 0xFF}) {
		return nil
	}
	if bytes.HasPrefix(b, []byte{0x89, 0x50, 0x4E, 0x47}) {
		return nil
	}
	want := strings.ToLower(strings.Split(mimeType, ";")[0])
	return fmt.Errorf("not an image (declared=%q detected=%q)", want, got)
}
