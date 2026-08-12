package domain

import "testing"

var allStates = []State{
	StateAccepted,
	StateStopped,
	StateSubmittingOffline,
	StateWaitingOffline,
	StateSubmittingCopy,
	StateWaitingCopy,
	StateVerifyingLocal,
	StateCompleted,
	StateFailed,
	StateCancelRequested,
	StateCancelled,
	StateDeleteRequested,
	StateDeleted,
}

func TestStatePredicates(t *testing.T) {
	terminal := map[State]bool{
		StateCompleted: true,
		StateFailed:    true,
		StateCancelled: true,
	}
	unclaimable := map[State]bool{
		StateStopped:   true,
		StateCompleted: true,
		StateFailed:    true,
		StateCancelled: true,
		StateDeleted:   true,
	}
	for _, state := range allStates {
		if !state.Valid() {
			t.Errorf("%s should be valid", state)
		}
		if got, want := state.Visible(), state != StateDeleteRequested && state != StateDeleted; got != want {
			t.Errorf("%s Visible() = %t, want %t", state, got, want)
		}
		if got := state.Terminal(); got != terminal[state] {
			t.Errorf("%s Terminal() = %t, want %t", state, got, terminal[state])
		}
		if got := state.Claimable(); got != !unclaimable[state] {
			t.Errorf("%s Claimable() = %t, want %t", state, got, !unclaimable[state])
		}
	}

	invalid := State("UNKNOWN")
	if invalid.Valid() || invalid.Visible() || invalid.Terminal() || invalid.Claimable() {
		t.Error("invalid state must satisfy no predicates")
	}
	if !SourceMagnet.Valid() || !SourceTorrent.Valid() || SourceKind("file").Valid() {
		t.Error("source validity does not match the closed source set")
	}
}

func TestCanTransitionExhaustive(t *testing.T) {
	legal := map[State]map[State]bool{
		StateAccepted: {
			StateSubmittingOffline: true,
			StateFailed:            true,
			StateCancelRequested:   true,
			StateDeleteRequested:   true,
		},
		StateStopped: {
			StateAccepted:        true,
			StateSubmittingCopy:  true,
			StateVerifyingLocal:  true,
			StateCancelRequested: true,
			StateDeleteRequested: true,
		},
		StateSubmittingOffline: {
			StateWaitingOffline:  true,
			StateFailed:          true,
			StateCancelRequested: true,
			StateDeleteRequested: true,
		},
		StateWaitingOffline: {
			StateSubmittingCopy:  true,
			StateFailed:          true,
			StateCancelRequested: true,
			StateDeleteRequested: true,
		},
		StateSubmittingCopy: {
			StateWaitingCopy:     true,
			StateFailed:          true,
			StateCancelRequested: true,
			StateDeleteRequested: true,
		},
		StateWaitingCopy: {
			StateVerifyingLocal:  true,
			StateFailed:          true,
			StateCancelRequested: true,
			StateDeleteRequested: true,
		},
		StateVerifyingLocal: {
			StateCompleted:       true,
			StateFailed:          true,
			StateCancelRequested: true,
			StateDeleteRequested: true,
		},
		StateCompleted: {
			StateDeleteRequested: true,
		},
		StateFailed: {
			StateDeleteRequested:   true,
			StateAccepted:          true,
			StateSubmittingOffline: true,
			StateWaitingOffline:    true,
			StateSubmittingCopy:    true,
			StateWaitingCopy:       true,
			StateVerifyingLocal:    true,
		},
		StateCancelRequested: {
			StateStopped:         true,
			StateCancelled:       true,
			StateDeleteRequested: true,
		},
		StateCancelled: {
			StateDeleteRequested: true,
		},
		StateDeleteRequested: {
			StateDeleted: true,
		},
		StateDeleted: {
			StateAccepted:       true,
			StateStopped:        true,
			StateVerifyingLocal: true,
		},
	}

	for _, from := range allStates {
		for _, to := range allStates {
			if got, want := CanTransition(from, to), legal[from][to]; got != want {
				t.Errorf("CanTransition(%s, %s) = %t, want %t", from, to, got, want)
			}
		}
	}
	if CanTransition(State("UNKNOWN"), StateAccepted) || CanTransition(StateAccepted, State("UNKNOWN")) {
		t.Error("unknown states must not be transitionable")
	}
}
