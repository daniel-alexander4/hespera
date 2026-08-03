package podcast

import (
	"strings"
	"testing"
	"time"
)

const minimalFeed = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd">
  <channel>
    <title>Test Show</title>
    <description>A show.</description>
    <itunes:author>Someone</itunes:author>
    <link>https://example.com/</link>
    <itunes:image href="https://example.com/cover.jpg"/>
    <item>
      <title>Episode One</title>
      <guid isPermaLink="false">ep-1</guid>
      <pubDate>Tue, 15 Jul 2025 09:00:00 +0000</pubDate>
      <itunes:duration>1:02:03</itunes:duration>
      <enclosure url="https://cdn.example.com/1.mp3" type="audio/mpeg" length="12345"/>
    </item>
  </channel>
</rss>`

// messyFeed is the realistic case: the spec is a suggestion. Every deviation
// below is one seen on real feeds — a bare <image><url>, a duration in plain
// seconds, a single-digit day, double-escaped HTML in the description, an item
// with no enclosure, an item with no title, an item whose audio is a file: URL,
// and an item with no GUID at all.
const messyFeed = `<rss version="2.0">
  <channel>
    <title>Messy &amp; Co.</title>
    <description>&lt;p&gt;Notes with &lt;b&gt;markup&lt;/b&gt; &amp;amp; entities&lt;/p&gt;</description>
    <image><url>https://example.com/rss-cover.png</url></image>
    <item>
      <title>Numbers</title>
      <pubDate>Tue, 1 Apr 2025 07:05 +0000</pubDate>
      <itunes:duration>930</itunes:duration>
      <itunes:episode>7</itunes:episode>
      <itunes:season>2</itunes:season>
      <enclosure url="https://cdn.example.com/n.mp3" type="audio/mpeg" length="notanumber"/>
    </item>
    <item>
      <title>No enclosure, should vanish</title>
    </item>
    <item>
      <enclosure url="https://cdn.example.com/untitled.mp3"/>
    </item>
    <item>
      <title>Hostile audio, should vanish</title>
      <enclosure url="file:///etc/passwd"/>
    </item>
    <item>
      <title>No guid</title>
      <itunes:duration>4:20</itunes:duration>
      <enclosure url="https://cdn.example.com/noguid.mp3"/>
    </item>
  </channel>
</rss>`

func TestParseFeedMinimal(t *testing.T) {
	f, err := ParseFeed([]byte(minimalFeed))
	if err != nil {
		t.Fatalf("ParseFeed: %v", err)
	}
	if f.Title != "Test Show" || f.Author != "Someone" {
		t.Errorf("channel fields: %+v", f)
	}
	if f.ImageURL != "https://example.com/cover.jpg" {
		t.Errorf("itunes:image href should win: %q", f.ImageURL)
	}
	if len(f.Episodes) != 1 {
		t.Fatalf("want 1 episode, got %d", len(f.Episodes))
	}
	e := f.Episodes[0]
	if e.GUID != "ep-1" || e.AudioURL != "https://cdn.example.com/1.mp3" || e.AudioBytes != 12345 {
		t.Errorf("episode: %+v", e)
	}
	if e.Duration != 3723 { // 1:02:03
		t.Errorf("duration: got %d, want 3723", e.Duration)
	}
	if e.Published.IsZero() || e.Published.Year() != 2025 {
		t.Errorf("published: %v", e.Published)
	}
}

// TestParseFeedSurvivesMess is the point of the parser. A feed that breaks the
// spec in six ways at once must still yield the episodes that are playable, and
// drop exactly the ones that are not.
func TestParseFeedSurvivesMess(t *testing.T) {
	f, err := ParseFeed([]byte(messyFeed))
	if err != nil {
		t.Fatalf("ParseFeed: %v", err)
	}
	if f.Title != "Messy & Co." {
		t.Errorf("entity in title not unescaped: %q", f.Title)
	}
	if strings.Contains(f.Description, "<") || !strings.Contains(f.Description, "markup") {
		t.Errorf("description should be de-marked-up prose, got %q", f.Description)
	}
	if f.ImageURL != "https://example.com/rss-cover.png" {
		t.Errorf("RSS <image><url> fallback failed: %q", f.ImageURL)
	}

	// Three items are unplayable and must be gone: no enclosure, no title,
	// and a file: URL.
	if len(f.Episodes) != 2 {
		var got []string
		for _, e := range f.Episodes {
			got = append(got, e.Title)
		}
		t.Fatalf("want 2 usable episodes, got %d: %v", len(f.Episodes), got)
	}

	n := f.Episodes[0]
	if n.Duration != 930 {
		t.Errorf("bare-seconds duration: got %d", n.Duration)
	}
	if n.EpisodeNo != 7 || n.SeasonNo != 2 {
		t.Errorf("episode/season: %+v", n)
	}
	if n.AudioBytes != 0 {
		t.Errorf("a non-numeric length must degrade to 0, got %d", n.AudioBytes)
	}
	if n.Published.IsZero() {
		t.Error("single-digit-day date should parse")
	}

	// A missing GUID falls back to the audio URL, which is what makes refresh
	// de-duplication work on feeds that never had one.
	last := f.Episodes[1]
	if last.GUID != "https://cdn.example.com/noguid.mp3" {
		t.Errorf("GUID fallback: %q", last.GUID)
	}
	if last.Duration != 260 { // 4:20
		t.Errorf("mm:ss duration: got %d", last.Duration)
	}
}

func TestParseDuration(t *testing.T) {
	cases := map[string]int{
		"":         0,
		"0":        0,
		"90":       90,
		"4:20":     260,
		"1:02:03":  3723,
		"01:00:00": 3600,
		"1830.5":   1830,
		"garbage":  0,
		"1:2:3:4":  0, // more than h:m:s is not a duration
		"-5":       0,
	}
	for in, want := range cases {
		if got := parseDuration(in); got != want {
			t.Errorf("parseDuration(%q) = %d, want %d", in, got, want)
		}
	}
}

// TestParseDateFallsBackToZero: an undated episode must sort to the bottom, not
// masquerade as the newest forever — which is what defaulting to "now" would do.
func TestParseDate(t *testing.T) {
	for _, s := range []string{
		"Tue, 15 Jul 2025 09:00:00 +0000",
		"Tue, 15 Jul 2025 09:00:00 GMT",
		"Tue, 1 Apr 2025 07:05 +0000",
		"2025-07-15T09:00:00Z",
		"2025-07-15",
	} {
		if parseDate(s).IsZero() {
			t.Errorf("%q should parse", s)
		}
	}
	for _, s := range []string{"", "   ", "not a date", "15/07/2025"} {
		if !parseDate(s).IsZero() {
			t.Errorf("%q should yield the zero time", s)
		}
	}
	if got := parseDate("Tue, 15 Jul 2025 09:00:00 +0000"); got.Location() != time.UTC {
		t.Errorf("dates should normalize to UTC, got %v", got.Location())
	}
}

func TestParseFeedRejectsNonFeeds(t *testing.T) {
	for _, s := range []string{
		"",
		"not xml at all",
		`{"json":true}`,
		`<html><body>a web page</body></html>`,
		`<rss><channel></channel></rss>`, // no title AND no items
	} {
		if _, err := ParseFeed([]byte(s)); err == nil {
			t.Errorf("%.30q should be rejected", s)
		}
	}
}

func TestCleanStripsMarkup(t *testing.T) {
	cases := map[string]string{
		"plain":                             "plain",
		"<p>hi</p>":                         "hi",
		"a &amp; b":                         "a & b",
		"  spaced   out  ":                  "spaced out",
		"<a href='x'>link</a> after":        "link after",
		"unclosed <tag":                     "unclosed", // safe-side: swallow the tail
		"&lt;p&gt;double escaped&lt;/p&gt;": "double escaped",
	}
	for in, want := range cases {
		if got := clean(in); got != want {
			t.Errorf("clean(%q) = %q, want %q", in, got, want)
		}
	}
}
