package video

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"time"
)

// ffmpegSem bounds total concurrent ffprobe/ffmpeg processes (probe, remux, and
// on-demand HLS segment transcodes). nil means unlimited. Configured once at
// startup via SetConcurrency, before any use.
var (
	ffmpegSem     chan struct{}
	ffmpegTimeout time.Duration
)

// SetConcurrency configures the global ffprobe/ffmpeg concurrency cap. A limit
// of <= 0 means unlimited (no semaphore). acquireTimeout bounds how long work
// waits for a slot; <= 0 waits indefinitely (subject to the caller's context).
func SetConcurrency(limit int, acquireTimeout time.Duration) {
	if limit <= 0 {
		ffmpegSem = nil
	} else {
		ffmpegSem = make(chan struct{}, limit)
	}
	ffmpegTimeout = acquireTimeout
}

// acquire blocks for a concurrency slot and returns a release func. A nil
// semaphore (the default) means unlimited and never blocks.
func acquire(ctx context.Context) (func(), error) {
	if ffmpegSem == nil {
		return func() {}, nil
	}
	if ffmpegTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, ffmpegTimeout)
		defer cancel()
	}
	select {
	case ffmpegSem <- struct{}{}:
		return func() { <-ffmpegSem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type ProbeResult struct {
	Format   ProbeFormat    `json:"format"`
	Streams  []ProbeStream  `json:"streams"`
	Chapters []ProbeChapter `json:"chapters,omitempty"`
}

// ProbeChapter is an embedded chapter marker (start/end in seconds + its title).
// Powers marker-based intro/recap/commercial skipping (skipsegments.go).
type ProbeChapter struct {
	StartSec float64 `json:"start_sec"`
	EndSec   float64 `json:"end_sec"`
	Title    string  `json:"title"`
}

type ProbeFormat struct {
	Duration string `json:"duration"`
	Size     string `json:"size"`
	BitRate  string `json:"bit_rate"`
}

type ProbeStream struct {
	Index     int    `json:"index"`
	CodecType string `json:"codec_type"`
	CodecName string `json:"codec_name"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Channels  int    `json:"channels"`
	Language  string
	Title     string
	IsDefault bool
}

// rawProbeStream matches ffprobe's JSON output, including nested tags/disposition.
type rawProbeStream struct {
	Index     int    `json:"index"`
	CodecType string `json:"codec_type"`
	CodecName string `json:"codec_name"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Channels  int    `json:"channels"`
	Tags      struct {
		Language string `json:"language"`
		Title    string `json:"title"`
	} `json:"tags"`
	Disposition struct {
		Default int `json:"default"`
	} `json:"disposition"`
}

type rawProbeChapter struct {
	StartTime string `json:"start_time"`
	EndTime   string `json:"end_time"`
	Tags      struct {
		Title string `json:"title"`
	} `json:"tags"`
}

type rawProbeResult struct {
	Format   ProbeFormat       `json:"format"`
	Streams  []rawProbeStream  `json:"streams"`
	Chapters []rawProbeChapter `json:"chapters"`
}

func Probe(ctx context.Context, filePath string) (*ProbeResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	release, err := acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("ffprobe acquire slot: %w", err)
	}
	defer release()

	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "quiet",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		"-show_chapters",
		filePath,
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe %s: %w", filePath, err)
	}

	return parseProbeJSON(out)
}

func parseProbeJSON(data []byte) (*ProbeResult, error) {
	var raw rawProbeResult
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse ffprobe json: %w", err)
	}
	result := &ProbeResult{
		Format:  raw.Format,
		Streams: make([]ProbeStream, len(raw.Streams)),
	}
	for i, s := range raw.Streams {
		result.Streams[i] = ProbeStream{
			Index:     s.Index,
			CodecType: s.CodecType,
			CodecName: s.CodecName,
			Width:     s.Width,
			Height:    s.Height,
			Channels:  s.Channels,
			Language:  s.Tags.Language,
			Title:     s.Tags.Title,
			IsDefault: s.Disposition.Default == 1,
		}
	}
	for _, c := range raw.Chapters {
		start, err1 := strconv.ParseFloat(c.StartTime, 64)
		end, err2 := strconv.ParseFloat(c.EndTime, 64)
		if err1 != nil || err2 != nil || end <= start {
			continue
		}
		result.Chapters = append(result.Chapters, ProbeChapter{StartSec: start, EndSec: end, Title: c.Tags.Title})
	}
	return result, nil
}
