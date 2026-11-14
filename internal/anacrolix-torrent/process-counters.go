package torrent

import "sync/atomic"

// Process-wide event counters for /metrics/prometheus. The per-Torrent counters beside them
// vanish with the Torrent, so a sum over active torrents falls when one expires; these only
// ever rise. Lock-free: a scrape reads them without taking the client lock.
var processCounters struct {
	hedgeTriggers     atomic.Int64
	peerEjectsTCP     atomic.Int64
	peerEjectsUTP     atomic.Int64
	peerChurnsTCP     atomic.Int64
	peerChurnsUTP     atomic.Int64
	gradient2Backoffs atomic.Int64
}

// ProcessCounts is a snapshot of the process-wide event counters.
type ProcessCounts struct {
	HedgeTriggers     int64
	PeerEjectsTCP     int64
	PeerEjectsUTP     int64
	PeerChurnsTCP     int64
	PeerChurnsUTP     int64
	Gradient2Backoffs int64
}

// ProcessCounters returns the process-wide counts since startup.
func ProcessCounters() ProcessCounts {
	return ProcessCounts{
		HedgeTriggers:     processCounters.hedgeTriggers.Load(),
		PeerEjectsTCP:     processCounters.peerEjectsTCP.Load(),
		PeerEjectsUTP:     processCounters.peerEjectsUTP.Load(),
		PeerChurnsTCP:     processCounters.peerChurnsTCP.Load(),
		PeerChurnsUTP:     processCounters.peerChurnsUTP.Load(),
		Gradient2Backoffs: processCounters.gradient2Backoffs.Load(),
	}
}
