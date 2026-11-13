package main

import (
	"sort"
	"sync/atomic"
	"time"

	"flodni/internal/gostorm/torr"
	"flodni/internal/metadb"
	"flodni/internal/warmup"
)

// The per-file warm record (Readiness step 2): what the library knows about whether the
// swarm can serve a file's head, and what state the cache holds its windows in. The two
// are separate fields on purpose: an eviction never changes readability. The record is
// EXPERIMENTAL (README); today only reads through the mount write it.
const (
	readWarmed  = "warmed"  // the swarm delivered the windows' pieces
	readPresent = "present" // complete with no delivery time available
	readFailed  = "failed"  // an attempt got no bytes from the swarm
	readUnknown = "unknown" // served for an empty value

	cacheCached  = "cached"
	cacheEvicted = "evicted" // quota eviction removed the windows

	// Cache states warm on add writes when the windows were not kept (the values of
	// warmer.Cache*): the scope "retries" of catch-up feeds these files again.
	cacheDroppedQuota      = "dropped_quota"
	cacheDroppedNoEvidence = "dropped_no_evidence"
	cacheLost              = "lost"
	cacheWriteFailed       = "write_failed"
)

// fileWarm is the record of one file, times in Unix seconds, 0 = unset. Empty
// readability or cache means nothing recorded yet.
type fileWarm struct {
	readability    string
	cache          string
	failedAttempts int64
	firstFailedAt  int64
	lastAttemptAt  int64
	lastDeliveryAt int64
	// queuedAt is the time warm on add queued the file, 0 when it waits for no attempt.
	// An attempt that records an outcome clears it. It is not a record of the API
	// (recorded ignores it): it only tells the catch-up scope "retries" that an attempt
	// began and never finished.
	queuedAt int64
}

func (w *fileWarm) merge(r metadb.FileState) {
	if w.readability == "" && w.cache == "" && w.failedAttempts == 0 && w.lastDeliveryAt == 0 && w.queuedAt == 0 {
		*w = fileWarm{r.Readability, r.Cache, r.FailedAttempts, r.FirstFailedAt, r.LastAttemptAt, r.LastSwarmDeliveryAt, r.QueuedAt}
	}
}

func (w fileWarm) recorded() bool {
	return w.readability != "" || w.cache != "" || w.failedAttempts > 0 || w.lastAttemptAt > 0 || w.lastDeliveryAt > 0
}

// warmUpdate is one observation. An empty readability or cache leaves that field alone;
// zero times are absent. deliveredAt only moves the stored time forward.
type warmUpdate struct {
	key         warmup.FileKey
	readability string
	cache       string
	deliveredAt time.Time
	attemptAt   time.Time
	queuedAt    time.Time // warm on add queued the file now
	clearQueued bool      // an attempt recorded an outcome: the queued mark ends
}

// apply returns the record after u. warmed resets the failure streak; failed counts
// it and starts it at the attempt time, and replaces warmed (the delivery time stays).
// present carries no evidence, so it never replaces warmed.
func (w fileWarm) apply(u warmUpdate, now time.Time) fileWarm {
	read := u.readability
	if read == readPresent && w.readability == readWarmed {
		read = ""
	}
	if read != "" {
		w.readability = read
		switch read {
		case readWarmed:
			w.failedAttempts, w.firstFailedAt = 0, 0
		case readFailed:
			w.failedAttempts++
			if w.firstFailedAt == 0 {
				at := u.attemptAt
				if at.IsZero() {
					at = now
				}
				w.firstFailedAt = at.Unix()
			}
		}
	}
	if u.cache != "" {
		w.cache = u.cache
	}
	if !u.attemptAt.IsZero() {
		w.lastAttemptAt = u.attemptAt.Unix()
	}
	if u.clearQueued {
		w.queuedAt = 0
	}
	if !u.queuedAt.IsZero() {
		w.queuedAt = u.queuedAt.Unix()
	}
	if at := u.deliveredAt.Unix(); !u.deliveredAt.IsZero() && at > w.lastDeliveryAt {
		w.lastDeliveryAt = at
	}
	return w
}

// RecordWarm applies u to the file's record and stores it, reporting whether anything
// changed. It holds warmMu from the memory update to the store write, so an eviction
// cannot be overtaken; it never holds mu over the write (IsPlayed reads under the
// cache's quota lock).
func (x *fileStateIndex) RecordWarm(u warmUpdate) bool {
	k := normKey(u.key)
	if k.FileIdx < 0 {
		return false
	}
	now := x.now()
	x.warmMu.Lock()
	defer x.warmMu.Unlock()
	x.mu.Lock()
	e := x.m[k]
	var cur fileWarm
	if e != nil {
		cur = e.warm
	}
	next := cur.apply(u, now)
	if next == cur {
		x.mu.Unlock()
		return false
	}
	if e == nil {
		e = &fileStateEntry{}
		x.m[k] = e
	}
	e.warm = next
	st := x.store
	x.mu.Unlock()
	if st != nil {
		err := st.SaveFileWarm(metadb.FileWarm{
			Hash: k.Hash, FileIdx: k.FileIdx, Readability: next.readability, Cache: next.cache,
			FailedAttempts: next.failedAttempts, FirstFailedAt: next.firstFailedAt,
			LastAttemptAt: next.lastAttemptAt, LastSwarmDeliveryAt: next.lastDeliveryAt, QueuedAt: next.queuedAt,
			UpdatedAt: now.Unix(),
		})
		if err != nil {
			x.logf("WARNING: file state: cannot store the warm record of %s/%d: %v", k.Hash, k.FileIdx, err)
		}
	}
	return true
}

func orUnknown(s string) string {
	if s == "" {
		return readUnknown
	}
	return s
}

// WarmSnapshot returns the records for the API, by lower-case hash, sorted by file
// index. A row that carries only a played mark has no record and is left out. Memory
// only.
func (x *fileStateIndex) WarmSnapshot() map[string][]fileHealth {
	out := map[string][]fileHealth{}
	x.mu.Lock()
	for k, e := range x.m {
		if !e.warm.recorded() {
			continue
		}
		w := e.warm
		out[k.Hash] = append(out[k.Hash], fileHealth{
			FileIndex:           k.FileIdx,
			Readability:         orUnknown(w.readability),
			Cache:               orUnknown(w.cache),
			FailedAttempts:      w.failedAttempts,
			FirstFailedAt:       unixOrNil(w.firstFailedAt),
			LastAttemptAt:       unixOrNil(w.lastAttemptAt),
			LastSwarmDeliveryAt: unixOrNil(w.lastDeliveryAt),
		})
	}
	x.mu.Unlock()
	for _, fs := range out {
		sort.Slice(fs, func(i, j int) bool { return fs[i].FileIndex < fs[j].FileIndex })
	}
	return out
}

// Counters for flodni_warm_fills_total{trigger="mount"}: records the recorder wrote
// when a file's windows were completed by reads through the mount.
var (
	warmFillsMountWarmed atomic.Int64
	warmFillsMountCached atomic.Int64
)

// windowsCached is warmup.OnWindowsCached: memory only and non-blocking, because it can
// run on a FUSE read. A file already recorded as cached and warmed is skipped; the rest
// goes to the pending map for the recorder.
func (x *fileStateIndex) windowsCached(k warmup.FileKey, fileSize int64) {
	k = normKey(k)
	x.mu.Lock()
	if e := x.m[k]; e != nil && e.warm.cache == cacheCached && e.warm.readability == readWarmed {
		x.mu.Unlock()
		return
	}
	x.pending[k] = fileSize
	x.mu.Unlock()
	select {
	case x.wake <- struct{}{}:
	default: // a wake is already queued
	}
}

// drainPending records every pending file. The engine stamp, when it exists, makes the
// record warmed with the stamp's time (not now: the delivery happened when the pieces
// arrived, maybe hours before the windows completed); without one only the cache state
// is recorded.
func (x *fileStateIndex) drainPending() {
	x.mu.Lock()
	batch := x.pending
	x.pending = map[warmup.FileKey]int64{}
	deliveredAt := x.deliveredAt
	x.mu.Unlock()
	for k, size := range batch {
		x.recordPending(k, size, deliveredAt)
	}
}

// recordPending records one file. It recovers: it calls into the engine on a bare
// goroutine, and a panic there must not take the daemon down.
func (x *fileStateIndex) recordPending(k warmup.FileKey, size int64, deliveredAt func(warmup.FileKey, int64) time.Time) {
	defer func() {
		if r := recover(); r != nil {
			x.logf("WARNING: file state: warm recorder recovered on %s/%d: %v", k.Hash, k.FileIdx, r)
		}
	}()
	var at time.Time
	if deliveredAt != nil {
		at = deliveredAt(k, size)
	}
	if at.IsZero() {
		if x.RecordWarm(warmUpdate{key: k, cache: cacheCached}) {
			warmFillsMountCached.Add(1)
		}
		return
	}
	if x.RecordWarm(warmUpdate{key: k, readability: readWarmed, cache: cacheCached, deliveredAt: at}) {
		warmFillsMountWarmed.Add(1)
	}
}

// runRecorder drains the pending map whenever windowsCached signals, until stop closes,
// then once more (best effort: the state database stays open at shutdown).
func (x *fileStateIndex) runRecorder(stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			x.drainPending()
			return
		case <-x.wake:
			x.drainPending()
		}
	}
}

// warmDeliveredAt is the engine side of the recorder: the later delivery stamp of the
// file's head range and tail range, zero when neither carries one.
func warmDeliveredAt(k warmup.FileKey, fileSize int64) time.Time {
	t := torr.PeekTorrent(k.Hash)
	head, _ := t.DeliveredAt(k.FileIdx, 0, min(warmup.FileSize, fileSize))
	tailStart := warmup.TailStart(fileSize)
	tail, _ := t.DeliveredAt(k.FileIdx, tailStart, fileSize-tailStart)
	if tail.After(head) {
		return tail
	}
	return head
}
