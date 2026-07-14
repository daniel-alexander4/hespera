// Package playback decides, per client, whether a TV source can be played
// directly, remuxed (direct-stream), or must be transcoded — and over which
// protocol. It is a pure decision layer: no HTTP, no DB, no ffmpeg. The web
// layer resolves a MediaInfo (from a probed video.ProbeResult and the selected
// tracks), picks a ClientProfile, and acts on the returned Decision.
//
// The matrix fails safe toward Transcode: any unknown or unsupported container
// or codec — or any uncertainty — degrades to transcoding (which always plays)
// rather than risking a broken direct-play. A wrong "direct-play" is a black
// screen; a wrong "transcode" is merely slower.
package playback

import "strings"

// Decision is the chosen playback strategy.
type Decision string

const (
	DirectPlay   Decision = "direct_play"   // serve the file as-is
	DirectStream Decision = "direct_stream" // repackage the container, keep codecs (remux)
	Transcode    Decision = "transcode"     // re-encode video and/or audio
)

// Protocol is how the chosen strategy reaches the client.
type Protocol string

const (
	ProtocolFile Protocol = "file" // a single byte stream (direct play / remux)
	ProtocolHLS  Protocol = "hls"  // segmented adaptive stream (transcode)
)

// Reason is a machine-readable explanation for a decision, useful for telemetry
// and a playback-debug view.
type Reason string

const (
	ReasonContainerUnsupported   Reason = "container_unsupported_for_client"
	ReasonVideoCodecUnsupported  Reason = "video_codec_unsupported_for_client"
	ReasonAudioCodecUnsupported  Reason = "audio_codec_unsupported_for_client"
	ReasonSubtitleRequiresBurnIn Reason = "subtitle_requires_burn_in"
	ReasonResolutionTooHigh      Reason = "video_resolution_too_high_for_client"
	ReasonBitrateTooHigh         Reason = "source_bitrate_too_high_for_client"
	ReasonRemuxToSupported       Reason = "remux_to_supported_container"
	ReasonForcedMode             Reason = "forced_mode_override"
	ReasonUnknownClientFallback  Reason = "unknown_client_profile_fallback"
)

// MediaInfo describes the SELECTED tracks of a source for one playback request.
// Track selection is resolved up front (see FromProbe) so the decider is a pure
// function of already-chosen tracks rather than re-deriving them.
type MediaInfo struct {
	Container      string
	VideoCodec     string
	VideoWidth     int
	VideoHeight    int
	HasAudio       bool
	AudioCodec     string
	BitrateBPS     int64
	HasSubtitle    bool // a subtitle track was explicitly selected
	SubtitleIsText bool // selected subtitle is text (sidecar-able) vs bitmap (burn-in)
}

// ContainerCaps is the set of video/audio codecs a client can play inside one
// specific container. Modelling support per-container — rather than three
// independent codec sets — is what lets the decider reject invalid combinations
// like Opus-in-MP4 while still allowing Opus-in-WebM.
type ContainerCaps struct {
	Video map[string]bool
	Audio map[string]bool
}

// ClientProfile is a client's playback capability matrix.
type ClientProfile struct {
	Name             string
	Containers       map[string]ContainerCaps
	RemuxTarget      string // container to remux into for direct-stream (e.g. "mp4")
	MaxWidth         int    // 0 = no cap
	MaxHeight        int    // 0 = no cap
	MaxBitrateBPS    int64  // 0 = no cap
	CompatProtocol   Protocol
	AllowTextSidecar bool // deliver text subtitles as a sidecar instead of burning in
}

// Output is the decision plus its rationale and subtitle handling.
type Output struct {
	Decision        Decision
	Protocol        Protocol
	Reasons         []Reason
	SubtitleSidecar bool // selected text subtitle delivered as a sidecar
	SubtitleBurnIn  bool // selected subtitle must be burned in (forces transcode)
	// AudioTranscode marks a DirectStream whose audio must be re-encoded to AAC
	// while the video is still stream-copied — the middle gear between "copy
	// everything" and "re-encode everything". See RemuxAudioNeedsTranscode.
	AudioTranscode bool
}

// Decide chooses the playback strategy for the selected tracks under a client
// profile. modeOverride forces a decision when non-empty ("direct"/"direct_play",
// "direct_stream", "transcode"/"transcoded"/"compat"); "auto"/"" decides normally.
func Decide(p ClientProfile, m MediaInfo, modeOverride string) Output {
	sidecar := m.HasSubtitle && m.SubtitleIsText && p.AllowTextSidecar
	burnIn := m.HasSubtitle && !sidecar

	switch normalizeMode(modeOverride) {
	case "direct", "direct_play":
		return Output{Decision: DirectPlay, Protocol: ProtocolFile, Reasons: []Reason{ReasonForcedMode}, SubtitleSidecar: sidecar}
	case "direct_stream":
		return Output{
			Decision: DirectStream, Protocol: ProtocolFile, Reasons: []Reason{ReasonForcedMode}, SubtitleSidecar: sidecar,
			AudioTranscode: RemuxAudioNeedsTranscode(p, m.AudioCodec, m.HasAudio),
		}
	case "transcode", "transcoded", "compat", "compatibility":
		return Output{Decision: Transcode, Protocol: p.CompatProtocol, Reasons: []Reason{ReasonForcedMode}, SubtitleSidecar: sidecar, SubtitleBurnIn: burnIn}
	}

	container := strings.ToLower(strings.TrimSpace(m.Container))
	video := strings.ToLower(strings.TrimSpace(m.VideoCodec))
	audio := strings.ToLower(strings.TrimSpace(m.AudioCodec))

	reasons := make([]Reason, 0, 4)
	if burnIn {
		reasons = append(reasons, ReasonSubtitleRequiresBurnIn)
	}
	if caps, ok := p.Containers[container]; !ok {
		reasons = append(reasons, ReasonContainerUnsupported)
	} else {
		if !caps.Video[video] {
			reasons = append(reasons, ReasonVideoCodecUnsupported)
		}
		if m.HasAudio && !caps.Audio[audio] {
			reasons = append(reasons, ReasonAudioCodecUnsupported)
		}
	}
	resTooHigh := p.MaxWidth > 0 && p.MaxHeight > 0 && (m.VideoWidth > p.MaxWidth || m.VideoHeight > p.MaxHeight)
	if resTooHigh {
		reasons = append(reasons, ReasonResolutionTooHigh)
	}
	bitrateTooHigh := p.MaxBitrateBPS > 0 && m.BitrateBPS > p.MaxBitrateBPS
	if bitrateTooHigh {
		reasons = append(reasons, ReasonBitrateTooHigh)
	}

	out := Output{SubtitleSidecar: sidecar, SubtitleBurnIn: burnIn}
	switch {
	case len(reasons) == 0:
		out.Decision, out.Protocol = DirectPlay, ProtocolFile
	case !burnIn && !resTooHigh && !bitrateTooHigh && canRemux(p, video):
		// The video is client-compatible, so it can be stream-copied into the
		// remux target; only the packaging (and possibly the audio) is wrong.
		reasons = append(reasons, ReasonRemuxToSupported)
		out.Decision, out.Protocol = DirectStream, ProtocolFile
		out.AudioTranscode = RemuxAudioNeedsTranscode(p, audio, m.HasAudio)
	default:
		out.Decision, out.Protocol = Transcode, p.CompatProtocol
	}
	out.Reasons = reasons
	return out
}

// canRemux reports whether the source's video can be repackaged, unchanged, into
// the profile's remux-target container. The audio is deliberately not part of
// this test: an audio codec the client can't play (or that the fragmented-MP4
// muxer can't carry) is re-encoded in place while the video is still copied —
// re-encoding the video because of the soundtrack is the waste this avoids.
func canRemux(p ClientProfile, video string) bool {
	caps, ok := p.Containers[p.RemuxTarget]
	return ok && caps.Video[video]
}

// fmp4CopyableAudio lists the audio codecs ffmpeg can stream-copy into the
// fragmented MP4 the remux path emits. AC-3/E-AC-3 are absent for a hard
// muxer reason, not a client one: the MP4 muxer must parse an audio packet
// before it can write the moov, but -movflags empty_moov writes the moov
// first, so the mux fails outright ("Cannot write moov atom before AC3
// packets"). Copying them is therefore never an option here, even for a client
// (Safari) whose profile says it could decode them.
var fmp4CopyableAudio = set("aac", "mp3", "flac")

// RemuxAudioNeedsTranscode reports whether a DirectStream must re-encode the
// selected audio track to AAC — because the client can't decode it, or because
// the fragmented-MP4 output can't carry it (see fmp4CopyableAudio).
func RemuxAudioNeedsTranscode(p ClientProfile, audioCodec string, hasAudio bool) bool {
	if !hasAudio {
		return false
	}
	audio := strings.ToLower(strings.TrimSpace(audioCodec))
	caps, ok := p.Containers[p.RemuxTarget]
	if !ok {
		return true
	}
	return !caps.Audio[audio] || !fmp4CopyableAudio[audio]
}

func normalizeMode(mode string) string {
	m := strings.ToLower(strings.TrimSpace(mode))
	if m == "auto" {
		return ""
	}
	return m
}
