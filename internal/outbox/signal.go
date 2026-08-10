package outbox

import "sync"

// Signal is a process-owned broadcast latch for domain event notifications.
// The store owns exactly one Signal for the process lifetime, so it survives
// settings runtime generation swaps. Notify must be called only after a
// transaction that inserted a domain_events row commits successfully.
//
// A waiter snapshots the current channel before its authoritative database
// query and waits on that channel afterwards; Notify closes the channel and
// installs a fresh one, so every snapshot holder wakes exactly once per
// notification. A missed notification only adds bounded delay, because the
// database remains authoritative and waiters re-snapshot on the next round.
type Signal struct {
	mu sync.Mutex
	ch chan struct{}
}

// NewSignal returns a Signal with an open snapshot channel.
func NewSignal() *Signal {
	return &Signal{ch: make(chan struct{})}
}

// Snapshot returns the current broadcast channel. The returned channel is
// closed by the next Notify call; it is never closed by the Signal itself.
func (s *Signal) Snapshot() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ch == nil {
		s.ch = make(chan struct{})
	}
	return s.ch
}

// Notify wakes every holder of the current snapshot channel and installs a
// fresh open channel for the next round. It is safe for concurrent use and
// may be called repeatedly; each call wakes the snapshot holders of the
// channel that was current when they snapshotted.
func (s *Signal) Notify() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ch == nil {
		s.ch = make(chan struct{})
	}
	close(s.ch)
	s.ch = make(chan struct{})
}
