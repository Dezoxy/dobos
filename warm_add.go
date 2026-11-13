package main

import (
	"context"
	"errors"
	"sync"
	"time"

	"flodni/internal/library"
	"flodni/internal/promtext"
	"flodni/internal/warmer"
	"flodni/internal/warmup"
)

// Warm on add (Dezoxy fork, Readiness step 2): the wiring of internal/warmer to the
// library, the engine, the cache and the per-file record. The warmer is built at start
// and idle until the library files a title with warm_on_add on.

// addWarmer is the process-wide warmer; nil-safe, so a test or a build that never
// created it reports zeroed series and ignores removals.
var addWarmer *warmer.Warmer

// warmWake is NativeClient.WakeStored, set at start beside the engine bridge.
var warmWake func(ctx context.Context, hash string) error

// warnWarmOnAddNoDir makes the "no warmup_dir" warning appear once.
var warnWarmOnAddNoDir sync.Once

// warmOnAddActive is the switch the warmer reads at each enqueue: warm_on_add, and the
// index cache (warmup_dir) the windows are written to.
func warmOnAddActive() bool {
	c := gc()
	return c != nil && c.WarmOnAdd && c.WarmupDir != ""
}

// onLibraryAdded is library.Config.OnAdded: it only enqueues.
func onLibraryAdded(hash string, files []library.AddedFile) {
	c := gc()
	if c == nil || !c.WarmOnAdd {
		return
	}
	if c.WarmupDir == "" {
		warnWarmOnAddNoDir.Do(func() {
			logger.Printf("WARNING: warm_on_add is set but warmup_dir is not: the windows have no index cache to live in, so nothing is warmed on add")
		})
		return
	}
	fs := make([]warmer.File, 0, len(files))
	for _, f := range files {
		fs = append(fs, warmer.File{Index: f.FileIndex, Size: f.Size})
	}
	addWarmer.Add(hash, fs)
}

// onHashRemoved is warmup.OnHashRemoved: the warmer stops first, so nothing it still
// holds lands behind the delete of the torrent's records.
func onHashRemoved(w *warmer.Warmer, x *fileStateIndex) func(string) {
	return func(hash string) {
		w.Cancel(hash)
		x.ForgetHash(hash)
	}
}

// addWarmerDeps wires the warmer to the package globals and the file index x.
func addWarmerDeps(x *fileStateIndex, stop <-chan struct{}) warmer.Deps {
	return warmer.Deps{
		Enabled:      warmOnAddActive,
		PlaybackOpen: playbackOpen,
		Live:         func(hash string) bool { return torrentLive(hash) },
		WakeStored: func(ctx context.Context, hash string) error {
			if warmWake == nil {
				return errors.New("no engine client")
			}
			return warmWake(ctx, hash)
		},
		Policy: x.WarmPolicy,
		Fill: func(req warmup.FillRequest) (warmup.FillResult, error) {
			return warmup.DiskWarmup.FillWindows(req) // nil-safe: ErrNotWired
		},
		DeliveredAt:  warmDeliveredAt,
		EvictedSince: x.EvictedSince,
		Record: func(u warmer.Update) {
			x.RecordWarm(warmUpdate{key: u.Key, readability: u.Readability, cache: u.Cache,
				deliveredAt: u.DeliveredAt, attemptAt: u.AttemptAt})
		},
		SwarmRead: func(hash string, at time.Time) {
			db := stateDB
			if db == nil {
				return
			}
			if err := db.RecordSwarmRead(hash, at); err != nil {
				logger.Printf("WARNING: warm on add: cannot stamp the swarm read of %s: %v", hash, err)
			}
		},
		Classify: classifyFailure,
		RemoveCache: func(hash string) {
			if d := warmup.DiskWarmup; d != nil {
				d.RemoveHash(hash) // runs OnHashRemoved: cancel and forget
				return
			}
			x.ForgetHash(hash)
		},
		Log:  func(line string) { logger.Print(line) },
		Stop: stop,
	}
}

// addWarmFillSamples is flodni_warm_fills_total{trigger="add"}: every outcome, 0 until
// it happens. Emitted in the same family as the mount series.
func addWarmFillSamples(st warmer.Stats) []promtext.Sample {
	out := make([]promtext.Sample, 0, len(warmer.Outcomes))
	for _, o := range warmer.Outcomes {
		out = append(out, promSample(st.Fills[o], lbl("trigger", warmer.Trigger), lbl("outcome", o)))
	}
	return out
}

// collectAddWarmProm writes the warmer's other families.
func collectAddWarmProm(w *promtext.Writer, st warmer.Stats) {
	w.Counter("flodni_warm_fill_bytes_total",
		"Bytes warm on add wrote to the head and tail windows, also by fills that did not finish.",
		promSample(st.HeadBytes, lbl("trigger", warmer.Trigger), lbl("window", "head")),
		promSample(st.TailBytes, lbl("trigger", warmer.Trigger), lbl("window", "tail")))
	cf := make([]promtext.Sample, 0, len(warmer.CacheFailureReasons))
	for _, r := range warmer.CacheFailureReasons {
		cf = append(cf, promSample(st.CacheFailures[r], lbl("reason", r)))
	}
	w.Counter("flodni_warm_cache_failures_total",
		"Warm on add attempts that lost a window or could not write it, by reason: lost (windows gone with no quota eviction mark) and write_failed. Retried at 10 minutes, 1 hour and 6 hours.",
		cf...)
	w.Gauge("flodni_warm_queue_files", "Files waiting for warm on add.", promSample(int64(st.QueueFiles)))
	paused := int64(0)
	if st.Paused {
		paused = 1
	}
	w.Gauge("flodni_warm_paused", "1 while warm on add is paused for a playback.", promSample(paused))
}
