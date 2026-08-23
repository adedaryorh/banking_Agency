package clock

import "time"

// Clock provides testable time operations
type Clock interface {
	Now() time.Time
}

// RealClock returns actual system time
type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }

// FrozenClock returns a fixed time (for testing)
type FrozenClock struct {
	Frozen time.Time
}

func (c FrozenClock) Now() time.Time { return c.Frozen }

// SystemClock is an alias for RealClock for convenience
type SystemClock = RealClock
