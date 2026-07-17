package video

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

func TestParseSeekLanding(t *testing.T) {
	cases := []struct {
		name    string
		json    string
		t       float64
		want    float64
		wantErr bool
	}{
		{
			name: "keyframe at 8s for a request of 10s",
			json: `{"packets":[{"pts_time":"8.000000","flags":"K__"}],"format":{"start_time":"0.000000"}}`,
			t:    10, want: 8,
		},
		{
			name: "start_time subtracted (mpegts-style absolute pts)",
			json: `{"packets":[{"pts_time":"13.423222","flags":"K__"}],"format":{"start_time":"1.400000"}}`,
			t:    15, want: 12.023222,
		},
		{
			name: "dts fallback when pts is N/A",
			json: `{"packets":[{"pts_time":"N/A","dts_time":"4.000000","flags":"K__"}],"format":{}}`,
			t:    6, want: 4,
		},
		{
			name: "landing exactly at the request is valid",
			json: `{"packets":[{"pts_time":"10.000000","flags":"K__"}],"format":{}}`,
			t:    10, want: 10,
		},
		{
			name: "tiny negative after start_time subtraction clamps to 0",
			json: `{"packets":[{"pts_time":"1.383222","flags":"K__"}],"format":{"start_time":"1.400000"}}`,
			t:    5, want: 0,
		},
		{
			name: "non-keyframe first packet is an error (irreproducible seek)",
			json: `{"packets":[{"pts_time":"9.989889","flags":"___"}],"format":{}}`,
			t:    10, wantErr: true,
		},
		{
			name: "landing past the request is an error (mpegts overshoot)",
			json: `{"packets":[{"pts_time":"13.423222","flags":"K__"}],"format":{}}`,
			t:    10, wantErr: true,
		},
		{
			name: "no packets is an error",
			json: `{"packets":[],"format":{}}`,
			t:    10, wantErr: true,
		},
		{
			name: "unparseable packet time is an error",
			json: `{"packets":[{"pts_time":"N/A","dts_time":"N/A","flags":"K__"}],"format":{}}`,
			t:    10, wantErr: true,
		},
		{
			name: "garbage json is an error",
			json: `not json`,
			t:    10, wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseSeekLanding([]byte(tc.json), tc.t)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if diff := got - tc.want; diff > 0.001 || diff < -0.001 {
				t.Fatalf("landing = %v, want %v", got, tc.want)
			}
		})
	}
}

// gopClip generates a clip with a forced GOP so keyframe positions are known
// exactly: 30fps with -g 120 puts keyframes at 0s, 4s, 8s, ...
func gopClip(t *testing.T, seconds int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gop.mp4")
	dur := strconv.Itoa(seconds)
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=duration="+dur+":size=320x240:rate=30",
		"-c:v", "libx264", "-g", "120", "-keyint_min", "120", "-sc_threshold", "0",
		"-an", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate gop clip: %v: %s", err, out)
	}
	return path
}

func TestSeekLandingRealClip(t *testing.T) {
	ffmpegAvailable(t)
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not installed; skipping integration test")
	}
	src := gopClip(t, 20)

	cases := []struct {
		req, want float64
	}{
		{10, 8},  // mid-GOP request snaps back to the previous keyframe
		{8, 8},   // a request exactly on a keyframe lands on it
		{2, 0},   // inside the first GOP lands at the very start
		{17, 16}, // last GOP
	}
	for _, tc := range cases {
		got, err := SeekLanding(context.Background(), src, tc.req)
		if err != nil {
			t.Fatalf("SeekLanding(%v): %v", tc.req, err)
		}
		if diff := got - tc.want; diff > 0.05 || diff < -0.05 {
			t.Fatalf("SeekLanding(%v) = %v, want %v", tc.req, got, tc.want)
		}
	}

	// The reported landing must match where ffmpeg's own -ss actually starts a
	// stream copy: remux from 10s of a 20s clip → the output carries 20-8=12s.
	outPath := filepath.Join(t.TempDir(), "cut.mp4")
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-ss", "10", "-i", src, "-c", "copy", "-avoid_negative_ts", "make_zero", outPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("remux cut: %v: %s", err, out)
	}
	probeOut, err := exec.Command("ffprobe", "-v", "error",
		"-show_entries", "format=duration", "-of", "csv=p=0", outPath).Output()
	if err != nil {
		t.Fatalf("probe cut: %v", err)
	}
	var cutDur float64
	if _, err := fmt.Sscanf(string(probeOut), "%f", &cutDur); err != nil {
		t.Fatalf("parse cut duration %q: %v", probeOut, err)
	}
	if diff := cutDur - 12; diff > 0.2 || diff < -0.2 {
		t.Fatalf("ffmpeg -ss 10 stream copy carries %.3fs; SeekLanding predicted a start of 8s (want ~12s of output)", cutDur)
	}
}
