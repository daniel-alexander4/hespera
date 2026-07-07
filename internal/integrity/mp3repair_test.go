package integrity

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	isodb "hespera/internal/db"
	"hespera/internal/video"

	_ "modernc.org/sqlite"
)

func TestContainerRepairable(t *testing.T) {
	for _, p := range []string{"a.flac", "a.m4a", "a.ogg", "a.opus", "a.wav", "a.mkv", "a.mp4", "dir/Song.FLAC"} {
		if !containerRepairable(p) {
			t.Errorf("%s should be container-repairable", p)
		}
	}
	// Raw MP3 has no container — must never be remux-repaired (any case).
	for _, p := range []string{"a.mp3", "A.MP3", "x/y/Song.Mp3"} {
		if containerRepairable(p) {
			t.Errorf("%s (mp3) must NOT be container-repaired", p)
		}
	}
}

// TestRepairOneSkipsContainerRepairForMP3 is the regression guard on the churn
// fix: the cheap tier must invoke the container check/remux for real-container
// formats and skip it entirely for MP3 (whose tolerant demux false-flags framing
// noise as corruption, so remuxing rewrites the user's file for nothing). Uses a
// seam spy — no ffmpeg needed; the audits on the non-existent fixture paths
// harmlessly return empty, and the row UPDATE lands in a temp DB.
func TestRepairOneSkipsContainerRepairForMP3(t *testing.T) {
	var calledWith []string
	orig := repairFileFn
	repairFileFn = func(_ context.Context, path string, _ bool) (video.RepairOutcome, error) {
		calledWith = append(calledWith, path)
		return video.RepairOutcome{Status: "ok"}, nil
	}
	defer func() { repairFileFn = orig }()

	conn, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "t.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := isodb.Migrate(conn); err != nil {
		t.Fatal(err)
	}
	// No rows needed: repairOne's final UPDATE is a harmless no-op on a missing
	// id, and the audits on these non-existent paths return empty. The test only
	// observes whether the container-repair seam is invoked.
	ctx := context.Background()
	repairOne(ctx, conn, "music_tracks", 1, "/x/a.mp3", true)
	repairOne(ctx, conn, "music_tracks", 2, "/x/a.flac", true)

	if contains(calledWith, "/x/a.mp3") {
		t.Fatalf("MP3 must NOT reach the container repair; calls=%v", calledWith)
	}
	if !contains(calledWith, "/x/a.flac") {
		t.Fatalf("FLAC should reach the container repair; calls=%v", calledWith)
	}
}

func contains(ss []string, v string) bool {
	for _, s := range ss {
		if s == v {
			return true
		}
	}
	return false
}
