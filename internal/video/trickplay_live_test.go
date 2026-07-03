package video

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestGenerateTrickplayLive exercises real ffmpeg on the file named by
// HESPERA_TRICKPLAY_SRC (skipped otherwise) — the measurement gate for the
// generation-cost question: logs wall time and on-disk size.
func TestGenerateTrickplayLive(t *testing.T) {
	src := os.Getenv("HESPERA_TRICKPLAY_SRC")
	if src == "" {
		t.Skip("set HESPERA_TRICKPLAY_SRC=<video file> to run the live trickplay generation test")
	}
	SetConcurrency(4, 10*time.Second)
	out := t.TempDir()
	t0 := time.Now()
	if err := GenerateTrickplay(context.Background(), src, out); err != nil {
		t.Fatalf("GenerateTrickplay: %v", err)
	}
	elapsed := time.Since(t0)

	b, err := os.ReadFile(filepath.Join(out, "manifest.json"))
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	var m TrickplayManifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("manifest parse: %v", err)
	}
	if m.Width <= 0 || m.Height <= 0 || m.Frames <= 0 || m.Tile != trickplayTile {
		t.Fatalf("manifest values wrong: %+v", m)
	}
	sprites, _ := filepath.Glob(filepath.Join(out, "sprite*.jpg"))
	if len(sprites) == 0 {
		t.Fatal("no sprites")
	}
	var total int64
	for _, s := range sprites {
		if fi, err := os.Stat(s); err == nil {
			total += fi.Size()
		}
	}
	t.Logf("src=%s wall=%.1fs sprites=%d frames=%d tilepx=%dx%d disk=%.1fKB",
		filepath.Base(src), elapsed.Seconds(), len(sprites), m.Frames, m.Width, m.Height, float64(total)/1024)
}
