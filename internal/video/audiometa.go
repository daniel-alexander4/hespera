package video

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ProbeTags recovers the tag dictionary and reports whether an embedded cover
// (an attached-picture video stream) is present, using ffprobe. It is the
// fallback for audio files that the pure-Go tag reader rejects outright: ffprobe
// reads the container's metadata where a single malformed frame aborted the
// in-process parse. Tag keys are lowercased; format-level tags (typical for MP4)
// are merged with audio-stream tags (typical for FLAC/OGG Vorbis comments),
// format taking precedence. Gated through the shared ffmpeg concurrency cap.
func ProbeTags(ctx context.Context, filePath string) (tags map[string]string, hasArt bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	release, err := acquire(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("ffprobe acquire slot: %w", err)
	}
	defer release()

	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "quiet",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		filePath,
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, false, fmt.Errorf("ffprobe %s: %w", filePath, err)
	}
	return parseProbeTags(out)
}

type rawTagProbe struct {
	Format struct {
		Tags map[string]string `json:"tags"`
	} `json:"format"`
	Streams []struct {
		CodecType   string            `json:"codec_type"`
		Tags        map[string]string `json:"tags"`
		Disposition struct {
			AttachedPic int `json:"attached_pic"`
		} `json:"disposition"`
	} `json:"streams"`
}

func parseProbeTags(data []byte) (map[string]string, bool, error) {
	var raw rawTagProbe
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, false, fmt.Errorf("parse ffprobe json: %w", err)
	}
	tags := map[string]string{}
	// Audio-stream tags first, format tags last so format wins on conflict.
	hasArt := false
	for _, s := range raw.Streams {
		if s.Disposition.AttachedPic == 1 {
			hasArt = true
		}
		if s.CodecType == "audio" {
			mergeLowerTags(tags, s.Tags)
		}
	}
	mergeLowerTags(tags, raw.Format.Tags)
	return tags, hasArt, nil
}

func mergeLowerTags(dst, src map[string]string) {
	for k, v := range src {
		dst[strings.ToLower(strings.TrimSpace(k))] = v
	}
}

// ExtractCoverArt returns the bytes of an embedded cover (the file's
// attached-picture stream) by stream-copying it through ffmpeg. It is paired
// with ProbeTags for the reject-fallback path: when the tag reader fails but the
// container still carries a cover, this recovers it without re-encoding. Returns
// the raw image bytes (caller validates/normalizes). Gated through the shared
// ffmpeg concurrency cap.
func ExtractCoverArt(ctx context.Context, filePath string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	release, err := acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("ffmpeg acquire slot: %w", err)
	}
	defer release()

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-nostdin",
		"-v", "quiet",
		"-i", filePath,
		"-an",
		"-map", "0:v:0",
		"-c:v", "copy",
		"-frames:v", "1",
		"-f", "image2pipe",
		"pipe:1",
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffmpeg cover %s: %w", filePath, err)
	}
	return out, nil
}
