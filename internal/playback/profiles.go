package playback

import "strings"

func set(values ...string) map[string]bool {
	m := make(map[string]bool, len(values))
	for _, v := range values {
		if k := strings.ToLower(strings.TrimSpace(v)); k != "" {
			m[k] = true
		}
	}
	return m
}

// profiles is the immutable table of known client capabilities. Built once at
// init and only read thereafter, so it is safe to share.
//
// Codec sets are deliberately conservative: only combinations confidently
// playable in <video> are listed, so anything omitted fails safe to transcode.
var profiles = map[string]ClientProfile{
	"web-chrome": {
		Name: "web-chrome",
		Containers: map[string]ContainerCaps{
			"mp4":  {Video: set("h264"), Audio: set("aac")},
			"webm": {Video: set("vp8", "vp9", "av1"), Audio: set("opus", "vorbis")},
		},
		RemuxTarget: "mp4", MaxWidth: 3840, MaxHeight: 2160, MaxBitrateBPS: 40_000_000,
		CompatProtocol: ProtocolHLS, AllowTextSidecar: true,
	},
	"web-firefox": {
		Name: "web-firefox",
		Containers: map[string]ContainerCaps{
			"mp4":  {Video: set("h264"), Audio: set("aac")},
			"webm": {Video: set("vp8", "vp9", "av1"), Audio: set("opus", "vorbis")},
		},
		RemuxTarget: "mp4", MaxWidth: 3840, MaxHeight: 2160, MaxBitrateBPS: 40_000_000,
		CompatProtocol: ProtocolHLS, AllowTextSidecar: true,
	},
	"web-safari": {
		Name: "web-safari",
		Containers: map[string]ContainerCaps{
			"mp4": {Video: set("h264", "hevc"), Audio: set("aac", "ac3", "eac3")},
		},
		RemuxTarget: "mp4", MaxWidth: 3840, MaxHeight: 2160, MaxBitrateBPS: 40_000_000,
		CompatProtocol: ProtocolHLS, AllowTextSidecar: true,
	},
}

const defaultProfileKey = "web-chrome"

// Profile returns the client profile for an explicit hint, else one inferred
// from the User-Agent. The bool is false when neither matched and the default
// (web-chrome) was substituted — callers may surface ReasonUnknownClientFallback.
func Profile(clientHint, userAgent string) (ClientProfile, bool) {
	if key := strings.ToLower(strings.TrimSpace(clientHint)); key != "" {
		if p, ok := profiles[key]; ok {
			return p, true
		}
	}
	if p, ok := profiles[inferFromUA(userAgent)]; ok {
		return p, true
	}
	return profiles[defaultProfileKey], false
}

func inferFromUA(ua string) string {
	s := strings.ToLower(ua)
	switch {
	case strings.Contains(s, "firefox"):
		return "web-firefox"
	case strings.Contains(s, "safari") && !strings.Contains(s, "chrome") && !strings.Contains(s, "chromium"):
		return "web-safari"
	case strings.Contains(s, "chrome"), strings.Contains(s, "chromium"), strings.Contains(s, "edg"):
		return "web-chrome"
	default:
		return ""
	}
}
