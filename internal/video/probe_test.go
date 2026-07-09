package video

import (
	"encoding/json"
	"strings"
	"testing"
)

const sampleFFProbeJSON = `{
  "format": {
    "duration": "2712.123000",
    "size": "4289510912",
    "bit_rate": "12654321"
  },
  "streams": [
    {
      "index": 0,
      "codec_type": "video",
      "codec_name": "hevc",
      "width": 1920,
      "height": 1080,
      "tags": {},
      "disposition": {"default": 1, "dub": 0}
    },
    {
      "index": 1,
      "codec_type": "audio",
      "codec_name": "aac",
      "channels": 6,
      "tags": {"language": "eng", "title": "Surround 5.1"},
      "disposition": {"default": 1, "dub": 0}
    },
    {
      "index": 2,
      "codec_type": "subtitle",
      "codec_name": "subrip",
      "tags": {"language": "spa", "title": "Spanish"},
      "disposition": {"default": 0, "dub": 0}
    }
  ]
}`

func TestParseProbeJSON(t *testing.T) {
	result, err := parseProbeJSON([]byte(sampleFFProbeJSON))
	if err != nil {
		t.Fatalf("parseProbeJSON: %v", err)
	}

	if result.Format.Duration != "2712.123000" {
		t.Fatalf("format.duration = %q, want 2712.123000", result.Format.Duration)
	}
	if result.Format.Size != "4289510912" {
		t.Fatalf("format.size = %q, want 4289510912", result.Format.Size)
	}
	if len(result.Streams) != 3 {
		t.Fatalf("streams count = %d, want 3", len(result.Streams))
	}

	// Video stream
	v := result.Streams[0]
	if v.CodecType != "video" || v.CodecName != "hevc" {
		t.Fatalf("stream 0: type=%q codec=%q, want video/hevc", v.CodecType, v.CodecName)
	}
	if v.Width != 1920 || v.Height != 1080 {
		t.Fatalf("stream 0: %dx%d, want 1920x1080", v.Width, v.Height)
	}
	if !v.IsDefault {
		t.Fatalf("stream 0: IsDefault = false, want true")
	}

	// Audio stream
	a := result.Streams[1]
	if a.CodecType != "audio" || a.Channels != 6 {
		t.Fatalf("stream 1: type=%q channels=%d, want audio/6", a.CodecType, a.Channels)
	}
	if a.Language != "eng" {
		t.Fatalf("stream 1: language=%q, want eng", a.Language)
	}
	if a.Title != "Surround 5.1" {
		t.Fatalf("stream 1: title=%q, want Surround 5.1", a.Title)
	}

	// Subtitle stream
	s := result.Streams[2]
	if s.CodecType != "subtitle" || s.Language != "spa" {
		t.Fatalf("stream 2: type=%q lang=%q, want subtitle/spa", s.CodecType, s.Language)
	}
	if s.IsDefault {
		t.Fatalf("stream 2: IsDefault = true, want false")
	}
}

func TestParseProbeJSONInvalid(t *testing.T) {
	_, err := parseProbeJSON([]byte(`not json`))
	if err == nil {
		t.Fatalf("expected error for invalid json")
	}
}

func TestParseProbeJSONChapters(t *testing.T) {
	data := `{"format":{},"streams":[],"chapters":[
		{"start_time":"0.000000","end_time":"8.000000","tags":{"title":"Intro"}},
		{"start_time":"8.000000","end_time":"8.000000","tags":{"title":"ZeroLen"}},
		{"start_time":"40.000000","end_time":"48.000000","tags":{"title":"Advertisement"}}
	]}`
	result, err := parseProbeJSON([]byte(data))
	if err != nil {
		t.Fatalf("parseProbeJSON: %v", err)
	}
	// The zero-length chapter (end <= start) is dropped.
	if len(result.Chapters) != 2 {
		t.Fatalf("got %d chapters, want 2: %+v", len(result.Chapters), result.Chapters)
	}
	if result.Chapters[0] != (ProbeChapter{StartSec: 0, EndSec: 8, Title: "Intro"}) {
		t.Errorf("chapter 0 = %+v", result.Chapters[0])
	}
	if result.Chapters[1] != (ProbeChapter{StartSec: 40, EndSec: 48, Title: "Advertisement"}) {
		t.Errorf("chapter 1 = %+v", result.Chapters[1])
	}
}

func TestVideoDisplayAspect(t *testing.T) {
	cases := []struct {
		name  string
		probe ProbeResult
		want  float64
	}{
		{
			name: "explicit DAR wins (anamorphic PAL DVD)",
			probe: ProbeResult{Streams: []ProbeStream{{
				CodecType: "video", Width: 702, Height: 576,
				SampleAspectRatio: "499:351", DisplayAspectRatio: "499:288",
			}}},
			want: 499.0 / 288.0,
		},
		{
			name: "SAR fallback when DAR absent",
			probe: ProbeResult{Streams: []ProbeStream{{
				CodecType: "video", Width: 720, Height: 576, SampleAspectRatio: "64:45",
			}}},
			want: (720.0 / 576.0) * (64.0 / 45.0),
		},
		{
			name: "square pixels return plain w/h",
			probe: ProbeResult{Streams: []ProbeStream{{
				CodecType: "video", Width: 640, Height: 496,
			}}},
			want: 640.0 / 496.0,
		},
		{
			name: "garbage ratios ignored",
			probe: ProbeResult{Streams: []ProbeStream{{
				CodecType: "video", Width: 1920, Height: 1080,
				SampleAspectRatio: "N/A", DisplayAspectRatio: "0:1",
			}}},
			want: 1920.0 / 1080.0,
		},
		{
			name: "audio stream skipped, no video = 0",
			probe: ProbeResult{Streams: []ProbeStream{{
				CodecType: "audio", Channels: 2,
			}}},
			want: 0,
		},
		{
			name: "zero dimensions and no DAR = 0",
			probe: ProbeResult{Streams: []ProbeStream{{
				CodecType: "video",
			}}},
			want: 0,
		},
	}
	for _, tc := range cases {
		got := tc.probe.VideoDisplayAspect()
		if diff := got - tc.want; diff > 1e-9 || diff < -1e-9 {
			t.Errorf("%s: VideoDisplayAspect = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The reprobe jobs backfill rows probed before aspect capture by testing for the
// display_aspect_ratio key in stored stream_info_json — so the key must always be
// present in a marshaled probe (non-omitempty), even when ffprobe reported nothing,
// or the backfill would re-select those rows forever.
func TestProbeJSONAlwaysCarriesDARKey(t *testing.T) {
	result, err := parseProbeJSON([]byte(sampleFFProbeJSON))
	if err != nil {
		t.Fatalf("parseProbeJSON: %v", err)
	}
	b, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"display_aspect_ratio"`) {
		t.Fatalf("marshaled probe missing display_aspect_ratio key: %s", b)
	}
}
