package web

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"hespera/internal/config"
	isodb "hespera/internal/db"
)

// Synthetic-library scale harness for the browse lists — the tool that tests
// the pending "true O(page) scale" gate ("a library where the O(N) group scan
// actually feels slow"). Two entry points:
//
//   - Benchmarks (not run by `go test ./...`): measure loadTVSeriesList /
//     loadMovieList per-page-view cost at 1k/10k/30k titles.
//       go test ./internal/web -bench BrowseScale -benchtime 5x -run xxx
//
//   - A gated generator that writes a full synthetic hespera.sqlite to feel
//     the pages in a real browser (mirrors the HESPERA_LIVE_FIXTURE pattern):
//       HESPERA_SYNTH_DB=/tmp/synth.sqlite go test ./internal/web -run TestGenerateSyntheticBrowseDB
//       HESPERA_NO_BROWSER=1 HESPERA_DATA_DIR=/tmp/synthdata HESPERA_DB_PATH=/tmp/synth.sqlite ./bin/hespera
//
// Only DB rows are synthesized — the browse lists never touch the filesystem
// (posters 404 to the placeholder), so no media files are needed.

// seedBrowseScale bulk-inserts nSeries matched series (epsPer episodes each),
// nMovies matched films, and their metadata-cache payloads. Names are assigned
// from a fixed-seed permutation so the alphabetical ORDER BY does real work
// rather than reading rows in insertion order.
func seedBrowseScale(tb testing.TB, db *sql.DB, nSeries, epsPer, nMovies int) {
	tb.Helper()
	tx, err := db.Begin()
	if err != nil {
		tb.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	res, err := tx.Exec("INSERT INTO libraries (name, type, root_path) VALUES ('Synth TV', 'tv', '/synth/tv')")
	if err != nil {
		tb.Fatalf("tv lib: %v", err)
	}
	tvLib, _ := res.LastInsertId()
	res, err = tx.Exec("INSERT INTO libraries (name, type, root_path) VALUES ('Synth Movies', 'movies', '/synth/movies')")
	if err != nil {
		tb.Fatalf("movie lib: %v", err)
	}
	movLib, _ := res.LastInsertId()

	fileStmt, err := tx.Prepare("INSERT INTO tv_series_files (library_id, abs_path, container, file_size_bytes, mtime_unix) VALUES (?, ?, 'mkv', 1, 1)")
	if err != nil {
		tb.Fatalf("prepare file: %v", err)
	}
	identStmt, err := tx.Prepare(`INSERT INTO tv_series_identities (file_id, provider, series_id, status, guessed_title, season_number, episode_numbers_csv)
		VALUES (?, 'tmdb', ?, 'matched', '', 1, ?)`)
	if err != nil {
		tb.Fatalf("prepare ident: %v", err)
	}
	tvCacheStmt, err := tx.Prepare("INSERT INTO tv_series_metadata_cache (entity_key, lang, payload_json) VALUES (?, 'en', ?)")
	if err != nil {
		tb.Fatalf("prepare tv cache: %v", err)
	}
	movStmt, err := tx.Prepare("INSERT INTO movie_files (library_id, abs_path, container, file_size_bytes, mtime_unix, tmdb_id, match_status) VALUES (?, ?, 'mkv', 1, 1, ?, 'matched')")
	if err != nil {
		tb.Fatalf("prepare movie: %v", err)
	}
	movCacheStmt, err := tx.Prepare("INSERT INTO movie_metadata_cache (entity_key, lang, payload_json) VALUES (?, 'en', ?)")
	if err != nil {
		tb.Fatalf("prepare movie cache: %v", err)
	}

	rng := rand.New(rand.NewSource(42))
	seriesNames := rng.Perm(nSeries)
	for i := 0; i < nSeries; i++ {
		sid := fmt.Sprint(100000 + i)
		for e := 1; e <= epsPer; e++ {
			fres, err := fileStmt.Exec(tvLib, fmt.Sprintf("/synth/tv/s%d/e%d.mkv", i, e))
			if err != nil {
				tb.Fatalf("tv file: %v", err)
			}
			fid, _ := fres.LastInsertId()
			if _, err := identStmt.Exec(fid, sid, fmt.Sprint(e)); err != nil {
				tb.Fatalf("ident: %v", err)
			}
		}
		payload := fmt.Sprintf(`{"name":"Synthetic Show %06d","poster_path":"/p%d.jpg","first_air_date":"%d-03-01"}`,
			seriesNames[i], i, 1960+i%65)
		if _, err := tvCacheStmt.Exec("show:"+sid, payload); err != nil {
			tb.Fatalf("tv cache: %v", err)
		}
	}

	movieNames := rng.Perm(nMovies)
	for i := 0; i < nMovies; i++ {
		tmdbID := 500000 + i
		if _, err := movStmt.Exec(movLib, fmt.Sprintf("/synth/movies/m%d.mkv", i), tmdbID); err != nil {
			tb.Fatalf("movie: %v", err)
		}
		payload := fmt.Sprintf(`{"title":"Synthetic Film %06d","release_date":"%d-06-15"}`, movieNames[i], 1950+i%75)
		if _, err := movCacheStmt.Exec(fmt.Sprintf("movie:%d", tmdbID), payload); err != nil {
			tb.Fatalf("movie cache: %v", err)
		}
	}

	if err := tx.Commit(); err != nil {
		tb.Fatalf("commit: %v", err)
	}
	// The scale indexes are chosen by the planner only after stats exist.
	if _, err := db.Exec("ANALYZE"); err != nil {
		tb.Fatalf("analyze: %v", err)
	}
}

// newBenchHandler is newTestHandler for testing.TB (benchmarks included).
func newBenchHandler(tb testing.TB) (*Handler, *sql.DB) {
	tb.Helper()
	dir := tb.TempDir()
	conn, err := isodb.Open(filepath.Join(dir, "bench.sqlite"))
	if err != nil {
		tb.Fatalf("open: %v", err)
	}
	tb.Cleanup(func() { _ = conn.Close() })
	if err := isodb.Migrate(conn); err != nil {
		tb.Fatalf("migrate: %v", err)
	}
	h, err := New(Deps{
		Cfg:      config.Config{DataDir: dir, MediaRoot: dir},
		DB:       conn,
		AssetsFS: stubAssetsFS(),
	})
	if err != nil {
		tb.Fatalf("New: %v", err)
	}
	return h, conn
}

func BenchmarkBrowseScaleTV(b *testing.B) {
	for _, size := range []int{1000, 10000, 30000} {
		b.Run(fmt.Sprintf("series=%d", size), func(b *testing.B) {
			h, db := newBenchHandler(b)
			seedBrowseScale(b, db, size, 10, 0)
			ctx := context.Background()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, _, _, err := h.loadTVSeriesList(ctx, 1); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
	// The worst cases at the top size: a deep page and a search filter.
	h, db := newBenchHandler(b)
	seedBrowseScale(b, db, 30000, 10, 0)
	ctx := context.Background()
	b.Run("series=30000/deep-page", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, _, _, err := h.loadTVSeriesList(ctx, 250); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkBrowseScaleMovies(b *testing.B) {
	for _, size := range []int{1000, 10000, 50000} {
		b.Run(fmt.Sprintf("movies=%d", size), func(b *testing.B) {
			h, db := newBenchHandler(b)
			seedBrowseScale(b, db, 0, 0, size)
			ctx := context.Background()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, _, _, err := h.loadMovieList(ctx, 1); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// TestGenerateSyntheticBrowseDB writes a migrated, fully-seeded synthetic DB to
// $HESPERA_SYNTH_DB (30k series × 10 eps + 50k movies) for feeling the browse
// pages in a real browser. Skipped unless the env var is set — a dev tool, not
// a test.
func TestGenerateSyntheticBrowseDB(t *testing.T) {
	path := os.Getenv("HESPERA_SYNTH_DB")
	if path == "" {
		t.Skip("set HESPERA_SYNTH_DB=<path> to generate a synthetic browse DB")
	}
	_ = os.Remove(path)
	conn, err := isodb.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer conn.Close()
	if err := isodb.Migrate(conn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	seedBrowseScale(t, conn, 30000, 10, 50000)
	t.Logf("synthetic DB written to %s (30k series ×10 eps, 50k movies)", path)
}
