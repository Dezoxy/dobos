// Package warmer is warm on add (Dezoxy fork, Readiness step 2): when the library files a
// title, it completes the head and tail windows of the title's files in the background,
// so the first play does not wait for the swarm. It fills through warmup.FillWindows and
// records what happened per file. Everything it touches is injected (Deps), so it needs
// no engine, cache or database to be tested.
//
// The queue is in memory: a restart forgets it, and the per-file record says which files
// were never warmed. Two workers take one hash at a time. Playback pauses all of it.
package warmer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"flodni/internal/warmup"
)

// Trigger is the label of every fill, log line and counter this package makes.
const Trigger = "add"

// Outcomes of an attempt, the outcome label of flodni_warm_fills_total. A pause, a
// shutdown and a removal record nothing and are not outcomes.
const (
	OutcomeWarmed           = "warmed"
	OutcomePresent          = "present"
	OutcomeAlreadyWarm      = "already_warm"
	OutcomeNoRoom           = "no_room"
	OutcomeFailed           = "failed"
	OutcomeEvicted          = "evicted"
	OutcomeLost             = "lost"
	OutcomeWriteFailed      = "write_failed"
	OutcomeWakeError        = "wake_error"
	OutcomeSkipped          = "skipped"
	OutcomeDroppedQueueFull = "dropped_queue_full"
)

// Outcomes lists every outcome, so the metrics can emit each at 0.
var Outcomes = []string{OutcomeWarmed, OutcomePresent, OutcomeAlreadyWarm, OutcomeNoRoom, OutcomeFailed,
	OutcomeEvicted, OutcomeLost, OutcomeWriteFailed, OutcomeWakeError, OutcomeSkipped, OutcomeDroppedQueueFull}

// Values of the per-file record the warmer writes.
const (
	ReadWarmed  = "warmed"
	ReadPresent = "present"
	ReadFailed  = "failed"

	CacheCached            = "cached"
	CacheEvicted           = "evicted"
	CacheLost              = "lost"
	CacheWriteFailed       = "write_failed"
	CacheDroppedQuota      = "dropped_quota"
	CacheDroppedNoEvidence = "dropped_no_evidence"
)

// CacheFailureReasons are the reason labels of flodni_warm_cache_failures_total.
var CacheFailureReasons = []string{CacheLost, CacheWriteFailed}

// Why a read gave up, as readability.go spells it. After either of these the swarm is
// not there at all, so the rest of the job would only wait out the same timeouts.
const (
	reasonNoPeers   = "no_peers"
	reasonNotActive = "not_active"
)

// File is one media file of an added title: the engine's file id and its size.
type File struct {
	Index int
	Size  int64
}

// Update is one observation for the per-file record. An empty Readability or Cache
// leaves that field alone; a zero time is absent.
type Update struct {
	Key         warmup.FileKey
	Readability string
	Cache       string
	DeliveredAt time.Time
	AttemptAt   time.Time
}

// Deps is everything the warmer needs from outside. Every func is required except
// those with a default (Now, Log, Classify, EvictedSince).
type Deps struct {
	// Enabled is read at enqueue.
	Enabled func() bool
	// PlaybackOpen reports a playback in progress. It may cache; it is polled by every
	// worker before it takes a job and by each fill every 500 ms.
	PlaybackOpen func() bool
	// Live reports that the engine holds the torrent in RAM.
	Live func(hash string) bool
	// WakeStored wakes a closed torrent from the hash alone and must refuse a hash the
	// engine no longer stores (hard rule 3).
	WakeStored func(ctx context.Context, hash string) error
	// Policy is the eviction policy in force now: never EvictAny.
	Policy func() warmup.EvictPolicy
	// Fill is warmup.FillWindows.
	Fill func(warmup.FillRequest) (warmup.FillResult, error)
	// DeliveredAt is the time the swarm delivered a piece under the file's windows,
	// zero when no stamp exists.
	DeliveredAt func(k warmup.FileKey, fileSize int64) time.Time
	// EvictedSince reports a confirmed quota eviction of the file at or after since.
	EvictedSince func(k warmup.FileKey, since time.Time) bool
	// Record applies an Update to the per-file record.
	Record func(Update)
	// SwarmRead stamps the per-torrent record: the swarm delivered bytes at that time.
	SwarmRead func(hash string, at time.Time)
	// Classify says why a timed-out fill got nothing: no_peers, not_active or timeout.
	Classify func(hash string) string
	// RemoveCache deletes the hash's cache files and forgets its records. Called again
	// after a fill that was cancelled by a removal.
	RemoveCache func(hash string)

	Now func() time.Time
	// Log receives one "[Warm] {json}" line per attempt.
	Log func(line string)
	// Stop ends the workers; closing it is shutdown.
	Stop <-chan struct{}

	Workers   int // 0 means 2
	MaxQueued int // 0 means 4096 files
	// RetryDelays are the waits before each retry of a cache-side failure; nil means
	// 10 minutes, 1 hour and 6 hours, then the file is left alone.
	RetryDelays []time.Duration
	ResumePoll  time.Duration // 0 means 5 s: playback must be clear on two polls this far apart
	EvictWait   time.Duration // 0 means 1 s: how long to wait for the eviction mark
}

// Stats is a snapshot for the metrics.
type Stats struct {
	Fills         map[string]int64 // by outcome; every outcome present
	HeadBytes     int64
	TailBytes     int64
	CacheFailures map[string]int64 // by reason; every reason present
	QueueFiles    int
	Paused        bool
}

type file struct {
	idx       int
	size      int64
	delivered time.Time // latest swarm delivery seen for this file, across attempts
	retries   int
}

type job struct {
	hash  string
	files []*file // still to do, in order
	all   []warmup.FileKey
	epoch uint64
}

// Warmer is the queue and its workers. A nil *Warmer ignores Add and Cancel and reports
// zeroed stats.
type Warmer struct {
	d      Deps
	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	cond    *sync.Cond
	pending map[string]*job // queued work by hash, merged
	fifo    []string        // hashes of pending, oldest first
	running map[string]*job // a worker never takes a hash that is running
	queued  int             // files in pending
	tomb    map[string]bool // removed hashes, until a later Add of the same hash
	epoch   map[string]uint64
	paused  bool
	closed  bool

	// recMu is held while an attempt writes its records; Cancel passes through it.
	recMu sync.Mutex

	protectMu sync.RWMutex
	protected map[warmup.FileKey]int

	smu           sync.Mutex
	fills         map[string]int64
	headBytes     int64
	tailBytes     int64
	cacheFailures map[string]int64
}

// New starts the workers. They end when d.Stop closes.
func New(d Deps) *Warmer {
	if d.Workers <= 0 {
		d.Workers = 2
	}
	if d.MaxQueued <= 0 {
		d.MaxQueued = 4096
	}
	if d.RetryDelays == nil {
		d.RetryDelays = []time.Duration{10 * time.Minute, time.Hour, 6 * time.Hour}
	}
	if d.ResumePoll <= 0 {
		d.ResumePoll = 5 * time.Second
	}
	if d.EvictWait <= 0 {
		d.EvictWait = time.Second
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = func(string) {}
	}
	if d.Classify == nil {
		d.Classify = func(string) string { return "timeout" }
	}
	if d.EvictedSince == nil {
		d.EvictedSince = func(warmup.FileKey, time.Time) bool { return false }
	}
	w := &Warmer{
		d: d, pending: map[string]*job{}, running: map[string]*job{}, tomb: map[string]bool{},
		epoch: map[string]uint64{}, protected: map[warmup.FileKey]int{},
		fills: zeroed(Outcomes), cacheFailures: zeroed(CacheFailureReasons),
	}
	w.ctx, w.cancel = context.WithCancel(context.Background())
	w.cond = sync.NewCond(&w.mu)
	go func() {
		<-d.Stop
		w.mu.Lock()
		w.closed = true
		w.cond.Broadcast()
		w.mu.Unlock()
		w.cancel()
	}()
	for i := 0; i < d.Workers; i++ {
		go w.worker()
	}
	return w
}

func zeroed(keys []string) map[string]int64 {
	m := make(map[string]int64, len(keys))
	for _, k := range keys {
		m[k] = 0
	}
	return m
}

// Add queues the files of an added title. It only enqueues: memory, no blocking. It
// does nothing while the switch is off. Calls for one hash merge, so a pack filed
// episode by episode loses no file, and a later Add clears a removal's tombstone.
func (w *Warmer) Add(hash string, files []File) {
	if w == nil || !w.d.Enabled() {
		return
	}
	hash = strings.ToLower(hash)
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	delete(w.tomb, hash)
	dropped := 0
	for _, f := range files {
		if f.Size <= 0 || f.Index < 0 {
			continue
		}
		if !w.enqueueLocked(hash, &file{idx: f.Index, size: f.Size}) {
			dropped++
		}
	}
	w.count(OutcomeDroppedQueueFull, dropped)
	w.cond.Broadcast()
}

// enqueueLocked adds one file to the hash's pending job, creating it. It reports false
// when the queue is full. A file already queued for the hash is kept as it is.
func (w *Warmer) enqueueLocked(hash string, f *file) bool {
	j := w.pending[hash]
	if j != nil {
		for _, q := range j.files {
			if q.idx == f.idx {
				return true
			}
		}
	}
	if w.queued >= w.d.MaxQueued {
		return false
	}
	if j == nil {
		j = &job{hash: hash, epoch: w.epoch[hash]}
		w.pending[hash] = j
		w.fifo = append(w.fifo, hash)
	}
	j.files = append(j.files, f)
	w.queued++
	return true
}

// Cancel is a removal of the hash: its queued files go, a running fill stops, nothing
// more is recorded or woken for it until a later Add of the same hash. A nil Warmer is
// a no-op.
func (w *Warmer) Cancel(hash string) {
	if w == nil {
		return
	}
	hash = strings.ToLower(hash)
	w.mu.Lock()
	w.tomb[hash] = true
	w.epoch[hash]++
	if j := w.pending[hash]; j != nil {
		w.queued -= len(j.files)
		delete(w.pending, hash)
		for i, h := range w.fifo {
			if h == hash {
				w.fifo = append(w.fifo[:i], w.fifo[i+1:]...)
				break
			}
		}
	}
	w.mu.Unlock()
	// An attempt writing its records finishes first, so the forget that follows this
	// call, and the library's delete of the per-torrent row, come after them.
	w.recMu.Lock()
	w.recMu.Unlock() //nolint:staticcheck // a barrier, not a critical section
}

// Stats returns a snapshot. A nil Warmer reports every series at 0.
func (w *Warmer) Stats() Stats {
	if w == nil {
		return Stats{Fills: zeroed(Outcomes), CacheFailures: zeroed(CacheFailureReasons)}
	}
	st := Stats{Fills: map[string]int64{}, CacheFailures: map[string]int64{}}
	w.smu.Lock()
	for k, v := range w.fills {
		st.Fills[k] = v
	}
	for k, v := range w.cacheFailures {
		st.CacheFailures[k] = v
	}
	st.HeadBytes, st.TailBytes = w.headBytes, w.tailBytes
	w.smu.Unlock()
	w.mu.Lock()
	st.QueueFiles, st.Paused = w.queued, w.paused
	w.mu.Unlock()
	return st
}

func (w *Warmer) count(outcome string, n int) {
	if n == 0 {
		return
	}
	w.smu.Lock()
	w.fills[outcome] += int64(n)
	w.smu.Unlock()
}

// cancelledLocked: the job's hash was removed since the job was queued. A tombstone
// cleared by a later Add does not revive an older job: its epoch is stale.
func (w *Warmer) cancelledLocked(j *job) bool {
	return w.tomb[j.hash] || w.epoch[j.hash] != j.epoch
}

func (w *Warmer) cancelled(j *job) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.cancelledLocked(j)
}

func (w *Warmer) isClosed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closed
}

// pause starts a pause if none is on; the resume loop ends it.
func (w *Warmer) pause() {
	w.mu.Lock()
	start := !w.paused && !w.closed
	if start {
		w.paused = true
	}
	w.mu.Unlock()
	if start {
		go w.resumeLoop()
	}
}

// resumeLoop ends the pause once playback was clear on two polls in a row, ResumePoll
// apart: a media server pausing and resuming must not bounce the fill.
func (w *Warmer) resumeLoop() {
	clearPolls := 0
	for clearPolls < 2 {
		select {
		case <-w.d.Stop:
			return
		case <-time.After(w.d.ResumePoll):
		}
		if w.d.PlaybackOpen() {
			clearPolls = 0
		} else {
			clearPolls++
		}
	}
	w.mu.Lock()
	w.paused = false
	w.cond.Broadcast()
	w.mu.Unlock()
}

// pausedNow reports a pause, starting one when playback just opened.
func (w *Warmer) pausedNow() bool {
	w.mu.Lock()
	p := w.paused
	w.mu.Unlock()
	if p {
		return true
	}
	if w.d.PlaybackOpen() {
		w.pause()
		return true
	}
	return false
}

func (w *Warmer) protect(k warmup.FileKey) bool {
	w.protectMu.RLock()
	defer w.protectMu.RUnlock()
	k.Hash = strings.ToLower(k.Hash)
	return w.protected[k] > 0
}

func (w *Warmer) setProtected(j *job, delta int) {
	w.protectMu.Lock()
	for _, k := range j.all {
		w.protected[k] += delta
		if w.protected[k] <= 0 {
			delete(w.protected, k)
		}
	}
	w.protectMu.Unlock()
}

func (w *Warmer) worker() {
	for {
		j := w.next()
		if j == nil {
			return
		}
		w.runJob(j)
		w.mu.Lock()
		delete(w.running, j.hash)
		w.cond.Broadcast()
		w.mu.Unlock()
		w.setProtected(j, -1)
	}
}

// takeableLocked is the index in fifo of the oldest hash no worker has, or -1.
func (w *Warmer) takeableLocked() int {
	for i, h := range w.fifo {
		if w.running[h] == nil {
			return i
		}
	}
	return -1
}

// next blocks until a job can run: work is queued for a hash nobody has, no pause is on
// and no playback is open. It returns nil at shutdown.
func (w *Warmer) next() *job {
	for {
		w.mu.Lock()
		for !w.closed && (w.paused || w.takeableLocked() < 0) {
			w.cond.Wait()
		}
		closed := w.closed
		w.mu.Unlock()
		if closed {
			return nil
		}
		if w.d.PlaybackOpen() {
			w.pause()
			continue
		}
		w.mu.Lock()
		i := w.takeableLocked()
		if w.closed || w.paused || i < 0 {
			w.mu.Unlock()
			continue
		}
		hash := w.fifo[i]
		w.fifo = append(w.fifo[:i], w.fifo[i+1:]...)
		j := w.pending[hash]
		delete(w.pending, hash)
		w.queued -= len(j.files)
		w.running[hash] = j
		j.all = make([]warmup.FileKey, len(j.files))
		for n, f := range j.files {
			j.all[n] = warmup.FileKey{Hash: hash, FileIdx: f.idx}
		}
		w.mu.Unlock()
		w.setProtected(j, 1)
		return j
	}
}

type action int

const (
	actNext      action = iota // the file is done: go on
	actRequeue                 // paused: put this file and the rest back
	actSkipRest                // the swarm or the wake failed: leave the job's other files
	actCancelled               // the hash was removed
	actClosed                  // shutdown
)

func (w *Warmer) runJob(j *job) {
	for len(j.files) > 0 {
		if w.isClosed() {
			return
		}
		if !w.d.Enabled() {
			return // switched off: the rest of the queue is dropped
		}
		if w.cancelled(j) {
			w.cleanup(j)
			return
		}
		if w.pausedNow() {
			w.requeue(j)
			return
		}
		switch w.attemptSafe(j, j.files[0]) {
		case actNext:
			j.files = j.files[1:]
		case actRequeue:
			w.requeue(j)
			return
		case actSkipRest:
			// The files left behind are counted, so the outcomes add up to the files
			// queued. Not when the switch was turned off: that drops the queue.
			if w.d.Enabled() {
				w.count(OutcomeSkipped, len(j.files)-1)
			}
			return
		case actClosed:
			return
		case actCancelled:
			w.cleanup(j)
			return
		}
	}
}

// cleanup removes the cache files of a hash whose removal this job outlived: the fill
// may have written after the removal deleted them. A later Add of the same hash clears
// the tombstone, and then the files belong to the new torrent.
func (w *Warmer) cleanup(j *job) {
	w.mu.Lock()
	tomb := w.tomb[j.hash]
	w.mu.Unlock()
	if tomb {
		w.d.RemoveCache(j.hash)
	}
}

// requeue puts the job's remaining files back at the front of the queue, keeping each
// file's delivery tally.
func (w *Warmer) requeue(j *job) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.cancelledLocked(j) {
		return
	}
	p := w.pending[j.hash]
	if p == nil {
		p = &job{hash: j.hash, epoch: j.epoch}
		w.pending[j.hash] = p
		w.fifo = append([]string{j.hash}, w.fifo...)
	}
	have := map[int]bool{}
	for _, f := range p.files {
		have[f.idx] = true
	}
	var front []*file
	for _, f := range j.files {
		if !have[f.idx] {
			front = append(front, f)
		}
	}
	p.files = append(front, p.files...)
	w.queued += len(front)
}

// scheduleRetry queues the file again after the next delay of the backoff, unless the
// schedule is spent.
func (w *Warmer) scheduleRetry(j *job, f *file) {
	if f.retries >= len(w.d.RetryDelays) {
		return
	}
	delay := w.d.RetryDelays[f.retries]
	f.retries++
	epoch := j.epoch
	time.AfterFunc(delay, func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.closed || w.tomb[j.hash] || w.epoch[j.hash] != epoch {
			return
		}
		if !w.enqueueLocked(j.hash, f) {
			w.count(OutcomeDroppedQueueFull, 1)
			return
		}
		w.cond.Broadcast()
	})
}

// record counts the outcome and writes the records, unless the hash was removed. Cancel
// waits for it, so a removal's forget comes after the records.
func (w *Warmer) record(j *job, f *file, u Update, outcome string, retry, stampSwarm bool) bool {
	w.recMu.Lock()
	defer w.recMu.Unlock()
	if w.cancelled(j) {
		return false
	}
	w.count(outcome, 1)
	if retry {
		w.smu.Lock()
		w.cacheFailures[u.Cache]++
		w.smu.Unlock()
		w.scheduleRetry(j, f)
	}
	w.d.Record(u)
	if stampSwarm {
		w.d.SwarmRead(j.hash, f.delivered)
	}
	return true
}

// attemptSafe is attempt with a recover: the fill calls into the engine on a bare
// goroutine, and a panic there must not take the daemon down.
func (w *Warmer) attemptSafe(j *job, f *file) (act action) {
	defer func() {
		if r := recover(); r != nil {
			w.d.Log(fmt.Sprintf("WARNING: warm on add: recovered on %s/%d: %v", j.hash, f.idx, r))
			act = actSkipRest
		}
	}()
	return w.attempt(j, f)
}

// stopFill is the Stop func of a fill.
func (w *Warmer) stopFill(j *job) bool {
	if w.isClosed() || w.cancelled(j) || !w.d.Enabled() {
		return true
	}
	return w.pausedNow()
}

// evictedSince polls the eviction mark for up to EvictWait: the evictor sets it after it
// drops its locks, so a fill can see the file gone before the mark exists.
func (w *Warmer) evictedSince(k warmup.FileKey, since time.Time) bool {
	deadline := time.Now().Add(w.d.EvictWait)
	step := max(w.d.EvictWait/20, time.Millisecond)
	for {
		if w.d.EvictedSince(k, since) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(step)
	}
}

type logLine struct {
	Hash            string  `json:"hash"`
	File            int     `json:"file"`
	Trigger         string  `json:"trigger"`
	Outcome         string  `json:"outcome"`
	Cache           string  `json:"cache"`
	Reason          string  `json:"reason"`
	HeadBytes       int64   `json:"head_bytes"`
	TailBytes       int64   `json:"tail_bytes"`
	DeliveredPieces int     `json:"delivered_pieces"`
	DeliveredAt     *string `json:"delivered_at"`
	Evicted         int     `json:"evicted"`
	DurationMS      int64   `json:"duration_ms"`
}

func (w *Warmer) logAttempt(l logLine, delivered time.Time, started time.Time) {
	if !delivered.IsZero() {
		s := delivered.UTC().Format(time.RFC3339)
		l.DeliveredAt = &s
	}
	l.DurationMS = time.Since(started).Milliseconds()
	b, err := json.Marshal(l)
	if err != nil {
		return
	}
	w.d.Log("[Warm] " + string(b))
}

// attempt warms one file: wake if needed, fill, then record the outcome.
func (w *Warmer) attempt(j *job, f *file) action {
	key := warmup.FileKey{Hash: j.hash, FileIdx: f.idx}
	started := time.Now()
	line := logLine{Hash: j.hash, File: f.idx, Trigger: Trigger}

	// Wake: only a torrent the engine closed, and only through WakeStored, which
	// refuses a hash it no longer stores. The tombstone is checked on both sides.
	if !w.d.Live(j.hash) {
		if w.cancelled(j) {
			return actCancelled
		}
		if err := w.d.WakeStored(w.ctx, j.hash); err != nil {
			if w.isClosed() {
				return actClosed
			}
			if w.cancelled(j) {
				return actCancelled
			}
			w.count(OutcomeWakeError, 1)
			line.Outcome, line.Reason = OutcomeWakeError, err.Error()
			w.logAttempt(line, f.delivered, started)
			return actSkipRest
		}
		if w.cancelled(j) {
			return actCancelled
		}
	}

	policy := w.d.Policy()
	attemptAt := w.d.Now()
	res, err := w.d.Fill(warmup.FillRequest{
		Key: key, FileSize: f.size, Policy: policy, Trigger: warmup.TriggerWarmAdd,
		Protect: w.protect, Stop: func() bool { return w.stopFill(j) },
	})
	w.smu.Lock()
	w.headBytes += res.HeadBytes
	w.tailBytes += res.TailBytes
	w.smu.Unlock()
	line.HeadBytes, line.TailBytes, line.DeliveredPieces, line.Evicted = res.HeadBytes, res.TailBytes, res.DeliveredPieces, res.Evicted

	// The delivery time, after every return, errors too: a resumed fill that finds the
	// windows complete from RAM must still carry the evidence of the earlier attempt.
	// A fill that fetched nothing adds none: a scan that filled the windows recorded
	// its own delivery.
	fetched := !(err == nil && (res.AlreadyWarm || res.HeadBytes+res.TailBytes == 0))
	if fetched {
		if at := w.d.DeliveredAt(key, f.size); at.After(f.delivered) {
			f.delivered = at
		}
	}

	if errors.Is(err, warmup.ErrStopped) {
		line.Outcome = "stopped"
		w.logAttempt(line, f.delivered, started)
		switch {
		case w.isClosed():
			return actClosed
		case w.cancelled(j):
			return actCancelled
		case !w.d.Enabled():
			return actSkipRest
		}
		return actRequeue
	}
	if w.cancelled(j) {
		line.Outcome = "cancelled"
		w.logAttempt(line, f.delivered, started)
		return actCancelled
	}

	u := Update{Key: key, AttemptAt: attemptAt}
	act := actNext
	retry := false
	stampSwarm := false
	// delivered turns the readability into warmed with the delivery time (a cache-side
	// failure after the swarm delivered is still swarm evidence).
	delivered := func() {
		if !f.delivered.IsZero() {
			u.Readability, u.DeliveredAt, stampSwarm = ReadWarmed, f.delivered, true
		}
	}
	switch {
	case err == nil && !fetched:
		line.Outcome, u.Cache = OutcomeAlreadyWarm, CacheCached
		delivered() // only a delivery this file's own earlier attempt made
	case err == nil:
		line.Outcome, u.Cache = OutcomeWarmed, CacheCached
		if f.delivered.IsZero() {
			line.Outcome, u.Readability = OutcomePresent, ReadPresent
		} else {
			delivered()
		}
	case errors.Is(err, warmup.ErrNoRoom):
		line.Outcome = OutcomeNoRoom
		u.Cache = CacheDroppedNoEvidence
		if policy == warmup.EvictNeverPlayed {
			u.Cache = CacheDroppedQuota
		}
	case errors.Is(err, warmup.ErrTimeout):
		line.Outcome, u.Readability, u.DeliveredAt = OutcomeFailed, ReadFailed, f.delivered
		line.Reason = w.d.Classify(j.hash)
		if line.Reason == reasonNoPeers || line.Reason == reasonNotActive {
			act = actSkipRest
		}
	case errors.Is(err, warmup.ErrEvicted) && w.evictedSince(key, attemptAt):
		line.Outcome, u.Cache = OutcomeEvicted, CacheEvicted
	case errors.Is(err, warmup.ErrEvicted):
		line.Outcome, u.Cache, retry = OutcomeLost, CacheLost, true
		line.Reason = "evicted without a quota eviction mark"
		delivered()
	default:
		line.Outcome, u.Cache, retry = OutcomeWriteFailed, CacheWriteFailed, true
		line.Reason = err.Error()
		delivered()
	}
	line.Cache = u.Cache
	if !w.record(j, f, u, line.Outcome, retry, stampSwarm) {
		line.Outcome, line.Cache = "cancelled", ""
		w.logAttempt(line, f.delivered, started)
		return actCancelled
	}
	w.logAttempt(line, f.delivered, started)
	if w.cancelled(j) {
		return actCancelled
	}
	return act
}
