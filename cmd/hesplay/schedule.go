package main

// schedule.go — the noise reconciler.
//
// The systemd units this replaces were EDGE-triggered: a timer fired at 20:00
// to start and another at 10:00 to stop. brownnoise.service says so in its own
// comment — "A reboot inside the window leaves it silent until 20:00 — start it
// by hand if that ever matters." That is the failure this file exists to
// remove: a box rebooted at 23:00, or a hesplay restarted by a deploy, misses
// the edge and stays silent all night with nothing wrong in any log.
//
// So nothing here reacts to a boundary. Every tick asks the same question —
// "should noise be playing right now, and is it?" — and closes the gap. Reboot
// inside a window and the next tick starts it.

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// noiseTick is how often the reconciler re-asks its question. A window boundary
// is therefore accurate to within one tick, which is the right trade: the
// alternative is arming timers, and an armed timer is exactly the thing that
// gets missed across a restart.
const noiseTick = 30 * time.Second

// sessionKind is what currently occupies the audio slot. There is only one
// slot: the box has one sound card, and on a board with no audio server the
// second opener simply fails, so noise and music take turns rather than mixing.
type sessionKind int

const (
	sessionNone sessionKind = iota
	sessionQueue
	sessionNoise
)

// noiseRuntime is the reconciler's view of the world — everything decideNoise
// needs, and nothing that would drag a controller into a unit test.
type noiseRuntime struct {
	Kind      sessionKind
	Scheduled bool // the running noise session was started by the scheduler
	// LatchUntil suppresses auto-start until this instant. Set when a human
	// stops noise DURING a window, so the reconciler doesn't immediately undo
	// them. Deliberately not consulted for anything else — see decideNoise.
	LatchUntil time.Time
}

// noiseAction is what the reconciler wants done.
type noiseAction int

const (
	noiseLeaveAlone noiseAction = iota
	noiseStartAction
	noiseStopAction
)

// decideNoise is the whole scheduling policy, as a pure function of the config,
// the clock and the current runtime. Kept pure because every interesting case
// here — midnight wrap, a reboot mid-window, a manual stop, music preempting —
// is a question about state transitions, not about processes.
func decideNoise(cfg noiseConfig, now time.Time, rt noiseRuntime) (noiseAction, noiseWindow) {
	w, active := cfg.activeWindow(now)

	switch {
	// Music holds the slot. Noise never interrupts a queue; the reconciler picks
	// it back up when the queue ends and the slot frees. This is NOT a latch —
	// preemption and a human's stop are different reasons for silence, and
	// conflating them would mean one song at 22:00 killed the noise all night.
	case rt.Kind == sessionQueue:
		return noiseLeaveAlone, noiseWindow{}

	// Already making noise inside a window: correct, whoever started it. A
	// manual session running a different preset than the window names is left
	// alone on purpose — the human chose it more recently than the schedule did.
	case active && rt.Kind == sessionNoise:
		return noiseLeaveAlone, w

	// Inside a window with a free slot: start, unless a human stopped it and
	// this window occurrence is still latched off.
	case active && rt.Kind == sessionNone:
		if now.Before(rt.LatchUntil) {
			return noiseLeaveAlone, w
		}
		return noiseStartAction, w

	// Outside every window, and the noise running is the schedule's own. A
	// MANUAL session is left alone — someone asked for it, and no window ending
	// revokes that.
	case !active && rt.Kind == sessionNoise && rt.Scheduled:
		return noiseStopAction, noiseWindow{}
	}
	return noiseLeaveAlone, noiseWindow{}
}

// activeWindow returns the first configured window containing now. A malformed
// window is skipped rather than failing the whole schedule: one bad "25:00"
// typed into a config should cost that window, not the night.
func (cfg noiseConfig) activeWindow(now time.Time) (noiseWindow, bool) {
	for _, w := range cfg.Schedule {
		if in, err := w.contains(now); err == nil && in {
			return w, true
		}
	}
	return noiseWindow{}, false
}

// contains reports whether t falls inside this window.
//
// A window whose end is not strictly after its start WRAPS MIDNIGHT, which is
// the normal case here — 20:00→10:00 is 14 hours across a day boundary. Days
// name the day the window STARTS on, so a Monday 20:00→10:00 window runs into
// Tuesday morning; matching the morning half against Tuesday would silently
// shift every overnight window by a day.
//
// start == end means a full 24 hours, which falls out of the wrap arithmetic
// rather than being special-cased.
func (w noiseWindow) contains(t time.Time) (bool, error) {
	start, err := parseHHMM(w.Start)
	if err != nil {
		return false, fmt.Errorf("window start: %w", err)
	}
	end, err := parseHHMM(w.End)
	if err != nil {
		return false, fmt.Errorf("window end: %w", err)
	}
	mins := t.Hour()*60 + t.Minute()

	if start < end {
		return w.dayAllowed(t.Weekday()) && mins >= start && mins < end, nil
	}
	// Wraps midnight: the evening half belongs to today, the morning half to a
	// window that started yesterday.
	if mins >= start && w.dayAllowed(t.Weekday()) {
		return true, nil
	}
	if mins < end && w.dayAllowed(prevWeekday(t.Weekday())) {
		return true, nil
	}
	return false, nil
}

// endsAt is the instant this window's CURRENT occurrence ends. Used for the
// manual-stop latch, which must expire exactly when the window does so the next
// night starts clean.
func (w noiseWindow) endsAt(t time.Time) (time.Time, error) {
	start, err := parseHHMM(w.Start)
	if err != nil {
		return time.Time{}, err
	}
	end, err := parseHHMM(w.End)
	if err != nil {
		return time.Time{}, err
	}
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	mins := t.Hour()*60 + t.Minute()
	if start >= end && mins >= start {
		// We are in the evening half; the window ends tomorrow morning.
		day = day.AddDate(0, 0, 1)
	}
	return day.Add(time.Duration(end) * time.Minute), nil
}

// dayAllowed reports whether this window runs on the given start day. An empty
// Days list means every day.
func (w noiseWindow) dayAllowed(d time.Weekday) bool {
	if len(w.Days) == 0 {
		return true
	}
	for _, x := range w.Days {
		if time.Weekday(x) == d {
			return true
		}
	}
	return false
}

func prevWeekday(d time.Weekday) time.Weekday {
	return time.Weekday((int(d) + 6) % 7)
}

// parseHHMM reads a local "HH:MM" into minutes since midnight. Strict on
// purpose: a schedule is unattended, so a value that merely looks plausible
// ("8:00pm", "20.00") should be rejected where it is written rather than
// silently interpreted at 3am.
func parseHHMM(s string) (int, error) {
	s = strings.TrimSpace(s)
	h, m, ok := strings.Cut(s, ":")
	if !ok {
		return 0, fmt.Errorf("%q is not HH:MM", s)
	}
	hh, err := strconv.Atoi(h)
	if err != nil || len(h) == 0 || len(h) > 2 {
		return 0, fmt.Errorf("%q is not HH:MM", s)
	}
	mm, err := strconv.Atoi(m)
	if err != nil || len(m) != 2 {
		return 0, fmt.Errorf("%q is not HH:MM", s)
	}
	if hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, fmt.Errorf("%q is out of range", s)
	}
	return hh*60 + mm, nil
}

// validateSchedule reports every unusable window, so a config save can refuse
// with a useful message instead of a window quietly never firing.
func validateSchedule(ws []noiseWindow) error {
	for i, w := range ws {
		if _, err := parseHHMM(w.Start); err != nil {
			return fmt.Errorf("window %d start: %w", i+1, err)
		}
		if _, err := parseHHMM(w.End); err != nil {
			return fmt.Errorf("window %d end: %w", i+1, err)
		}
		for _, d := range w.Days {
			if d < 0 || d > 6 {
				return fmt.Errorf("window %d: %d is not a weekday (0=Sunday..6=Saturday)", i+1, d)
			}
		}
	}
	return nil
}
