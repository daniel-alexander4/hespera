package photoscan

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// Minimal EXIF reader: the capture timestamp + orientation, nothing else.
// Hand-rolled (repo precedent: the tolerant ID3v2 fallback in internal/music)
// rather than a dependency — only three tags are needed, and the TIFF IFD
// walk for them is small. Handles a JPEG's APP1 Exif segment and a bare TIFF
// file (a .tif starts with the TIFF header directly). Anything absent or
// malformed degrades to the zero value — the scanner falls back to mtime.

const (
	tagDateTime          = 0x0132 // IFD0: file-change date (fallback)
	tagOrientation       = 0x0112 // IFD0
	tagExifIFDPointer    = 0x8769 // IFD0 → Exif sub-IFD
	tagDateTimeOriginal  = 0x9003 // Exif IFD: shutter time (preferred)
	tagDateTimeDigitized = 0x9004
)

// exifReadLimit bounds how much of the file is read looking for the EXIF
// block. A JPEG's APP1 segment is ≤64KiB and sits at the front; bare TIFFs
// can scatter IFDs, so give them headroom — past this, treat as absent.
const exifReadLimit = 4 << 20

// ReadEXIF returns the capture time (zero when absent/undecodable) and the
// EXIF orientation (1-8; 0 when absent) of a JPEG or TIFF file.
func ReadEXIF(path string) (time.Time, int) {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}, 0
	}
	defer f.Close()
	buf, err := io.ReadAll(io.LimitReader(f, exifReadLimit))
	if err != nil || len(buf) < 8 {
		return time.Time{}, 0
	}

	var tiff []byte
	switch {
	case buf[0] == 0xFF && buf[1] == 0xD8: // JPEG SOI
		tiff = jpegExifPayload(buf)
	case string(buf[:4]) == "II*\x00" || string(buf[:4]) == "MM\x00*": // bare TIFF
		tiff = buf
	}
	if tiff == nil {
		return time.Time{}, 0
	}
	return parseTIFF(tiff)
}

// jpegExifPayload walks the JPEG marker chain for the APP1 "Exif" segment and
// returns its TIFF payload, or nil.
func jpegExifPayload(buf []byte) []byte {
	i := 2
	for i+4 <= len(buf) {
		if buf[i] != 0xFF {
			return nil // marker desync — bail rather than misparse
		}
		marker := buf[i+1]
		if marker == 0xDA || marker == 0xD9 { // SOS / EOI: no APP1 before image data
			return nil
		}
		size := int(binary.BigEndian.Uint16(buf[i+2 : i+4])) // includes the 2 length bytes
		if size < 2 || i+2+size > len(buf) {
			return nil
		}
		if marker == 0xE1 && size >= 8 && string(buf[i+4:i+10]) == "Exif\x00\x00" {
			return buf[i+10 : i+2+size]
		}
		i += 2 + size
	}
	return nil
}

// parseTIFF walks IFD0 (orientation, DateTime, the Exif-IFD pointer) and the
// Exif sub-IFD (DateTimeOriginal/Digitized). Every offset is bounds-checked;
// a malformed structure yields whatever was found before it broke.
func parseTIFF(tiff []byte) (time.Time, int) {
	if len(tiff) < 8 {
		return time.Time{}, 0
	}
	var bo binary.ByteOrder
	switch string(tiff[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return time.Time{}, 0
	}
	ifd0 := int64(bo.Uint32(tiff[4:8]))

	var orientation int
	var dtOriginal, dtDigitized, dt string
	var exifIFD int64

	walkIFD(tiff, bo, ifd0, func(tag uint16, typ uint16, count uint32, value []byte) {
		switch tag {
		case tagOrientation:
			if v, ok := shortValue(bo, typ, value); ok && v >= 1 && v <= 8 {
				orientation = int(v)
			}
		case tagDateTime:
			dt = asciiValue(value)
		case tagExifIFDPointer:
			if v, ok := longValue(bo, typ, value); ok {
				exifIFD = int64(v)
			}
		}
	})
	if exifIFD > 0 {
		walkIFD(tiff, bo, exifIFD, func(tag uint16, typ uint16, count uint32, value []byte) {
			switch tag {
			case tagDateTimeOriginal:
				dtOriginal = asciiValue(value)
			case tagDateTimeDigitized:
				dtDigitized = asciiValue(value)
			}
		})
	}

	for _, s := range []string{dtOriginal, dtDigitized, dt} {
		if t, err := parseExifTime(s); err == nil {
			return t, orientation
		}
	}
	return time.Time{}, orientation
}

// walkIFD visits each directory entry of one IFD, handing the callback the
// tag, type, count, and the resolved value bytes (inline for ≤4 bytes, else
// the pointed-to slice). Silently stops on any out-of-bounds structure.
func walkIFD(tiff []byte, bo binary.ByteOrder, off int64, visit func(tag, typ uint16, count uint32, value []byte)) {
	if off < 0 || off+2 > int64(len(tiff)) {
		return
	}
	n := int64(bo.Uint16(tiff[off : off+2]))
	if n > 512 { // no real IFD has hundreds of entries — corrupt count
		return
	}
	for i := int64(0); i < n; i++ {
		e := off + 2 + i*12
		if e+12 > int64(len(tiff)) {
			return
		}
		tag := bo.Uint16(tiff[e : e+2])
		typ := bo.Uint16(tiff[e+2 : e+4])
		count := bo.Uint32(tiff[e+4 : e+8])
		size := typeSize(typ) * int64(count)
		var value []byte
		if size <= 4 && size > 0 {
			value = tiff[e+8 : e+8+size]
		} else if size > 0 {
			vo := int64(bo.Uint32(tiff[e+8 : e+12]))
			if vo < 0 || vo+size > int64(len(tiff)) {
				continue
			}
			value = tiff[vo : vo+size]
		}
		visit(tag, typ, count, value)
	}
}

func typeSize(typ uint16) int64 {
	switch typ {
	case 1, 2, 6, 7: // BYTE, ASCII, SBYTE, UNDEFINED
		return 1
	case 3, 8: // SHORT, SSHORT
		return 2
	case 4, 9, 11: // LONG, SLONG, FLOAT
		return 4
	case 5, 10, 12: // RATIONAL, SRATIONAL, DOUBLE
		return 8
	default:
		return 0
	}
}

func shortValue(bo binary.ByteOrder, typ uint16, v []byte) (uint16, bool) {
	if typ != 3 || len(v) < 2 {
		return 0, false
	}
	return bo.Uint16(v[:2]), true
}

func longValue(bo binary.ByteOrder, typ uint16, v []byte) (uint32, bool) {
	if typ != 4 || len(v) < 4 {
		return 0, false
	}
	return bo.Uint32(v[:4]), true
}

func asciiValue(v []byte) string {
	return strings.TrimRight(string(v), "\x00")
}

// parseExifTime parses EXIF's "2006:01:02 15:04:05" (naive local wall time —
// EXIF carries no zone). Cameras with no clock write all-zero timestamps;
// reject those.
func parseExifTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasPrefix(s, "0000") {
		return time.Time{}, fmt.Errorf("empty exif time")
	}
	return time.ParseInLocation("2006:01:02 15:04:05", s, time.Local)
}
