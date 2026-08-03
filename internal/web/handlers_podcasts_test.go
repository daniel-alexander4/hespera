package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"hespera/internal/podcast"
)

// fakeFetcher stands in for the outbound client. Every podcast test uses one:
// this is the only vertical that contacts a host the user named, and a test
// that reached the real client would make a live request to whatever a fixture
// happened to contain.
type fakeFetcher struct {
	feed      *podcast.Feed
	feedErr   error
	gotURL    string
	gotRange  string
	body      string
	getErr    error
	getStatus int
}

func (f *fakeFetcher) FetchFeed(_ context.Context, rawURL string) (*podcast.Feed, error) {
	f.gotURL = rawURL
	return f.feed, f.feedErr
}

func (f *fakeFetcher) Get(_ context.Context, rawURL, rangeHdr string) (*http.Response, error) {
	f.gotURL, f.gotRange = rawURL, rangeHdr
	if f.getErr != nil {
		return nil, f.getErr
	}
	code := f.getStatus
	if code == 0 {
		code = http.StatusOK
	}
	return &http.Response{
		StatusCode: code,
		Header:     http.Header{"Content-Type": {"audio/mpeg"}, "Accept-Ranges": {"bytes"}},
		Body:       io.NopCloser(strings.NewReader(f.body)),
	}, nil
}

func podcastTestHandler(t *testing.T, f *fakeFetcher) (*Handler, *fakeFetcher) {
	t.Helper()
	h, _ := newTestHandler(t)
	if f == nil {
		f = &fakeFetcher{}
	}
	h.podcasts = f
	return h, f
}

func sampleFeed() *podcast.Feed {
	return &podcast.Feed{
		Title:  "Test Show",
		Author: "Someone",
		Episodes: []podcast.Episode{
			{GUID: "a", Title: "One", AudioURL: "https://cdn.example.com/1.mp3", Duration: 100},
			{GUID: "b", Title: "Two", AudioURL: "https://cdn.example.com/2.mp3", Duration: 200},
		},
	}
}

func TestPodcastSubscribeStoresFeed(t *testing.T) {
	h, f := podcastTestHandler(t, &fakeFetcher{feed: sampleFeed()})

	id, err := h.subscribeFeed(context.Background(), "https://example.com/feed.xml")
	if err != nil {
		t.Fatalf("subscribeFeed: %v", err)
	}
	if f.gotURL != "https://example.com/feed.xml" {
		t.Errorf("fetched %q", f.gotURL)
	}
	eps, err := h.loadEpisodes(context.Background(), id)
	if err != nil || len(eps) != 2 {
		t.Fatalf("episodes: %d, %v", len(eps), err)
	}
}

// TestPodcastSubscribeIsIdempotent: feed_url carries a UNIQUE, so re-subscribing
// must refresh in place rather than fail or create a second copy — and the
// episode ids must survive, because playback progress hangs off them.
func TestPodcastSubscribeIsIdempotent(t *testing.T) {
	h, _ := podcastTestHandler(t, &fakeFetcher{feed: sampleFeed()})
	ctx := context.Background()

	first, err := h.subscribeFeed(ctx, "https://example.com/feed.xml")
	if err != nil {
		t.Fatal(err)
	}
	before, _ := h.loadEpisodes(ctx, first)

	second, err := h.subscribeFeed(ctx, "https://example.com/feed.xml")
	if err != nil {
		t.Fatalf("re-subscribe: %v", err)
	}
	if second != first {
		t.Fatalf("re-subscribe created a second show: %d then %d", first, second)
	}
	after, _ := h.loadEpisodes(ctx, second)
	if len(after) != len(before) {
		t.Fatalf("episode count changed: %d then %d", len(before), len(after))
	}
	if before[0].ID != after[0].ID {
		t.Errorf("episode id changed across refresh (%d → %d): progress would be orphaned",
			before[0].ID, after[0].ID)
	}
}

func TestPodcastSubscribeRejectsUnusableURLs(t *testing.T) {
	h, _ := podcastTestHandler(t, &fakeFetcher{feed: sampleFeed()})

	for _, bad := range []string{"file:///etc/passwd", "javascript:alert(1)", "data:text/xml,<rss/>", "", "   "} {
		form := url.Values{"feed_url": {bad}}
		r := httptest.NewRequest(http.MethodPost, "/podcasts/subscribe", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		h.podcastSubscribe(w, r)

		if loc := w.Header().Get("Location"); !strings.Contains(loc, "error=") {
			t.Errorf("%q should redirect with an error, got %q", bad, loc)
		}
	}
	var n int
	if err := h.db.QueryRow("SELECT COUNT(*) FROM podcasts").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("a rejected URL created %d subscription(s)", n)
	}
}

// TestPodcastSubscribeRejectsEmptyFeed: a feed that parses but has nothing
// playable is not a subscription anyone wants, and storing it would leave a
// permanently empty show on the page.
func TestPodcastSubscribeRejectsEmptyFeed(t *testing.T) {
	h, _ := podcastTestHandler(t, &fakeFetcher{feed: &podcast.Feed{Title: "Empty"}})
	if _, err := h.subscribeFeed(context.Background(), "https://example.com/f"); err == nil {
		t.Fatal("an episode-less feed was accepted")
	}
}

// TestStreamEpisodeTakesAnIDNotAURL is the anti-open-proxy property. The route
// resolves an episode id to a stored, already-validated URL; nothing in the
// request can name a destination.
func TestStreamEpisodeTakesAnIDNotAURL(t *testing.T) {
	h, f := podcastTestHandler(t, &fakeFetcher{feed: sampleFeed(), body: "AUDIO"})
	id, err := h.subscribeFeed(context.Background(), "https://example.com/feed.xml")
	if err != nil {
		t.Fatal(err)
	}
	eps, _ := h.loadEpisodes(context.Background(), id)

	// An attacker-supplied URL riding along as a query parameter must be
	// ignored entirely — the destination comes from the row.
	r := httptest.NewRequest(http.MethodGet,
		"/stream/episode/"+strconv.FormatInt(eps[0].ID, 10)+"?url=http://169.254.169.254/latest/meta-data", nil)
	r.Header.Set("Range", "bytes=10-20")
	w := httptest.NewRecorder()
	h.streamPodcastEpisode(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if strings.Contains(f.gotURL, "169.254") {
		t.Fatalf("the request steered the destination: %q", f.gotURL)
	}
	if !strings.HasPrefix(f.gotURL, "https://cdn.example.com/") {
		t.Errorf("fetched %q, want the stored audio URL", f.gotURL)
	}
	if f.gotRange != "bytes=10-20" {
		t.Errorf("Range not forwarded: %q — seeking would break", f.gotRange)
	}
	if w.Body.String() != "AUDIO" {
		t.Errorf("body: %q", w.Body.String())
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("nosniff missing on proxied bytes")
	}
}

func TestStreamEpisodeUnknownID(t *testing.T) {
	h, _ := podcastTestHandler(t, nil)
	r := httptest.NewRequest(http.MethodGet, "/stream/episode/9999", nil)
	w := httptest.NewRecorder()
	h.streamPodcastEpisode(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", w.Code)
	}
}

// TestStreamEpisodeSurfacesABlockedAddress: if a feed's audio host later starts
// resolving inward, the listener should be told it was refused rather than
// shown a generic gateway error.
func TestStreamEpisodeSurfacesABlockedAddress(t *testing.T) {
	h, f := podcastTestHandler(t, &fakeFetcher{feed: sampleFeed()})
	id, _ := h.subscribeFeed(context.Background(), "https://example.com/feed.xml")
	eps, _ := h.loadEpisodes(context.Background(), id)
	f.getErr = podcast.ErrBlockedAddress

	r := httptest.NewRequest(http.MethodGet, "/stream/episode/"+strconv.FormatInt(eps[0].ID, 10), nil)
	w := httptest.NewRecorder()
	h.streamPodcastEpisode(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403", w.Code)
	}
}

func TestPodcastSessionIsSyntheticAndDirect(t *testing.T) {
	h, _ := podcastTestHandler(t, &fakeFetcher{feed: sampleFeed()})
	id, _ := h.subscribeFeed(context.Background(), "https://example.com/feed.xml")
	eps, _ := h.loadEpisodes(context.Background(), id)

	r := httptest.NewRequest(http.MethodGet, "/podcast/playback-session?file="+strconv.FormatInt(eps[0].ID, 10), nil)
	w := httptest.NewRecorder()
	h.podcastPlaybackSession(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["protocol"] != "file" {
		t.Errorf("protocol: %v — a remote episode has no HLS or remux path", body["protocol"])
	}
	src, _ := body["url"].(string)
	if !strings.HasPrefix(src, "/stream/episode/") {
		t.Errorf("source must be the local proxy, got %q", src)
	}
	if strings.Contains(src, "cdn.example.com") {
		t.Error("the session handed the remote URL to the browser — the proxy exists to prevent exactly that")
	}
}

// TestPodcastProgressIsEarnOnly: the client reports completed=false whenever it
// has not personally seen this playthrough finish, so a blind assignment would
// revoke the tick every time an episode was merely opened.
func TestPodcastProgressIsEarnOnly(t *testing.T) {
	h, _ := podcastTestHandler(t, &fakeFetcher{feed: sampleFeed()})
	id, _ := h.subscribeFeed(context.Background(), "https://example.com/feed.xml")
	eps, _ := h.loadEpisodes(context.Background(), id)
	epID := eps[0].ID

	post := func(pos float64, done bool) {
		b, _ := json.Marshal(map[string]any{
			"file_id": epID, "position_seconds": pos, "duration_seconds": 100.0, "completed": done,
		})
		r := httptest.NewRequest(http.MethodPost, "/podcast/playback-progress", strings.NewReader(string(b)))
		w := httptest.NewRecorder()
		h.podcastPlaybackProgress(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("progress post: %d %s", w.Code, w.Body)
		}
	}

	post(99, true) // finished it
	if _, _, done := h.loadPodcastProgress(context.Background(), epID); !done {
		t.Fatal("completed was not recorded")
	}
	post(3, false) // re-opened it later
	_, _, done := h.loadPodcastProgress(context.Background(), epID)
	if !done {
		t.Fatal("re-opening a finished episode revoked its completed flag")
	}
}
