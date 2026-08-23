package providers

import (
	"errors"
	"testing"
	"time"
)

func TestBreaker_OpenAfterConsecutiveFailures(t *testing.T) {
	b := NewBreaker(3, time.Minute)
	now := time.Unix(1000, 0)

	// First two failures: still closed.
	for i := 0; i < 2; i++ {
		if !b.Allow(now) {
			t.Fatalf("attempt %d not allowed while closed", i+1)
		}
		b.Failure(now)
		if b.State() != "closed" {
			t.Fatalf("attempt %d opened the circuit too early: %s", i+1, b.State())
		}
	}

	// Third failure opens it.
	if !b.Allow(now) {
		t.Fatal("third attempt must be allowed to reach the rail")
	}
	b.Failure(now)
	if b.State() != "open" {
		t.Fatalf("circuit did not open after threshold: %s", b.State())
	}

	// While open, Allow() refuses.
	if b.Allow(now.Add(10 * time.Second)) {
		t.Fatal("Allow() returned true while circuit is open")
	}
}

func TestBreaker_ProbeAfterCooldown(t *testing.T) {
	b := NewBreaker(1, 30*time.Second)
	now := time.Unix(1000, 0)

	// Trip the circuit.
	b.Allow(now)
	b.Failure(now)

	// Before cooldown elapses nothing passes.
	if b.Allow(now.Add(29 * time.Second)) {
		t.Fatal("probe allowed before cooldown elapsed")
	}

	// After cooldown exactly ONE probe may pass; it must fail the circuit closed
	// again when the rail is still down.
	if !b.Allow(now.Add(31 * time.Second)) {
		t.Fatal("half-open probe was refused")
	}
	if b.State() != "half_open" {
		t.Fatalf("expected half_open after probe, got %s", b.State())
	}
	// A second concurrent caller must be refused during the single probe.
	if b.Allow(now.Add(31 * time.Second)) {
		t.Fatal("second caller probed during a single-probe window")
	}
	b.Failure(now.Add(32 * time.Second))
	if b.State() != "open" {
		t.Fatalf("failed probe did not reopen circuit: %s", b.State())
	}
}

func TestBreaker_ProbeSuccessCloses(t *testing.T) {
	b := NewBreaker(1, time.Minute)
	now := time.Unix(1000, 0)

	b.Allow(now)
	b.Failure(now)
	now = now.Add(2 * time.Minute)

	if !b.Allow(now) {
		t.Fatal("expected probe to be allowed")
	}
	b.Success()
	if b.State() != "closed" {
		t.Fatalf("successful probe did not close circuit: %s", b.State())
	}
	if !b.Allow(now) {
		t.Fatal("traffic refused after recovery")
	}
}

func TestBreaker_Guard(t *testing.T) {
	b := NewBreaker(1, time.Minute)
	now := func() time.Time { return time.Unix(1000, 0) }

	// Open the circuit via Guard.
	err := b.Guard(now, func() error { return errors.New("boom") })
	if err == nil {
		t.Fatal("expected rail failure to propagate")
	}

	// A subsequent Guard refuses with ErrCircuitOpen before touching the rail.
	called := false
	err = b.Guard(now, func() error { called = true; return nil })
	if err == nil {
		t.Fatal("expected circuit-open guard error")
	}
	var pe *Error
	if !errors.As(err, &pe) {
		t.Fatalf("expected *Error, got %T", err)
	}
	if pe.Code != ErrCircuitOpen || !pe.Retryable {
		t.Fatalf("circuit-open error must be retryable ErrCircuitOpen: %+v", pe)
	}
	if called {
		t.Fatal("the rail was called while the circuit was open")
	}
}