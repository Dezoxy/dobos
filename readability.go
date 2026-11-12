package main

import (
	"log"
	"time"

	"flodni/internal/gostorm/native"
	"flodni/internal/metadb"
)

// Values of last_failure_reason and last_failure_source in the readability record.
// New values may be added; the README tells readers to ignore the ones they do not
// know.
const (
	failReasonNoPeers   = "no_peers"   // the engine had the torrent, with no connection
	failReasonTimeout   = "timeout"    // peers were connected and the read still gave up
	failReasonNotActive = "not_active" // the engine did not have the torrent running
	failReasonUnknown   = "unknown"    // the classification itself timed out

	// failSourceMount: a reader through the FUSE mount. The verdict comes from any
	// mount session of sessionVerdictFloor or more, scanners included.
	failSourceMount = "mount"

	// classifyTimeout bounds the engine calls made on a failing FUSE read's way out:
	// Stats() takes an engine lock, and that read is already late.
	classifyTimeout = time.Second
)

// classifyFailure says why a read gave up on hash. It asks the engine under a short
// bound and answers unknown when the engine does not reply in time.
func classifyFailure(hash string) string {
	if hash == "" {
		return failReasonUnknown
	}
	res := make(chan string, 1)
	live, peers := torrentLive, sessionActivePeers
	go func() {
		switch {
		case !live(hash):
			res <- failReasonNotActive
		case peers(hash) == 0:
			res <- failReasonNoPeers
		default:
			res <- failReasonTimeout
		}
	}()
	select {
	case r := <-res:
		return r
	case <-time.After(classifyTimeout):
		return failReasonUnknown
	}
}

// readabilityStore is the part of the state database the recorder writes to.
type readabilityStore interface {
	RecordMetadataFailure(hash, reason, source string, at time.Time) error
	RecordSwarmRead(hash string, at time.Time) error
	ResetMetadataFailures(hash string) error
}

// newReachabilityRecorder returns the native.ReachabilityOutcome hook that writes the
// readability record. Only bytes from the swarm stamp a read; a metainfo taken from it
// only ends the streak.
func newReachabilityRecorder(store readabilityStore, logger *log.Logger) func(native.Outcome) {
	return func(o native.Outcome) {
		var err error
		switch o.Kind {
		case native.OutcomeSwarmRead:
			err = store.RecordSwarmRead(o.Hash, time.Now())
		case native.OutcomeMetainfo:
			err = store.ResetMetadataFailures(o.Hash)
		case native.OutcomeFailed:
			short := o.Hash
			if len(short) > 8 {
				short = short[:8]
			}
			// Deliberately does not say why: the session layer that condemned
			// already logged its own reason, with duration and outcome.
			logger.Printf("[DeadSwarm] %s did not answer", short)
			err = store.RecordMetadataFailure(o.Hash, o.Reason, o.Source, time.Now())
		}
		if err != nil {
			logger.Printf("WARNING: reachability bookkeeping for %s: %v", o.Hash, err)
		}
	}
}

// Compile-time pins: the library manager asserts this signature on the registry at
// run time, so a rename would stop the deletes without a compile error.
var (
	_ readabilityStore                                = (*metadb.DB)(nil)
	_ interface{ ClearMetadataFailure(string) error } = (*metadb.DB)(nil)
)
