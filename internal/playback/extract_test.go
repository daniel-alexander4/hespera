package playback

import (
	"testing"

	"hespera/internal/video"
)

func TestFromProbeTrackSelection(t *testing.T) {
	p := &video.ProbeResult{
		Format: video.ProbeFormat{Duration: "100", BitRate: "5000000"},
		Streams: []video.ProbeStream{
			{CodecType: "video", CodecName: "h264", Width: 1920, Height: 1080},
			{CodecType: "audio", CodecName: "aac"},
			{CodecType: "audio", CodecName: "ac3", IsDefault: true},
			{CodecType: "subtitle", CodecName: "subrip"},
			{CodecType: "subtitle", CodecName: "hdmv_pgs_subtitle"},
		},
	}

	t.Run("ordinal 0 picks the default audio", func(t *testing.T) {
		m := FromProbe(p, "mkv", 0, 0, 0)
		if m.AudioCodec != "ac3" {
			t.Fatalf("AudioCodec = %q, want ac3 (the default track)", m.AudioCodec)
		}
		if m.VideoCodec != "h264" || m.VideoWidth != 1920 {
			t.Fatalf("video = %q %dx%d, want h264 1920x1080", m.VideoCodec, m.VideoWidth, m.VideoHeight)
		}
		if m.HasSubtitle {
			t.Fatalf("sub 0 should select no subtitle")
		}
	})

	t.Run("explicit audio ordinal", func(t *testing.T) {
		if m := FromProbe(p, "mkv", 0, 1, 0); m.AudioCodec != "aac" {
			t.Fatalf("audio ordinal 1 = %q, want aac", m.AudioCodec)
		}
	})

	t.Run("out-of-range audio ordinal falls back to default", func(t *testing.T) {
		if m := FromProbe(p, "mkv", 0, 9, 0); m.AudioCodec != "ac3" {
			t.Fatalf("audio ordinal 9 = %q, want ac3 (default fallback)", m.AudioCodec)
		}
	})

	t.Run("text subtitle classified as text", func(t *testing.T) {
		m := FromProbe(p, "mkv", 0, 0, 1)
		if !m.HasSubtitle || !m.SubtitleIsText {
			t.Fatalf("subrip: HasSubtitle=%v IsText=%v, want true/true", m.HasSubtitle, m.SubtitleIsText)
		}
	})

	t.Run("bitmap subtitle classified as non-text", func(t *testing.T) {
		m := FromProbe(p, "mkv", 0, 0, 2)
		if !m.HasSubtitle || m.SubtitleIsText {
			t.Fatalf("pgs: HasSubtitle=%v IsText=%v, want true/false", m.HasSubtitle, m.SubtitleIsText)
		}
	})

	t.Run("out-of-range subtitle ordinal selects none", func(t *testing.T) {
		if m := FromProbe(p, "mkv", 0, 0, 9); m.HasSubtitle {
			t.Fatalf("sub ordinal 9 should select no subtitle")
		}
	})
}

func TestFromProbeLargestVideoWins(t *testing.T) {
	// MKV cover-art is a tiny mjpeg video stream; the real stream must win.
	p := &video.ProbeResult{
		Streams: []video.ProbeStream{
			{CodecType: "video", CodecName: "mjpeg", Width: 600, Height: 600},
			{CodecType: "video", CodecName: "h264", Width: 1920, Height: 1080},
		},
	}
	if m := FromProbe(p, "mkv", 0, 0, 0); m.VideoCodec != "h264" {
		t.Fatalf("VideoCodec = %q, want h264 (largest frame)", m.VideoCodec)
	}
}

func TestFromProbeNoAudio(t *testing.T) {
	p := &video.ProbeResult{Streams: []video.ProbeStream{{CodecType: "video", CodecName: "h264", Width: 1280, Height: 720}}}
	if m := FromProbe(p, "mp4", 0, 0, 0); m.HasAudio {
		t.Fatalf("HasAudio = true, want false for a video-only source")
	}
}

func TestBitrate(t *testing.T) {
	t.Run("declared bit_rate wins", func(t *testing.T) {
		if got := bitrate(video.ProbeFormat{BitRate: "8000000", Duration: "100"}, 1<<30); got != 8_000_000 {
			t.Fatalf("bitrate = %d, want 8000000", got)
		}
	})
	t.Run("falls back to size*8/duration", func(t *testing.T) {
		// 100 MB over 100s = 8 Mbit/s.
		if got := bitrate(video.ProbeFormat{Duration: "100"}, 100_000_000); got != 8_000_000 {
			t.Fatalf("bitrate = %d, want 8000000", got)
		}
	})
	t.Run("zero when nothing usable", func(t *testing.T) {
		if got := bitrate(video.ProbeFormat{}, 0); got != 0 {
			t.Fatalf("bitrate = %d, want 0", got)
		}
	})
}

func TestFromProbeContainerNormalized(t *testing.T) {
	if m := FromProbe(&video.ProbeResult{}, ".MKV", 0, 0, 0); m.Container != "mkv" {
		t.Fatalf("Container = %q, want mkv", m.Container)
	}
}
