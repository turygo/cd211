package domain

// State is the persisted lifecycle state of a download.
type State string

const (
	StateAccepted          State = "ACCEPTED"
	StateStopped           State = "STOPPED"
	StateSubmittingOffline State = "SUBMITTING_OFFLINE"
	StateWaitingOffline    State = "WAITING_OFFLINE"
	StateSubmittingCopy    State = "SUBMITTING_COPY"
	StateWaitingCopy       State = "WAITING_COPY"
	StateVerifyingLocal    State = "VERIFYING_LOCAL"
	StateCompleted         State = "COMPLETED"
	StateFailed            State = "FAILED"
	StateCancelRequested   State = "CANCEL_REQUESTED"
	StateCancelled         State = "CANCELLED"
	StateDeleteRequested   State = "DELETE_REQUESTED"
	StateDeleted           State = "DELETED"
)

// SourceKind identifies the form used to submit a torrent.
type SourceKind string

const (
	SourceMagnet  SourceKind = "magnet"
	SourceTorrent SourceKind = "torrent"
)

// Valid reports whether s is a defined persisted state.
func (s State) Valid() bool {
	switch s {
	case StateAccepted, StateStopped, StateSubmittingOffline, StateWaitingOffline,
		StateSubmittingCopy, StateWaitingCopy, StateVerifyingLocal, StateCompleted,
		StateFailed, StateCancelRequested, StateCancelled, StateDeleteRequested, StateDeleted:
		return true
	default:
		return false
	}
}

// Visible reports whether s should be exposed as a qBittorrent torrent.
func (s State) Visible() bool {
	return s.Valid() && s != StateDeleteRequested && s != StateDeleted
}

// Terminal reports whether s is a terminal visible state.
func (s State) Terminal() bool {
	switch s {
	case StateCompleted, StateFailed, StateCancelled:
		return true
	default:
		return false
	}
}

// Claimable reports whether work in s may be claimed by a worker.
func (s State) Claimable() bool {
	if !s.Valid() {
		return false
	}

	switch s {
	case StateStopped, StateCompleted, StateFailed, StateCancelled, StateDeleted:
		return false
	default:
		return true
	}
}

// Valid reports whether s is a supported submission source.
func (s SourceKind) Valid() bool {
	return s == SourceMagnet || s == SourceTorrent
}

// CanTransition reports whether a download may move directly from from to to.
func CanTransition(from, to State) bool {
	if from == to {
		return false
	}

	switch from {
	case StateAccepted:
		return to == StateSubmittingOffline || activeExit(to)
	case StateStopped:
		return to == StateAccepted || to == StateSubmittingCopy || to == StateVerifyingLocal ||
			to == StateCancelRequested || to == StateDeleteRequested
	case StateSubmittingOffline:
		return to == StateWaitingOffline || activeExit(to)
	case StateWaitingOffline:
		return to == StateSubmittingCopy || activeExit(to)
	case StateSubmittingCopy:
		return to == StateWaitingCopy || activeExit(to)
	case StateWaitingCopy:
		return to == StateVerifyingLocal || activeExit(to)
	case StateVerifyingLocal:
		return to == StateCompleted || activeExit(to)
	case StateCompleted, StateCancelled:
		return to == StateDeleteRequested
	case StateFailed:
		switch to {
		case StateDeleteRequested, StateAccepted, StateSubmittingOffline, StateWaitingOffline,
			StateSubmittingCopy, StateWaitingCopy, StateVerifyingLocal:
			return true
		default:
			return false
		}
	case StateCancelRequested:
		return to == StateStopped || to == StateCancelled || to == StateDeleteRequested
	case StateDeleteRequested:
		return to == StateDeleted
	case StateDeleted:
		return to == StateAccepted || to == StateStopped || to == StateVerifyingLocal
	default:
		return false
	}
}

func activeExit(to State) bool {
	return to == StateFailed || to == StateCancelRequested || to == StateDeleteRequested
}
