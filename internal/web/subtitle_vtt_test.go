package web

import "testing"

func TestClampVTTOverlapsNoChangeIsByteIdentical(t *testing.T) {
	in := "WEBVTT\n\nNOTE a comment\n\n00:00.000 --> 00:02.000\nHello\n\n00:02.000 --> 00:04.000\nWorld\n"
	out := clampVTTOverlaps([]byte(in))
	if string(out) != in {
		t.Fatalf("non-overlapping input must round-trip byte-for-byte.\n got: %q\nwant: %q", out, in)
	}
}

func TestClampVTTOverlapsClampsToNextStart(t *testing.T) {
	in := "WEBVTT\n\n00:00.000 --> 00:05.000\nA\n\n00:03.000 --> 00:06.000\nB\n"
	// A ends at 5.000 but B starts at 3.000 → A's end clamps to 3.000.
	want := "WEBVTT\n\n00:00.000 --> 00:03.000\nA\n\n00:03.000 --> 00:06.000\nB\n"
	if out := string(clampVTTOverlaps([]byte(in))); out != want {
		t.Fatalf("overlap not clamped.\n got: %q\nwant: %q", out, want)
	}
}

func TestClampVTTOverlapsPreservesCueSettings(t *testing.T) {
	in := "WEBVTT\n\n00:00.000 --> 00:05.000 align:start position:10%\nA\n\n00:02.000 --> 00:04.000\nB\n"
	want := "WEBVTT\n\n00:00.000 --> 00:02.000 align:start position:10%\nA\n\n00:02.000 --> 00:04.000\nB\n"
	if out := string(clampVTTOverlaps([]byte(in))); out != want {
		t.Fatalf("cue settings not preserved on clamp.\n got: %q\nwant: %q", out, want)
	}
}

func TestClampVTTOverlapsHourFormat(t *testing.T) {
	in := "WEBVTT\n\n01:00:00.000 --> 01:00:10.000\nA\n\n01:00:04.000 --> 01:00:08.000\nB\n"
	want := "WEBVTT\n\n01:00:00.000 --> 01:00:04.000\nA\n\n01:00:04.000 --> 01:00:08.000\nB\n"
	if out := string(clampVTTOverlaps([]byte(in))); out != want {
		t.Fatalf("HH:MM:SS overlap not clamped.\n got: %q\nwant: %q", out, want)
	}
}

func TestClampVTTOverlapsChainAndMultiline(t *testing.T) {
	// Three overlapping cues in a row; multi-line payloads must be untouched.
	in := "WEBVTT\n\n00:00.000 --> 00:09.000\nfirst\nline\n\n00:03.000 --> 00:09.000\nsecond\n\n00:06.000 --> 00:09.000\nthird\n"
	want := "WEBVTT\n\n00:00.000 --> 00:03.000\nfirst\nline\n\n00:03.000 --> 00:06.000\nsecond\n\n00:06.000 --> 00:09.000\nthird\n"
	if out := string(clampVTTOverlaps([]byte(in))); out != want {
		t.Fatalf("chained overlaps not clamped.\n got: %q\nwant: %q", out, want)
	}
}

func TestClampVTTOverlapsSkipsOutOfOrder(t *testing.T) {
	// Next cue starts before the current one (unsorted) — never clamp to a smaller
	// value (would invert the cue); leave byte-identical.
	in := "WEBVTT\n\n00:05.000 --> 00:08.000\nA\n\n00:01.000 --> 00:02.000\nB\n"
	if out := string(clampVTTOverlaps([]byte(in))); out != in {
		t.Fatalf("out-of-order pair must be left unchanged.\n got: %q\nwant: %q", out, in)
	}
}

func TestParseVTTMillis(t *testing.T) {
	cases := []struct {
		tok string
		ms  int
		ok  bool
	}{
		{"00:00.000", 0, true},
		{"00:05.653", 5653, true},
		{"01:02:03.004", 3723004, true},
		{"10:00.000", 600000, true},
		{"bad", 0, false},
		{"00:00.00", 0, false}, // millis must be 3 digits
	}
	for _, c := range cases {
		ms, ok := parseVTTMillis(c.tok)
		if ok != c.ok || (ok && ms != c.ms) {
			t.Errorf("parseVTTMillis(%q) = (%d,%v), want (%d,%v)", c.tok, ms, ok, c.ms, c.ok)
		}
	}
}
