package playback

import "testing"

func chrome() ClientProfile { return profiles["web-chrome"] }
func safari() ClientProfile { return profiles["web-safari"] }

func TestDecide(t *testing.T) {
	tests := []struct {
		name     string
		profile  ClientProfile
		media    MediaInfo
		mode     string
		want     Decision
		protocol Protocol
	}{
		{
			name:     "h264/aac/mp4 on chrome direct-plays",
			profile:  chrome(),
			media:    MediaInfo{Container: "mp4", VideoCodec: "h264", HasAudio: true, AudioCodec: "aac"},
			want:     DirectPlay,
			protocol: ProtocolFile,
		},
		{
			name:     "h264/aac/mkv on chrome remuxes (container-only blocker)",
			profile:  chrome(),
			media:    MediaInfo{Container: "mkv", VideoCodec: "h264", HasAudio: true, AudioCodec: "aac"},
			want:     DirectStream,
			protocol: ProtocolFile,
		},
		{
			// The middle gear. The picture is fine; only the soundtrack is not.
			// Re-encoding the video here (which is what this used to do) burned
			// ~31x the CPU for nothing — see RemuxAudioNeedsTranscode.
			name:     "h264/ac3/mkv on chrome direct-streams, re-encoding only the audio",
			profile:  chrome(),
			media:    MediaInfo{Container: "mkv", VideoCodec: "h264", HasAudio: true, AudioCodec: "ac3"},
			want:     DirectStream,
			protocol: ProtocolFile,
		},
		{
			name:     "hevc/aac/mp4 on chrome transcodes (hevc unsupported anywhere)",
			profile:  chrome(),
			media:    MediaInfo{Container: "mp4", VideoCodec: "hevc", HasAudio: true, AudioCodec: "aac"},
			want:     Transcode,
			protocol: ProtocolHLS,
		},
		{
			name:     "hevc/aac/mp4 on safari direct-plays",
			profile:  safari(),
			media:    MediaInfo{Container: "mp4", VideoCodec: "hevc", HasAudio: true, AudioCodec: "aac"},
			want:     DirectPlay,
			protocol: ProtocolFile,
		},
		{
			name:     "vp9/opus/webm on chrome direct-plays",
			profile:  chrome(),
			media:    MediaInfo{Container: "webm", VideoCodec: "vp9", HasAudio: true, AudioCodec: "opus"},
			want:     DirectPlay,
			protocol: ProtocolFile,
		},
		{
			// F1 regression: independent set membership would have green-lit this.
			name:     "vp9/opus in MP4 on chrome transcodes (invalid combination, not remuxable)",
			profile:  chrome(),
			media:    MediaInfo{Container: "mp4", VideoCodec: "vp9", HasAudio: true, AudioCodec: "opus"},
			want:     Transcode,
			protocol: ProtocolHLS,
		},
		{
			// F1 regression: Opus-in-MP4 is not a valid direct-play combination —
			// that is the invariant this case guards, and it still holds. The
			// remedy is now the cheaper one: the h264 picture is copied and only
			// the opus track is re-encoded (AudioTranscode, pinned separately in
			// TestDecideAudioTranscode). The container↔codec matrix is still what
			// rejects direct play; only the fix-up got cheaper.
			name:     "h264/opus in MP4 on chrome cannot direct-play; the audio alone is re-encoded",
			profile:  chrome(),
			media:    MediaInfo{Container: "mp4", VideoCodec: "h264", HasAudio: true, AudioCodec: "opus"},
			want:     DirectStream,
			protocol: ProtocolFile,
		},
		{
			// F5 regression: a video with no audio track must not be flagged audio-unsupported.
			name:     "h264/mp4 with no audio direct-plays",
			profile:  chrome(),
			media:    MediaInfo{Container: "mp4", VideoCodec: "h264", HasAudio: false},
			want:     DirectPlay,
			protocol: ProtocolFile,
		},
		{
			name:     "bitrate over cap forces transcode",
			profile:  chrome(),
			media:    MediaInfo{Container: "mp4", VideoCodec: "h264", HasAudio: true, AudioCodec: "aac", BitrateBPS: 60_000_000},
			want:     Transcode,
			protocol: ProtocolHLS,
		},
		{
			name:     "resolution over cap forces transcode",
			profile:  chrome(),
			media:    MediaInfo{Container: "mp4", VideoCodec: "h264", HasAudio: true, AudioCodec: "aac", VideoWidth: 7680, VideoHeight: 4320},
			want:     Transcode,
			protocol: ProtocolHLS,
		},
		{
			name:     "unknown video codec fails safe to transcode",
			profile:  chrome(),
			media:    MediaInfo{Container: "mp4", VideoCodec: "theora", HasAudio: true, AudioCodec: "aac"},
			want:     Transcode,
			protocol: ProtocolHLS,
		},
		{
			name:     "selected text subtitle does not force transcode (sidecar)",
			profile:  chrome(),
			media:    MediaInfo{Container: "mp4", VideoCodec: "h264", HasAudio: true, AudioCodec: "aac", HasSubtitle: true, SubtitleIsText: true},
			want:     DirectPlay,
			protocol: ProtocolFile,
		},
		{
			name:     "selected bitmap subtitle forces transcode (burn-in)",
			profile:  chrome(),
			media:    MediaInfo{Container: "mp4", VideoCodec: "h264", HasAudio: true, AudioCodec: "aac", HasSubtitle: true, SubtitleIsText: false},
			want:     Transcode,
			protocol: ProtocolHLS,
		},
		{
			name:     "mode override forces transcode",
			profile:  chrome(),
			media:    MediaInfo{Container: "mp4", VideoCodec: "h264", HasAudio: true, AudioCodec: "aac"},
			mode:     "transcode",
			want:     Transcode,
			protocol: ProtocolHLS,
		},
		{
			name:     "mode override forces direct play of an incompatible file",
			profile:  chrome(),
			media:    MediaInfo{Container: "mkv", VideoCodec: "hevc", HasAudio: true, AudioCodec: "ac3"},
			mode:     "direct",
			want:     DirectPlay,
			protocol: ProtocolFile,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := Decide(tc.profile, tc.media, tc.mode)
			if out.Decision != tc.want {
				t.Fatalf("Decision = %q, want %q (reasons: %v)", out.Decision, tc.want, out.Reasons)
			}
			if out.Protocol != tc.protocol {
				t.Fatalf("Protocol = %q, want %q", out.Protocol, tc.protocol)
			}
		})
	}
}

// AudioTranscode is what tells the remux path to re-encode the soundtrack while
// still copying the picture. Two independent things can demand it: the client
// can't decode the codec, or our fragmented-MP4 output can't carry it.
func TestDecideAudioTranscode(t *testing.T) {
	tests := []struct {
		name    string
		profile ClientProfile
		media   MediaInfo
		want    bool
	}{
		{
			name:    "aac needs no re-encode — it copies into fMP4 and every client plays it",
			profile: chrome(),
			media:   MediaInfo{Container: "mkv", VideoCodec: "h264", HasAudio: true, AudioCodec: "aac"},
			want:    false,
		},
		{
			name:    "chrome cannot decode e-ac-3, so it is re-encoded",
			profile: chrome(),
			media:   MediaInfo{Container: "mkv", VideoCodec: "h264", HasAudio: true, AudioCodec: "eac3"},
			want:    true,
		},
		{
			// The regression this rule exists for. Safari's profile SAYS it can
			// decode ac3, so the old audio-aware canRemux green-lit a copy — but
			// ffmpeg cannot mux AC-3 into a fragmented MP4 with empty_moov at all
			// ("Cannot write moov atom before AC3 packets"), so the stream died on
			// the header write. The muxer's limit outranks the client's ability.
			name:    "safari CAN decode ac3, but fMP4 cannot carry it — re-encode anyway",
			profile: safari(),
			media:   MediaInfo{Container: "mkv", VideoCodec: "h264", HasAudio: true, AudioCodec: "ac3"},
			want:    true,
		},
		{
			name:    "a silent file asks nothing of the audio encoder",
			profile: chrome(),
			media:   MediaInfo{Container: "mkv", VideoCodec: "h264"},
			want:    false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := Decide(tc.profile, tc.media, "")
			if out.Decision != DirectStream {
				t.Fatalf("precondition: Decision = %q, want direct_stream", out.Decision)
			}
			if out.AudioTranscode != tc.want {
				t.Fatalf("AudioTranscode = %v, want %v", out.AudioTranscode, tc.want)
			}
		})
	}
}

func TestDecideSubtitleFlags(t *testing.T) {
	base := MediaInfo{Container: "mp4", VideoCodec: "h264", HasAudio: true, AudioCodec: "aac"}

	text := base
	text.HasSubtitle, text.SubtitleIsText = true, true
	if out := Decide(chrome(), text, ""); !out.SubtitleSidecar || out.SubtitleBurnIn {
		t.Fatalf("text subtitle: got sidecar=%v burnIn=%v, want sidecar=true burnIn=false", out.SubtitleSidecar, out.SubtitleBurnIn)
	}

	bitmap := base
	bitmap.HasSubtitle, bitmap.SubtitleIsText = true, false
	if out := Decide(chrome(), bitmap, ""); out.SubtitleSidecar || !out.SubtitleBurnIn {
		t.Fatalf("bitmap subtitle: got sidecar=%v burnIn=%v, want sidecar=false burnIn=true", out.SubtitleSidecar, out.SubtitleBurnIn)
	}
}

func TestProfile(t *testing.T) {
	tests := []struct {
		name    string
		hint    string
		ua      string
		want    string
		matched bool
	}{
		{"explicit hint", "web-safari", "", "web-safari", true},
		{"bad hint falls back to UA", "nonsense", "Mozilla/5.0 Firefox/120", "web-firefox", true},
		{"infer chrome", "", "Mozilla/5.0 Chrome/120 Safari/537", "web-chrome", true},
		{"infer safari", "", "Mozilla/5.0 Version/17 Safari/605", "web-safari", true},
		{"infer edge as chrome", "", "Mozilla/5.0 Chrome/120 Edg/120", "web-chrome", true},
		{"unknown UA defaults to chrome", "", "curl/8.0", "web-chrome", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, ok := Profile(tc.hint, tc.ua)
			if p.Name != tc.want {
				t.Fatalf("profile = %q, want %q", p.Name, tc.want)
			}
			if ok != tc.matched {
				t.Fatalf("matched = %v, want %v", ok, tc.matched)
			}
		})
	}
}
