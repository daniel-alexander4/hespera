package video

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"
)

// ffmpegSem bounds concurrent ffprobe/ffmpeg processes. nil means unlimited.
// Configured once at startup via SetConcurrency, before any Probe call.
var (
	ffmpegSem     chan struct{}
	ffmpegTimeout time.Duration
)

// SetConcurrency configures the global ffprobe/ffmpeg concurrency cap. A limit
// of <= 0 means unlimited (no semaphore). acquireTimeout bounds how long Probe
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
	Format  ProbeFormat   `json:"format"`
	Streams []ProbeStream `json:"streams"`
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

type rawProbeResult struct {
	Format  ProbeFormat      `json:"format"`
	Streams []rawProbeStream `json:"streams"`
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
	return result, nil
}
