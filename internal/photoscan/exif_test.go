package photoscan

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// buildTIFF assembles a minimal EXIF TIFF block: IFD0 with Orientation,
// DateTime, and an Exif-IFD pointer whose sub-IFD carries DateTimeOriginal.
// Little-endian when le, else big-endian — both real-world byte orders.
func buildTIFF(t *testing.T, le bool, orientation int, dateTime, dateTimeOriginal string) []byte {
	t.Helper()
	var bo binary.ByteOrder = binary.BigEndian
	hdr := []byte("MM\x00*")
	if le {
		bo = binary.LittleEndian
		hdr = []byte("II*\x00")
	}

	// Layout: header(8) | IFD0 | exif IFD | data area (ASCII values).
	// Compute offsets after sizing: IFD0 has up to 3 entries, exif IFD 1.
	var ifd0Entries [][]byte
	dataArea := &bytes.Buffer{}
	// data area begins after: 8 hdr + ifd0(2+N*12+4) + exifIFD(2+1*12+4)
	entry := func(tag, typ uint16, count uint32, val []byte) []byte {
		e := make([]byte, 12)
		bo.PutUint16(e[0:2], tag)
		bo.PutUint16(e[2:4], typ)
		bo.PutUint32(e[4:8], count)
		copy(e[8:12], val)
		return e
	}

	nIFD0 := 0
	if orientation > 0 {
		nIFD0++
	}
	if dateTime != "" {
		nIFD0++
	}
	nIFD0++ // exif pointer always present
	ifd0Size := 2 + nIFD0*12 + 4
	exifIFDOff := 8 + ifd0Size
	exifIFDSize := 2 + 1*12 + 4
	dataOff := exifIFDOff + exifIFDSize

	putASCII := func(s string) []byte {
		v := make([]byte, 4)
		bo.PutUint32(v, uint32(dataOff+dataArea.Len()))
		dataArea.WriteString(s)
		dataArea.WriteByte(0)
		return v
	}

	if orientation > 0 {
		v := make([]byte, 4)
		bo.PutUint16(v[0:2], uint16(orientation))
		ifd0Entries = append(ifd0Entries, entry(tagOrientation, 3, 1, v))
	}
	if dateTime != "" {
		ifd0Entries = append(ifd0Entries, entry(tagDateTime, 2, uint32(len(dateTime)+1), putASCII(dateTime)))
	}
	v := make([]byte, 4)
	bo.PutUint32(v, uint32(exifIFDOff))
	ifd0Entries = append(ifd0Entries, entry(tagExifIFDPointer, 4, 1, v))

	out := &bytes.Buffer{}
	out.Write(hdr)
	off := make([]byte, 4)
	bo.PutUint32(off, 8)
	out.Write(off)
	cnt := make([]byte, 2)
	bo.PutUint16(cnt, uint16(len(ifd0Entries)))
	out.Write(cnt)
	for _, e := range ifd0Entries {
		out.Write(e)
	}
	out.Write([]byte{0, 0, 0, 0}) // next-IFD pointer

	// Exif sub-IFD: DateTimeOriginal only (empty string → zero entries).
	bo.PutUint16(cnt, 1)
	out.Write(cnt)
	out.Write(entry(tagDateTimeOriginal, 2, uint32(len(dateTimeOriginal)+1), putASCII(dateTimeOriginal)))
	out.Write([]byte{0, 0, 0, 0})

	out.Write(dataArea.Bytes())
	return out.Bytes()
}

// wrapJPEG embeds a TIFF block as a JPEG APP1 Exif segment, preceded by a
// JFIF APP0 (the common real layout) and followed by an SOS marker.
func wrapJPEG(tiff []byte) []byte {
	out := &bytes.Buffer{}
	out.Write([]byte{0xFF, 0xD8}) // SOI
	// APP0 JFIF (16 bytes total payload incl length)
	out.Write([]byte{0xFF, 0xE0, 0x00, 0x10})
	out.Write([]byte("JFIF\x00\x01\x02\x00\x00\x01\x00\x01\x00\x00"))
	// APP1 Exif
	payload := append([]byte("Exif\x00\x00"), tiff...)
	out.Write([]byte{0xFF, 0xE1})
	ln := make([]byte, 2)
	binary.BigEndian.PutUint16(ln, uint16(len(payload)+2))
	out.Write(ln)
	out.Write(payload)
	out.Write([]byte{0xFF, 0xDA, 0x00, 0x02}) // SOS — parser must stop here
	return out.Bytes()
}

func writeTemp(t *testing.T, name string, b []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return p
}

func TestReadEXIF(t *testing.T) {
	want := time.Date(2019, 7, 4, 12, 30, 45, 0, time.Local)

	for _, le := range []bool{true, false} {
		name := map[bool]string{true: "little-endian", false: "big-endian"}[le]
		t.Run(name, func(t *testing.T) {
			tiff := buildTIFF(t, le, 6, "2020:01:01 00:00:00", "2019:07:04 12:30:45")
			p := writeTemp(t, "x.jpg", wrapJPEG(tiff))
			taken, orient := ReadEXIF(p)
			if !taken.Equal(want) {
				t.Fatalf("taken = %v, want %v", taken, want)
			}
			if orient != 6 {
				t.Fatalf("orientation = %d, want 6", orient)
			}
		})
	}

	t.Run("DateTimeOriginal preferred over IFD0 DateTime", func(t *testing.T) {
		tiff := buildTIFF(t, true, 1, "2020:01:01 00:00:00", "2019:07:04 12:30:45")
		p := writeTemp(t, "x.jpg", wrapJPEG(tiff))
		taken, _ := ReadEXIF(p)
		if !taken.Equal(want) {
			t.Fatalf("taken = %v, want DateTimeOriginal %v", taken, want)
		}
	})

	t.Run("bare TIFF file parses directly", func(t *testing.T) {
		tiff := buildTIFF(t, false, 3, "", "2019:07:04 12:30:45")
		p := writeTemp(t, "x.tif", tiff)
		taken, orient := ReadEXIF(p)
		if !taken.Equal(want) || orient != 3 {
			t.Fatalf("taken=%v orient=%d, want %v/3", taken, orient, want)
		}
	})

	t.Run("no EXIF: zero time, zero orientation", func(t *testing.T) {
		p := writeTemp(t, "plain.jpg", []byte{0xFF, 0xD8, 0xFF, 0xDA, 0x00, 0x02})
		taken, orient := ReadEXIF(p)
		if !taken.IsZero() || orient != 0 {
			t.Fatalf("expected zero values, got %v/%d", taken, orient)
		}
	})

	t.Run("all-zero camera timestamp rejected", func(t *testing.T) {
		tiff := buildTIFF(t, true, 1, "", "0000:00:00 00:00:00")
		p := writeTemp(t, "x.jpg", wrapJPEG(tiff))
		taken, _ := ReadEXIF(p)
		if !taken.IsZero() {
			t.Fatalf("all-zero timestamp must read as absent, got %v", taken)
		}
	})

	t.Run("truncated/corrupt EXIF never panics", func(t *testing.T) {
		tiff := buildTIFF(t, true, 6, "2020:01:01 00:00:00", "2019:07:04 12:30:45")
		full := wrapJPEG(tiff)
		for cut := 0; cut < len(full); cut += 3 {
			p := writeTemp(t, "cut.jpg", full[:cut])
			ReadEXIF(p) // any result is fine — just must not panic or hang
		}
	})
}
