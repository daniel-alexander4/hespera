package video

import (
	"context"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// FFmpegInfo reports whether ffmpeg is on PATH and its version, for the About
// page's health panel. Unlike the transcode paths it does NOT take an ffmpeg
// semaphore slot — `-version` spawns and exits instantly and is never on a hot
// path (only a manually-opened settings card hits it). Probed fresh each call
// (no cache), so a mid-session ffmpeg upgrade shows immediately — the whole
// point of a "did my upgrade take?" health check.
func FFmpegInfo(ctx context.Context) (present bool, version string, major int) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-version").Output()
	if err != nil {
		return false, "", 0
	}
	version = parseFFmpegVersion(string(out))
	return true, version, majorOf(version)
}

// ffmpegVersionRe pulls the version token out of ffmpeg's first line. ffmpeg
// prints "ffmpeg version <token> ...", where <token> varies wildly by build:
// "7.1.1", "6.1.1-3ubuntu5" (distro), "n7.1", "N-109421-g..." (git snapshot).
var ffmpegVersionRe = regexp.MustCompile(`(?m)^ffmpeg version (\S+)`)

// numberRe grabs the leading numeric X.Y[.Z] from a messy version token.
var numberRe = regexp.MustCompile(`(\d+)\.(\d+)(?:\.(\d+))?`)

func parseFFmpegVersion(out string) string {
	m := ffmpegVersionRe.FindStringSubmatch(out)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// majorOf extracts the numeric major version from a token like "6.1.1-3ubuntu5"
// or "n7.1"; 0 when no X.Y number is present (a git snapshot like "N-109421").
func majorOf(token string) int {
	m := numberRe.FindStringSubmatch(strings.TrimPrefix(token, "n"))
	if len(m) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}
