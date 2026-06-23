package platform

import "time"

// Clock abstracts the wall clock so time-dependent logic is deterministic in
// tests. Production code depends on this interface, never on time.Now directly.
type Clock interface {
	Now() time.Time
}

type systemClock struct{}

// NewClock returns a Clock backed by the system clock, always in UTC.
func NewClock() Clock { return systemClock{} }

func (systemClock) Now() time.Time { return time.Now().UTC() }

// FixedClock is a deterministic Clock for tests.
type FixedClock struct{ T time.Time }

func (f FixedClock) Now() time.Time { return f.T }
