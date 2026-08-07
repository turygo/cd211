package reconcile

import (
	"testing"
	"time"
)

func TestRealClockTimer(t *testing.T) {
	clock := RealClock{}
	before := clock.Now()
	timer := clock.NewTimer(time.Millisecond)
	select {
	case fired := <-timer.C():
		if fired.Before(before) {
			t.Fatalf("timer fired before creation: %v < %v", fired, before)
		}
	case <-time.After(time.Second):
		t.Fatal("real timer did not fire")
	}
}

func TestRealClockTimerStop(t *testing.T) {
	timer := RealClock{}.NewTimer(time.Hour)
	if !timer.Stop() {
		t.Fatal("fresh timer was not stopped")
	}
}
