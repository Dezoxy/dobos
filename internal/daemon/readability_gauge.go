package daemon

import (
	"log"
	"sync/atomic"
	"time"
)

// readabilityGaugeInterval is how often the torrent counts are recomputed. A scrape
// reads the stored numbers only, so it never touches the state database.
const readabilityGaugeInterval = 60 * time.Second

// readabilityCounts is the library split by readability, in torrents.
type readabilityCounts struct {
	ok, failed, neverRead int64
}

// readabilityGauge holds the latest counts, nil until the first refresh succeeds and
// forever when the state database is off, which omits the series.
var readabilityGauge atomic.Pointer[readabilityCounts]

// countReadability buckets the library by its readability record:
//   - failed: a row with a failure streak (fail_count >= 1);
//   - ok: no streak and a swarm read stamped (last_swarm_read > 0);
//   - never_read: the other library torrents, whether they have no row or a row a
//     metainfo reset without stamping a read.
//
// Rows are not filtered by library membership, so a row left behind by a torrent that
// left the library can push ok + failed past the library size: never_read stops at 0.
func countReadability(store readabilityLister, libraryHashes int) (readabilityCounts, error) {
	rows, err := store.ListReadability(0)
	if err != nil {
		return readabilityCounts{}, err
	}
	var c readabilityCounts
	for _, r := range rows {
		switch {
		case r.FailCount >= 1:
			c.failed++
		case r.LastSwarmRead > 0:
			c.ok++
		}
	}
	c.neverRead = max(int64(libraryHashes)-c.ok-c.failed, 0)
	return c, nil
}

// refreshReadabilityGauge stores a fresh count. On an error it keeps the last numbers.
func refreshReadabilityGauge(store readabilityLister, libraryHashes int, logger *log.Logger) {
	c, err := countReadability(store, libraryHashes)
	if err != nil {
		logger.Printf("WARNING: readability gauge: %v", err)
		return
	}
	readabilityGauge.Store(&c)
}

// startReadabilityGauge refreshes the gauge now and then every interval until stop
// closes. library reports the number of distinct torrents the mount lists.
func startReadabilityGauge(store readabilityLister, library func() int, stop <-chan struct{}, logger *log.Logger) {
	go func() {
		refreshReadabilityGauge(store, library(), logger)
		t := time.NewTicker(readabilityGaugeInterval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				refreshReadabilityGauge(store, library(), logger)
			case <-stop:
				return
			}
		}
	}()
}
