package video

import (
	"context"
	"encoding/binary"
	"io"
	"math"
)

// moovMaxBuffer bounds how much leading output we hold while waiting for the
// complete moov box. A fragmented-MP4 moov (empty_moov) is tiny and written
// first, so this is only a safety ceiling against an unexpected/malformed
// stream — past it we give up and pass bytes through unpatched.
const moovMaxBuffer = 1 << 20 // 1 MiB

// StreamFFmpegPatchMoov is StreamFFmpeg for the progressive fragmented-MP4 paths
// (remux, burn-in), rewriting the duration in the leading moov so the browser
// learns the full length up front. Those paths mux with empty_moov, which writes
// the movie/track durations as 0; a plain <video src> then can't show the real
// length (the scrubber only grows as it buffers). streamDurSec is the duration
// of *this* stream — for a resumed stream (input -ss) that is total-start, since
// the output is rebased to zero and contains only the tail. streamDurSec <= 0
// (unknown) streams through unchanged.
func StreamFFmpegPatchMoov(ctx context.Context, w io.Writer, args []string, streamDurSec float64) error {
	if streamDurSec <= 0 {
		return StreamFFmpeg(ctx, w, args)
	}
	p := &moovDurationPatcher{dst: w, durSec: streamDurSec}
	err := StreamFFmpeg(ctx, p, args)
	p.flush() // emit any still-buffered head if the moov never completed
	return err
}

// moovDurationPatcher is an io.Writer that buffers the leading boxes of a
// fragmented MP4 until the moov is complete, rewrites the mvhd/tkhd/mdhd
// duration fields to durSec, then streams the rest through untouched. Patches
// are fixed-width field overwrites (no box resizing), so every downstream offset
// stays valid.
type moovDurationPatcher struct {
	dst    io.Writer
	durSec float64
	buf    []byte
	done   bool // moov handled (or given up) — from here, pass through
}

func (p *moovDurationPatcher) Write(b []byte) (int, error) {
	if p.done {
		return p.dst.Write(b)
	}
	p.buf = append(p.buf, b...)
	// Walk top-level boxes until we find a fully-buffered moov.
	off := 0
	for off+8 <= len(p.buf) {
		size, hdr, ok := boxSize(p.buf, off)
		if !ok || size < int64(hdr) {
			break // 0/extends-to-EOF or malformed: stop probing, keep buffering
		}
		typ := string(p.buf[off+4 : off+8])
		end := off + int(size)
		if typ == "moov" {
			if int64(off)+size > int64(len(p.buf)) {
				break // moov not fully buffered yet — wait for more
			}
			patchMoovDurations(p.buf[off:end], p.durSec)
			return p.emit(len(b))
		}
		if int64(off)+size > int64(len(p.buf)) {
			break // this box (e.g. ftyp) not fully buffered yet
		}
		off = end
	}
	if len(p.buf) > moovMaxBuffer {
		return p.emit(len(b)) // safety: stop buffering, pass through unpatched
	}
	return len(b), nil
}

// emit flushes the buffer to the underlying writer and switches to pass-through,
// reporting n as the count of source bytes consumed (always the whole Write).
func (p *moovDurationPatcher) emit(n int) (int, error) {
	p.done = true
	buf := p.buf
	p.buf = nil
	if _, err := p.dst.Write(buf); err != nil {
		return n, err
	}
	return n, nil
}

func (p *moovDurationPatcher) flush() {
	if !p.done && len(p.buf) > 0 {
		_, _ = p.dst.Write(p.buf)
		p.buf, p.done = nil, true
	}
}

// boxSize reads the size and header length of the ISO-BMFF box at off. ok is
// false for a 0 size (extends to EOF) or a truncated 64-bit header.
func boxSize(b []byte, off int) (size int64, hdr int, ok bool) {
	s := int64(binary.BigEndian.Uint32(b[off : off+4]))
	if s == 1 {
		if off+16 > len(b) {
			return 0, 0, false
		}
		return int64(binary.BigEndian.Uint64(b[off+8 : off+16])), 16, true
	}
	if s == 0 {
		return 0, 0, false
	}
	return s, 8, true
}

// patchMoovDurations rewrites the duration fields of every mvhd/tkhd/mdhd in a
// fully-buffered moov box to durSec. mvhd/mdhd carry their own timescale; tkhd
// uses the movie timescale (from mvhd, which always precedes the traks).
func patchMoovDurations(moov []byte, durSec float64) {
	var movieTimescale uint32
	var walk func(off, end int)
	walk = func(off, end int) {
		for off+8 <= end {
			size, hdr, ok := boxSize(moov, off)
			if !ok || size < int64(hdr) || off+int(size) > end {
				return
			}
			typ := string(moov[off+4 : off+8])
			body := off + hdr
			switch typ {
			case "mvhd":
				movieTimescale = setFullBoxDuration(moov, body, durSec, true)
			case "mdhd":
				setFullBoxDuration(moov, body, durSec, true)
			case "tkhd":
				setTkhdDuration(moov, body, durSec, movieTimescale)
			case "moov", "trak", "mdia", "edts":
				walk(body, off+int(size))
			}
			off += int(size)
		}
	}
	walk(0, len(moov)) // moov[0:] are the moov box's bytes; treat header+children uniformly
}

// setFullBoxDuration patches an mvhd/mdhd (creation, modification, timescale,
// duration layout). Returns the box's timescale. ownTS is always true here (both
// box types carry a timescale); kept for clarity. No-op on a zero timescale.
func setFullBoxDuration(b []byte, body int, durSec float64, ownTS bool) uint32 {
	ver := b[body]
	var tsOff, durOff int
	if ver == 1 {
		tsOff, durOff = body+20, body+24
	} else {
		tsOff, durOff = body+12, body+16
	}
	if (ver == 1 && durOff+8 > len(b)) || (ver == 0 && durOff+4 > len(b)) {
		return 0
	}
	ts := binary.BigEndian.Uint32(b[tsOff : tsOff+4])
	if ts == 0 {
		return 0
	}
	writeDuration(b, durOff, ver, durSec, ts)
	return ts
}

// setTkhdDuration patches a tkhd, which has no own timescale and uses the movie
// timescale. No-op if the movie timescale is unknown.
func setTkhdDuration(b []byte, body int, durSec float64, movieTS uint32) {
	if movieTS == 0 {
		return
	}
	ver := b[body]
	durOff := body + 20 // v0: ver/flags(4)+creation(4)+mod(4)+trackID(4)+reserved(4)
	if ver == 1 {
		durOff = body + 28 // v1: creation(8)+mod(8)+trackID(4)+reserved(4)
	}
	if (ver == 1 && durOff+8 > len(b)) || (ver == 0 && durOff+4 > len(b)) {
		return
	}
	writeDuration(b, durOff, ver, durSec, movieTS)
}

func writeDuration(b []byte, off int, ver uint8, durSec float64, ts uint32) {
	d := uint64(math.Round(durSec * float64(ts)))
	if ver == 1 {
		binary.BigEndian.PutUint64(b[off:off+8], d)
	} else {
		if d > math.MaxUint32 {
			d = math.MaxUint32
		}
		binary.BigEndian.PutUint32(b[off:off+4], uint32(d))
	}
}
