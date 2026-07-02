// Package ytdlp resolves a song (artist + title) to candidate YouTube video ids
// by invoking the yt-dlp binary's search — a quota-free alternative to the
// YouTube Data API search (which costs 100 quota units/call, ~100 songs/day).
//
// It is an OPT-IN, off-by-default resolver: it works by scraping YouTube via
// yt-dlp, which may violate YouTube's Terms of Service, so it is gated behind a
// user setting and used only for personal, local playback. The binary is an
// optional runtime dependency — Available() reports whether it's installed, and
// callers fall back to the Data API path when it isn't.
package ytdlp

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// searchTimeout bounds a single yt-dlp invocation (a network call). Resolution is
// on-demand and permanently cached, so this is never on a hot path.
const searchTimeout = 25 * time.Second

// videoIDPattern is the exact YouTube video-id shape (11 url-safe chars). yt-dlp
// output is validated against it before use — the same guard the Data API path
// applies, so a stray output line can't inject anything into an iframe src.
var videoIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

// runSearch is the seam: it runs the search and returns yt-dlp's raw stdout.
// Production shells out (execSearch); tests replace it to exercise the parser
// without the binary.
var runSearch = execSearch

var (
	availOnce sync.Once
	availOK   bool
)

// Available reports whether the yt-dlp binary is on PATH. Probed once and cached;
// when false the opt-in resolver degrades gracefully to the Data API path.
func Available() bool {
	availOnce.Do(func() {
		_, err := exec.LookPath("yt-dlp")
		availOK = err == nil
	})
	return availOK
}

// Search returns up to n candidate YouTube video ids for "artist song", most
// relevant first. It uses `yt-dlp --flat-playlist` — yt-dlp's most robust mode,
// which lists search results without extracting each video (no signature
// deciphering, the part that rots fastest across YouTube changes). An empty slice
// means no result; a non-nil error means the tool failed (missing/rot/network),
// which the caller treats as "unavailable, try later", not a genuine no-match.
func Search(ctx context.Context, artist, song string, n int) ([]string, error) {
	if n < 1 {
		n = 1
	}
	q := strings.TrimSpace(artist + " " + song)
	if q == "" {
		return nil, nil
	}
	out, err := runSearch(ctx, q, n)
	if err != nil {
		return nil, err
	}
	return parseIDs(out, n), nil
}

// execSearch is the production runSearch: it invokes yt-dlp. The query is passed
// as a single argument (no shell), so it can't be interpreted or injected.
func execSearch(ctx context.Context, query string, n int) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "yt-dlp",
		"--flat-playlist",
		"--no-warnings",
		"--print", "id",
		fmt.Sprintf("ytsearch%d:%s", n, query),
	)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("yt-dlp search: %w", err)
	}
	return stdout.Bytes(), nil
}

// parseIDs extracts up to n valid video ids from yt-dlp's line-per-id stdout,
// dropping any line that isn't a well-formed id.
func parseIDs(out []byte, n int) []string {
	var ids []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		id := strings.TrimSpace(sc.Text())
		if videoIDPattern.MatchString(id) {
			ids = append(ids, id)
			if len(ids) >= n {
				break
			}
		}
	}
	return ids
}
