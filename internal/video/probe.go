package video

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"
)

// ffmpegSem bounds total concurrent ffprobe/ffmpeg processes. bgSem is a
// sub-cap on long-running background builds (whole-file HLS transcodes), held
// below the global limit so interactive work — probe, remux, live transcode —
// always keeps headroom and can't be starved by background builds. nil means
// unlimited. Configured once at startup via SetConcurrency, before any use.
var (
	ffmpegSem     chan struct{}
	bgSem         chan struct{}
	ffmpegTimeout time.Duration
)

// SetConcurrency configures the global ffprobe/ffmpeg concurrency cap. A limit
// of <= 0 means unlimited (no semaphore). acquireTimeout bounds how long
// foreground work waits for a slot; <= 0 waits indefinitely (subject to the
// caller's context). The background-build sub-cap is half the global limit (at
// least 1), reserving the remainder for interactive playback.
func SetConcurrency(limit int, acquireTimeout time.Duration) {
	if limit <= 0 {
		ffmpegSem, bgSem = nil, nil
	} else {
		ffmpegSem = make(chan struct{}, limit)
		bg := limit / 2
		if bg < 1 {
			bg = 1
		}
		bgSem = make(chan struct{}, bg)
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

// acquireBackground reserves a slot for a long-running background build. It
// takes a background slot (capped below the global limit) and then a global
// slot, so background builds can never consume the capacity reserved for
// interactive playback. It is not bounded by ffmpegTimeout — it waits, subject
// to ctx, rather than failing fast, since builds are not latency-sensitive.
func acquireBackground(ctx context.Context) (func(), error) {
	if ffmpegSem == nil {
		return func() {}, nil
	}
	select {
	case bgSem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case ffmpegSem <- struct{}{}:
		return func() { <-ffmpegSem; <-bgSem }, nil
	case <-ctx.Done():
		<-bgSem
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
