package playback

import (
	"strconv"
	"strings"

	"isomedia/internal/video"
)

// textSubtitleCodecs are subtitle codecs that can be extracted to a text sidecar
// (WebVTT). Anything not listed — PGS/DVD/DVB bitmap subs, or unknown — fails
// safe to burn-in.
var textSubtitleCodecs = set(
	"subrip", "srt", "ass", "ssa", "webvtt", "mov_text", "text", "eia_608", "eia_708",
)

// FromProbe resolves the SELECTED tracks of a probed source into a MediaInfo.
// audioOrdinal/subOrdinal are 1-based among streams of that type; 0 selects the
// default (or first) audio track and selects no subtitle. An out-of-range audio
// ordinal falls back to the default; an out-of-range subtitle ordinal selects
// none (so a stale/bogus index never forces a needless transcode).
func FromProbe(p *video.ProbeResult, container string, fileSizeBytes int64, audioOrdinal, subOrdinal int) MediaInfo {
	m := MediaInfo{Container: strings.ToLower(strings.TrimPrefix(strings.TrimSpace(container), "."))}
	if p == nil {
		return m
	}
	var audio, subs []video.ProbeStream
	for _, s := range p.Streams {
		switch strings.ToLower(s.CodecType) {
		case "video":
			// Largest frame wins, which skips tiny cover-art/thumbnail video streams.
			if m.VideoCodec == "" || s.Width*s.Height > m.VideoWidth*m.VideoHeight {
				m.VideoCodec = strings.ToLower(s.CodecName)
				m.VideoWidth = s.Width
				m.VideoHeight = s.Height
			}
		case "audio":
			audio = append(audio, s)
		case "subtitle":
			subs = append(subs, s)
		}
	}
	if len(audio) > 0 {
		m.HasAudio = true
		m.AudioCodec = strings.ToLower(pickStream(audio, audioOrdinal).CodecName)
	}
	if subOrdinal > 0 && subOrdinal <= len(subs) {
		m.HasSubtitle = true
		m.SubtitleIsText = textSubtitleCodecs[strings.ToLower(subs[subOrdinal-1].CodecName)]
	}
	m.BitrateBPS = bitrate(p.Format, fileSizeBytes)
	return m
}

// pickStream returns the 1-based ordinal stream, or the default/first when the
// ordinal is 0 or out of range.
func pickStream(streams []video.ProbeStream, ordinal int) video.ProbeStream {
	if ordinal >= 1 && ordinal <= len(streams) {
		return streams[ordinal-1]
	}
	for _, s := range streams {
		if s.IsDefault {
			return s
		}
	}
	return streams[0]
}

// bitrate prefers the container's declared bit_rate, falling back to
// size×8/duration when it is absent (common for MKV).
func bitrate(f video.ProbeFormat, fileSizeBytes int64) int64 {
	if b, err := strconv.ParseInt(strings.TrimSpace(f.BitRate), 10, 64); err == nil && b > 0 {
		return b
	}
	if dur, err := strconv.ParseFloat(strings.TrimSpace(f.Duration), 64); err == nil && dur > 0 && fileSizeBytes > 0 {
		return int64(float64(fileSizeBytes) * 8.0 / dur)
	}
	return 0
}
