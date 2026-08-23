package providers

import (
	"sync"
	"time"
)

type circuitState int32

const (
	circuitClosed circuitState = iota
	circuitOpen
	circuitHalfOpen
)

type Breaker struct {
	mu sync.Mutex

	state circuitState

	failures int
	// failureThreshold consecutive failures that open the circuit.
	failureThreshold int
	// cooldown the circuit stays open before allowing a probe.
	cooldown  time.Duration
	openUntil time.Time

	// halfProbes allowed in one half-open window.
	halfProbes int
}

func NewBreaker(failureThreshold int, cooldown time.Duration) *Breaker {
	if failureThreshold < 1 {
		failureThreshold = 5
	}
	if cooldown <= 0 {
		cooldown = 30 * time.Second
	}
	return &Breaker{failureThreshold: failureThreshold, cooldown: cooldown}
}

// State returns the current state for metrics/observability.
func (b *Breaker) State() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case circuitOpen:
		return "open"
	case circuitHalfOpen:
		return "half_open"
	default:
		return "closed"
	}
}

func (b *Breaker) Allow(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case circuitOpen:
		if now.Before(b.openUntil) {
			return false
		}
		b.state = circuitHalfOpen

		b.halfProbes = 1
		return true
	case circuitHalfOpen:

		if b.halfProbes >= 1 {
			return false
		}
		b.halfProbes++
		return true
	default:
		return true
	}
}

func (b *Breaker) Success() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures = 0
	if b.state == circuitHalfOpen {
		b.state = circuitClosed
	}
}

// Failure records a rail failure. If it does not already know of the failure,
// it marks the circuit tripped on this attempt.
func (b *Breaker) Failure(now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures++
	if b.state == circuitHalfOpen || b.failures >= b.failureThreshold {
		b.state = circuitOpen
		b.openUntil = now.Add(b.cooldown)
		if b.halfProbes > 0 {
			b.failures = 0 // probe failed; count restarts at the next window
		}
	}
}

func (b *Breaker) Guard(now func() time.Time, fn func() error) error {
	if now == nil {
		now = time.Now
	}
	if !b.Allow(now()) {
		return &Error{Code: ErrCircuitOpen, Message: "circuit open: payout rail temporarily blocked, cooling down", Retryable: true}
	}
	if err := fn(); err != nil {
		b.Failure(now())
		return err
	}
	b.Success()
	return nil
}
