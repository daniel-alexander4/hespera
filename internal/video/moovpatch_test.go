package video

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"
)

// readMvhd returns the (timescale, duration) of the first mvhd box.
func readMvhd(b []byte) (ts, dur uint64) {
	i := bytes.Index(b, []byte("mvhd"))
	if i < 0 {
		return 0, 0
	}
	body := i + 4
	if b[body] == 1 {
		return uint64(binary.BigEndian.Uint32(b[body+20 : body+24])), binary.BigEndian.Uint64(b[body+24 : body+32])
	}
	return uint64(binary.BigEndian.Uint32(b[body+12 : body+16])), uint64(binary.BigEndian.Uint32(b[body+16 : body+20]))
}

// TestStreamFFmpegPatchMoovRealStream proves the patcher fixes a real ffmpeg
// empty_moov fragmented MP4 (the actual remux/burn-in mux), which writes mvhd
// duration 0, so the browser can't show the full length until the patch.
func TestStreamFFmpegPatchMoovRealStream(t *testing.T) {
	ffmpegAvailable(t)
	src, _, _ := sampleClipDur(t, 5)
	args := RemuxArgs(src, 0, 0) // frag_keyframe+empty_moov+faststart, -f mp4 pipe:1

	var unpatched bytes.Buffer
	if err := StreamFFmpeg(context.Background(), &unpatched, args); err != nil {
		t.Fatalf("unpatched stream: %v", err)
	}
	if _, dur := readMvhd(unpatched.Bytes()); dur != 0 {
		t.Fatalf("expected empty_moov to write mvhd duration 0, got %d (test premise broken)", dur)
	}

	var patched bytes.Buffer
	if err := StreamFFmpegPatchMoov(context.Background(), &patched, RemuxArgs(src, 0, 0), 5); err != nil {
		t.Fatalf("patched stream: %v", err)
	}
	ts, dur := readMvhd(patched.Bytes())
	if ts == 0 || dur == 0 {
		t.Fatalf("patched mvhd has zero ts/dur: ts=%d dur=%d", ts, dur)
	}
	secs := float64(dur) / float64(ts)
	if secs < 4.5 || secs > 5.5 {
		t.Fatalf("patched mvhd duration = %.3fs, want ~5s", secs)
	}
	if patched.Len() != unpatched.Len() {
		t.Fatalf("patch changed byte length: unpatched=%d patched=%d", unpatched.Len(), patched.Len())
	}
}

// box builds an ISO-BMFF box: 4-byte size + 4-byte type + body.
func box(typ string, body []byte) []byte {
	b := make([]byte, 8+len(body))
	binary.BigEndian.PutUint32(b[0:4], uint32(8+len(body)))
	copy(b[4:8], typ)
	copy(b[8:], body)
	return b
}

func be32(v uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }

// buildFragMoov assembles a minimal fragmented-MP4-shaped file with mvhd/tkhd/mdhd
// durations all zero (as empty_moov writes them), plus ftyp + mdat around the moov.
func buildFragMoov() []byte {
	// mvhd v0: flags(4) creation(4) mod(4) timescale(4)=1000 duration(4)=0 + pad
	mvhd := box("mvhd", concat(make([]byte, 12), be32(1000), be32(0), make([]byte, 76)))
	// mdhd v0: flags(4) creation(4) mod(4) timescale(4)=600 duration(4)=0 + pad
	mdhd := box("mdhd", concat(make([]byte, 12), be32(600), be32(0), make([]byte, 4)))
	// tkhd v0: flags(4) creation(4) mod(4) trackID(4)=1 reserved(4) duration(4)=0 + pad
	tkhd := box("tkhd", concat(make([]byte, 12), be32(1), make([]byte, 4), be32(0), make([]byte, 60)))
	mdia := box("mdia", mdhd)
	trak := box("trak", concat(tkhd, mdia))
	moov := box("moov", concat(mvhd, trak))
	ftyp := box("ftyp", []byte("isomiso2mp41"))
	mdat := box("mdat", []byte("not-real-media-payload"))
	return concat(ftyp, moov, mdat)
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// durAt finds the first occurrence of a box tag and reads the 4-byte (v0)
// duration field at body+durRel.
func durAt(b []byte, tag string, durRel int) uint32 {
	i := bytes.Index(b, []byte(tag))
	body := i + 4
	return binary.BigEndian.Uint32(b[body+durRel : body+durRel+4])
}

func TestMoovPatcherRewritesDurations(t *testing.T) {
	in := buildFragMoov()
	var dst bytes.Buffer
	p := &moovDurationPatcher{dst: &dst, durSec: 42}
	// Feed in 7-byte chunks so the moov spans many Writes (exercises buffering).
	for off := 0; off < len(in); off += 7 {
		end := off + 7
		if end > len(in) {
			end = len(in)
		}
		if _, err := p.Write(in[off:end]); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	p.flush()
	out := dst.Bytes()

	if len(out) != len(in) {
		t.Fatalf("length changed: in=%d out=%d (patch must be fixed-width)", len(in), len(out))
	}
	if d := durAt(out, "mvhd", 16); d != 42000 {
		t.Errorf("mvhd duration = %d, want 42000 (42s * 1000)", d)
	}
	if d := durAt(out, "tkhd", 20); d != 42000 {
		t.Errorf("tkhd duration = %d, want 42000 (uses movie timescale)", d)
	}
	if d := durAt(out, "mdhd", 16); d != 25200 {
		t.Errorf("mdhd duration = %d, want 25200 (42s * 600)", d)
	}
	// Bytes outside the moov (ftyp, mdat) must be byte-identical.
	want := buildFragMoov()
	binary.BigEndian.PutUint32(want[bytes.Index(want, []byte("mvhd"))+4+16:][:4], 42000)
	binary.BigEndian.PutUint32(want[bytes.Index(want, []byte("tkhd"))+4+20:][:4], 42000)
	binary.BigEndian.PutUint32(want[bytes.Index(want, []byte("mdhd"))+4+16:][:4], 25200)
	if !bytes.Equal(out, want) {
		t.Fatal("output differs from input outside the patched duration fields")
	}
}

func TestMoovPatcherPassthroughWithoutMoov(t *testing.T) {
	in := concat(box("ftyp", []byte("isom")), box("mdat", []byte("payload-bytes")))
	var dst bytes.Buffer
	p := &moovDurationPatcher{dst: &dst, durSec: 30}
	if _, err := p.Write(in); err != nil {
		t.Fatalf("Write: %v", err)
	}
	p.flush()
	if !bytes.Equal(dst.Bytes(), in) {
		t.Fatal("stream without a moov must pass through unchanged")
	}
}
