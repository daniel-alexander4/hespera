package main

import (
	"testing"
	"time"
)

// 2026-08-03 is a Monday — the same Monday the live brownnoise-stop timer was
// scheduled for, so these fixtures line up with the schedule being replaced.
func at(day int, hh, mm int) time.Time {
	return time.Date(2026, 8, day, hh, mm, 0, 0, time.UTC)
}

// Spelled out rather than left to iota continuation: an omitted value in a Go
// const block repeats the previous EXPRESSION, so `tuesday` would silently be 3
// and every Tuesday assertion would quietly test Monday.
const (
	sunday  = 2 // 2026-08-02
	monday  = 3
	tuesday = 4
)

func TestParseHHMM(t *testing.T) {
	ok := map[string]int{"00:00": 0, "09:05": 545, "20:00": 1200, "23:59": 1439, " 10:00 ": 600, "8:30": 510}
	for in, want := range ok {
		got, err := parseHHMM(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("%q: got %d, want %d", in, got, want)
		}
	}
	// A schedule runs unattended, so anything merely plausible is rejected where
	// it is written rather than reinterpreted at 3am.
	for _, in := range []string{"", "2000", "20.00", "8:00pm", "24:00", "20:60", "-1:00", "20:5", "abc:00", "20:0a"} {
		if _, err := parseHHMM(in); err == nil {
			t.Errorf("%q should be rejected", in)
		}
	}
}

func TestWindowContainsSameDay(t *testing.T) {
	w := noiseWindow{Start: "13:00", End: "15:00"}
	cases := []struct {
		when time.Time
		want bool
	}{
		{at(monday, 12, 59), false},
		{at(monday, 13, 0), true}, // inclusive start
		{at(monday, 14, 30), true},
		{at(monday, 15, 0), false}, // exclusive end
	}
	for _, c := range cases {
		got, err := w.contains(c.when)
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("%s: got %v, want %v", c.when.Format("Mon 15:04"), got, c.want)
		}
	}
}

// TestWindowContainsWrapsMidnight is the shape actually in use: 20:00→10:00 is
// fourteen hours across a day boundary, and treating end<start as an empty
// range would silently mean no noise at all.
func TestWindowContainsWrapsMidnight(t *testing.T) {
	w := noiseWindow{Start: "20:00", End: "10:00"}
	cases := []struct {
		when time.Time
		want bool
	}{
		{at(sunday, 19, 59), false},
		{at(sunday, 20, 0), true},
		{at(sunday, 23, 59), true},
		{at(monday, 0, 0), true}, // past midnight, same occurrence
		{at(monday, 9, 59), true},
		{at(monday, 10, 0), false}, // exclusive end
		{at(monday, 12, 0), false},
	}
	for _, c := range cases {
		got, err := w.contains(c.when)
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("%s: got %v, want %v", c.when.Format("Mon 15:04"), got, c.want)
		}
	}
}

// TestWindowDaysNameTheStartDay: a Monday 20:00→10:00 window runs into Tuesday
// morning. Matching the morning half against Tuesday's own weekday would shift
// every overnight window a day late — the sort of bug that is invisible until
// someone asks for weekends only.
func TestWindowDaysNameTheStartDay(t *testing.T) {
	mondayOnly := noiseWindow{Start: "20:00", End: "10:00", Days: []int{int(time.Monday)}}
	cases := []struct {
		when time.Time
		want bool
		why  string
	}{
		{at(monday, 21, 0), true, "Monday evening is the window starting"},
		{at(tuesday, 3, 0), true, "Tuesday small hours belong to Monday's window"},
		{at(tuesday, 9, 59), true, "still Monday's window"},
		{at(tuesday, 10, 0), false, "Monday's window has ended"},
		{at(tuesday, 21, 0), false, "Tuesday evening is not a Monday window"},
		{at(monday, 3, 0), false, "Monday small hours would belong to a Sunday window"},
	}
	for _, c := range cases {
		got, err := mondayOnly.contains(c.when)
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("%s: got %v, want %v (%s)", c.when.Format("Mon 15:04"), got, c.want, c.why)
		}
	}
}

func TestWindowEndsAt(t *testing.T) {
	overnight := noiseWindow{Start: "20:00", End: "10:00"}
	// Evening half: the window ends tomorrow morning.
	got, err := overnight.endsAt(at(sunday, 22, 0))
	if err != nil {
		t.Fatal(err)
	}
	if want := at(monday, 10, 0); !got.Equal(want) {
		t.Errorf("evening: got %s, want %s", got, want)
	}
	// Morning half: it ends today.
	got, err = overnight.endsAt(at(monday, 3, 0))
	if err != nil {
		t.Fatal(err)
	}
	if want := at(monday, 10, 0); !got.Equal(want) {
		t.Errorf("morning: got %s, want %s", got, want)
	}
	// A same-day window ends the same day.
	sameDay := noiseWindow{Start: "13:00", End: "15:00"}
	got, err = sameDay.endsAt(at(monday, 14, 0))
	if err != nil {
		t.Fatal(err)
	}
	if want := at(monday, 15, 0); !got.Equal(want) {
		t.Errorf("same day: got %s, want %s", got, want)
	}
}

func nightly() noiseConfig {
	cfg := defaultNoiseConfig()
	cfg.Schedule = []noiseWindow{{Start: "20:00", End: "10:00", Preset: "brown"}}
	return cfg
}

func TestDecideNoise(t *testing.T) {
	cfg := nightly()
	inside, outside := at(sunday, 23, 0), at(monday, 12, 0)

	cases := []struct {
		name string
		now  time.Time
		rt   noiseRuntime
		want noiseAction
	}{
		{"inside a window with a free slot starts", inside,
			noiseRuntime{Kind: sessionNone}, noiseStartAction},

		// The headline case: the unit being replaced says in its own comment that
		// a reboot inside the window leaves the box silent until 20:00.
		{"a restart mid-window recovers", inside,
			noiseRuntime{Kind: sessionNone}, noiseStartAction},

		{"music holds the slot, noise waits", inside,
			noiseRuntime{Kind: sessionQueue}, noiseLeaveAlone},

		{"already making noise is left alone", inside,
			noiseRuntime{Kind: sessionNoise, Scheduled: true}, noiseLeaveAlone},

		{"a human's stop suppresses the rest of the window", inside,
			noiseRuntime{Kind: sessionNone, LatchUntil: at(monday, 10, 0)}, noiseLeaveAlone},

		{"the latch expires with the window", at(monday, 20, 30),
			noiseRuntime{Kind: sessionNone, LatchUntil: at(monday, 10, 0)}, noiseStartAction},

		{"a window ending stops the schedule's own noise", outside,
			noiseRuntime{Kind: sessionNoise, Scheduled: true}, noiseStopAction},

		{"a window ending does NOT stop a human's noise", outside,
			noiseRuntime{Kind: sessionNoise, Scheduled: false}, noiseLeaveAlone},

		{"outside a window with a free slot does nothing", outside,
			noiseRuntime{Kind: sessionNone}, noiseLeaveAlone},

		{"outside a window music is untouched", outside,
			noiseRuntime{Kind: sessionQueue}, noiseLeaveAlone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := decideNoise(cfg, c.now, c.rt)
			if got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

// TestDecideNoisePreemptionIsNotALatch is the interaction the design turns on:
// music preempting noise must not look like a human switching it off, or one
// song at 22:00 would silence the whole night.
func TestDecideNoisePreemptionIsNotALatch(t *testing.T) {
	cfg := nightly()
	now := at(sunday, 22, 0)

	// Music is playing: noise waits, and nothing sets a latch.
	if got, _ := decideNoise(cfg, now, noiseRuntime{Kind: sessionQueue}); got != noiseLeaveAlone {
		t.Fatalf("during music: got %v", got)
	}
	// The queue ends. The slot frees with no latch, so noise resumes.
	if got, _ := decideNoise(cfg, now.Add(20*time.Minute), noiseRuntime{Kind: sessionNone}); got != noiseStartAction {
		t.Fatalf("after the queue ended, noise should resume; got %v", got)
	}
}

// TestDecideNoiseUsesTheWindowsPreset: a window names what to play, which is
// the reason presets are named at all.
func TestDecideNoiseUsesTheWindowsPreset(t *testing.T) {
	cfg := defaultNoiseConfig()
	cfg.Schedule = []noiseWindow{{Start: "20:00", End: "10:00", Preset: "pink"}}
	action, w := decideNoise(cfg, at(sunday, 23, 0), noiseRuntime{Kind: sessionNone})
	if action != noiseStartAction {
		t.Fatalf("got %v", action)
	}
	if w.Preset != "pink" {
		t.Fatalf("window preset: got %q, want pink", w.Preset)
	}
	if p, err := cfg.findPreset(w.Preset); err != nil || p.Type != "pinknoise" {
		t.Fatalf("preset resolution: %+v %v", p, err)
	}
}

// TestActiveWindowSkipsMalformed: one bad "25:00" in a config should cost that
// window, not the night.
func TestActiveWindowSkipsMalformed(t *testing.T) {
	cfg := defaultNoiseConfig()
	cfg.Schedule = []noiseWindow{
		{Start: "25:00", End: "10:00"},
		{Start: "20:00", End: "10:00", Preset: "brown"},
	}
	w, ok := cfg.activeWindow(at(sunday, 23, 0))
	if !ok {
		t.Fatal("a malformed window took the whole schedule down with it")
	}
	if w.Start != "20:00" {
		t.Errorf("got window %+v", w)
	}
}

func TestValidateSchedule(t *testing.T) {
	if err := validateSchedule([]noiseWindow{{Start: "20:00", End: "10:00", Days: []int{0, 6}}}); err != nil {
		t.Errorf("valid schedule rejected: %v", err)
	}
	bad := [][]noiseWindow{
		{{Start: "25:00", End: "10:00"}},
		{{Start: "20:00", End: "banana"}},
		{{Start: "20:00", End: "10:00", Days: []int{7}}},
		{{Start: "20:00", End: "10:00", Days: []int{-1}}},
	}
	for _, ws := range bad {
		if err := validateSchedule(ws); err == nil {
			t.Errorf("%+v should be rejected", ws)
		}
	}
}
