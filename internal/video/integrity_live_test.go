package video

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestRepairFileLive runs the REAL ffmpeg container-repair path against a COPY
// of a genuinely corrupt file. Gated on HESPERA_LIVE_FIXTURE (a path to a corrupt
// media file) so the normal suite never needs ffmpeg or a fixture. It copies the
// fixture into t.TempDir() and only ever operates on that copy.
func TestRepairFileLive(t *testing.T) {
	src := os.Getenv("HESPERA_LIVE_FIXTURE")
	if src == "" {
		t.Skip("set HESPERA_LIVE_FIXTURE=<corrupt media file> to run the live repair test")
	}
	copyPath := filepath.Join(t.TempDir(), "copy"+filepath.Ext(src))
	if err := copyFile(src, copyPath); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	before, err := CheckIntegrity(ctx, copyPath, false)
	if err != nil {
		t.Fatalf("pre-check: %v", err)
	}
	t.Logf("container errors before repair: %d", before)
	if before == 0 {
		t.Fatalf("fixture reports no container corruption — not a useful live test")
	}

	out, err := RepairFile(ctx, copyPath, true)
	if err != nil {
		t.Fatalf("RepairFile: %v", err)
	}
	t.Logf("outcome: status=%s replaced=%v detail=%q", out.Status, out.Replaced, out.Detail)
	if out.Status != "repaired" || !out.Replaced {
		t.Fatalf("expected the container corruption to be repaired, got %+v", out)
	}

	after, err := CheckIntegrity(ctx, copyPath, false)
	if err != nil {
		t.Fatalf("post-check: %v", err)
	}
	t.Logf("container errors after repair: %d", after)
	if after != 0 {
		t.Fatalf("container corruption survived the repair: %d errors", after)
	}

	// The deep decode should still find the bitstream corruption a remux cannot fix.
	deep, err := CheckIntegrity(ctx, copyPath, true)
	if err != nil {
		t.Fatalf("deep check: %v", err)
	}
	t.Logf("bitstream (decode) errors after repair: %d", deep)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
