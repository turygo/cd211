package webhook

import "time"

// Clock supplies time and timers so delivery scheduling is deterministic in
// tests. It mirrors the shape used by internal/reconcile; the store methods
// take an explicit time.Time, so callers pass Clock.Now() through unchanged.
type Clock interface {
	Now() time.Time
	NewTimer(time.Duration) Timer
}

// Timer is the subset of time.Timer used by the dispatcher wait loop.
type Timer interface {
	C() <-chan time.Time
	Stop() bool
}

// RealClock uses the process wall clock.
type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }

func (RealClock) NewTimer(d time.Duration) Timer { return realTimer{Timer: time.NewTimer(d)} }

type realTimer struct{ *time.Timer }

func (t realTimer) C() <-chan time.Time { return t.Timer.C }
