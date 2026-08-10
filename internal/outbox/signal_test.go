package outbox

import (
	"sync"
	"testing"
)

// signalClosed reports whether a snapshotted event channel has been closed.
func signalClosed(channel <-chan struct{}) bool {
	select {
	case _, ok := <-channel:
		return !ok
	default:
		return false
	}
}

func TestSignalSnapshotNotifyWake(t *testing.T) {
	signal := NewSignal()
	snapshot := signal.Snapshot()
	if signalClosed(snapshot) {
		t.Fatal("fresh signal snapshot is closed")
	}

	signal.Notify()
	if !signalClosed(snapshot) {
		t.Error("snapshot not closed after Notify")
	}

	// A fresh snapshot is open and the next Notify wakes it too.
	second := signal.Snapshot()
	if signalClosed(second) {
		t.Fatal("snapshot after Notify is closed")
	}
	signal.Notify()
	if !signalClosed(second) {
		t.Error("second snapshot not closed after the second Notify")
	}

	// Snapshots taken after the last Notify stay open until the next one.
	third := signal.Snapshot()
	if signalClosed(third) {
		t.Fatal("snapshot with no pending notify is closed")
	}
}

func TestSignalConcurrentSnapshotAndNotify(t *testing.T) {
	signal := NewSignal()
	const (
		workers = 8
		rounds  = 200
	)
	var wait sync.WaitGroup
	wait.Add(workers)
	for worker := 0; worker < workers; worker++ {
		go func() {
			defer wait.Done()
			for round := 0; round < rounds; round++ {
				snapshot := signal.Snapshot()
				signal.Notify()
				// Every channel ever handed out is closed by the first Notify
				// after its creation, and this goroutine's own Notify follows
				// its Snapshot, so the snapshot must be closed here.
				if !signalClosed(snapshot) {
					t.Error("snapshot not closed after concurrent Notify")
					return
				}
			}
		}()
	}
	wait.Wait()
}

func TestSignalZeroValueIsUsable(t *testing.T) {
	var signal Signal
	// A snapshot before any notify returns an open channel, never a nil one.
	snapshot := signal.Snapshot()
	if signalClosed(snapshot) {
		t.Fatal("zero-value signal snapshot is closed")
	}
	signal.Notify()
	if !signalClosed(snapshot) {
		t.Error("zero-value signal snapshot not closed after Notify")
	}
}
