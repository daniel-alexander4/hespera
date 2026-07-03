// Package ratelimit provides the minimal min-interval rate limiter shared by
// the external API clients — one Limiter instance per host budget (e.g. the
// MetaBrainz family shares one; fanart.tv, TheAudioDB, Last.fm, and
// OpenSubtitles each own one).
package ratelimit

import (
	"context"
	"sync"
	"time"
)

// Limiter enforces a minimum interval between successive calls across all
// users of one instance.
type Limiter struct {
	mu       sync.Mutex
	next     time.Time
	interval time.Duration
}

// New returns a Limiter enforcing the given minimum interval between calls.
func New(interval time.Duration) *Limiter {
	return &Limiter{interval: interval}
}

// Wait blocks until this caller's reserved slot arrives or ctx is done. The
// slot is reserved under the mutex but the sleep happens outside it, so a
// concurrent caller is never serialized behind another's sleep — each just
// gets the next slot. A canceled ctx returns immediately with ctx.Err();
// callers that treat throttling as a side effect may ignore the error (their
// next request on the same ctx fails anyway).
func (l *Limiter) Wait(ctx context.Context) error {
	l.mu.Lock()
	now := time.Now()
	slot := l.next
	if slot.Before(now) {
		slot = now
	}
	l.next = slot.Add(l.interval)
	l.mu.Unlock()

	delay := time.Until(slot)
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
