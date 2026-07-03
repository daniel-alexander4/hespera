package ratelimit

import (
	"context"
	"testing"
	"time"
)

func TestWaitEnforcesInterval(t *testing.T) {
	l := New(50 * time.Millisecond)
	ctx := context.Background()

	start := time.Now()
	if err := l.Wait(ctx); err != nil { // first call: immediate
		t.Fatalf("first Wait: %v", err)
	}
	if err := l.Wait(ctx); err != nil { // second call: ~interval later
		t.Fatalf("second Wait: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 45*time.Millisecond {
		t.Fatalf("second call returned after %v, want >= ~50ms", elapsed)
	}
}

func TestWaitCanceledContextReturnsEarly(t *testing.T) {
	l := New(time.Hour)
	ctx := context.Background()
	if err := l.Wait(ctx); err != nil { // consume the immediate slot
		t.Fatalf("first Wait: %v", err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if err := l.Wait(canceled); err == nil {
		t.Fatal("Wait on canceled ctx returned nil, want ctx.Err()")
	}
	if time.Since(start) > time.Second {
		t.Fatal("Wait did not return promptly on canceled ctx")
	}
}
