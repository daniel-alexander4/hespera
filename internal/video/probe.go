package video

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"
)

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
