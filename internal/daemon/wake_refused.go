package daemon

import (
	"path/filepath"
	"sync/atomic"
	"syscall"

	"flodni/internal/gostorm/native"
)

// errnoWakeRefused is what a handle's read returns when the engine refused to wake
// its torrent (native.ErrNotStored). It is private, like errnoBudgetRefused:
// MkvHandle.Read maps it to EIO before ttffReadFailed and the short-read repair,
// because a torrent the engine does not store is not a swarm that failed to
// deliver, and must never become a dead-swarm verdict.
const errnoWakeRefused = syscall.ENOKEY

// wakeRefusedCount counts refused wakes since start, for flodni_wake_refused_total:
// one per handle at a read (the handle latches the refusal), one per Open that
// found the torrent not stored. 0 from the start.
var wakeRefusedCount atomic.Int64

// noteWakeRefused counts a refused wake, writes the distinct log line and reports it
// to the readability record under its own reason (metadb.NotStoredReason, "not_stored"). It is not a
// failed swarm read: nothing is added to consecutive_failures or to the session's
// failed reads, and no dead-swarm verdict follows.
func noteWakeRefused(path, hash, where string) {
	wakeRefusedCount.Add(1)
	short := hash
	if len(short) > 8 {
		short = short[:8]
	}
	logger.Printf("[VFS] wake refused at %s for %s (%s): the engine does not store this torrent; "+
		"re-add it (not a dead swarm)", where, filepath.Base(path), short)
	if native.ReachabilityOutcome != nil && hash != "" {
		native.ReachabilityOutcome(native.Outcome{
			Hash: hash, Kind: native.OutcomeNotStored, Source: failSourceMount,
		})
	}
}
