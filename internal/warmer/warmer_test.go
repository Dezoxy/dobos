package warmer

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"flodni/internal/warmup"
)

// The warmer (Readiness step 2, warm on add) against fakes: every collaborator is a
// dependency, so nothing here needs an engine, a cache or a database.

var (
	t20 = time.Date(2026, 11, 12, 20, 0, 0, 0, time.UTC)
	t23 = time.Date(2026, 11, 12, 23, 0, 0, 0, time.UTC)
)

type read struct {
	hash string
	at   time.Time
}

type env struct {
	t    *testing.T
	stop chan struct{}
	w    *Warmer

	mu       sync.Mutex
	enabled  bool
	openFn   func() bool
	live     bool
	wakeErr  error
	wakeHook func(hash string)
	wakes    []string
	policy   warmup.EvictPolicy
	fill     func(call int, req warmup.FillRequest) (warmup.FillResult, error)
	reqs     []warmup.FillRequest
	fillAt   []time.Time
	delivery func(call int) time.Time
	dcalls   int
	evicted  func(warmup.FileKey, time.Time) bool
	reason   string
	recs     []Update
	reads    []read
	removed  []string
	queued   []warmup.FileKey
	logs     []string
	now      time.Time
}

func newEnv(t *testing.T, tweak func(*Deps)) *env {
	t.Helper()
	e := &env{t: t, stop: make(chan struct{}), enabled: true, live: true, policy: warmup.EvictNeverPlayed,
		reason: "timeout", now: t23,
		fill: func(int, warmup.FillRequest) (warmup.FillResult, error) {
			return warmup.FillResult{HeadBytes: 10, TailBytes: 5, DeliveredPieces: 3}, nil
		},
		delivery: func(int) time.Time { return t20 },
	}
	d := Deps{
		Enabled: func() bool { e.mu.Lock(); defer e.mu.Unlock(); return e.enabled },
		PlaybackOpen: func() bool {
			e.mu.Lock()
			fn := e.openFn
			e.mu.Unlock()
			return fn != nil && fn()
		},
		Live: func(string) bool { e.mu.Lock(); defer e.mu.Unlock(); return e.live },
		WakeStored: func(_ context.Context, hash string) error {
			e.mu.Lock()
			e.wakes = append(e.wakes, hash)
			hook, err := e.wakeHook, e.wakeErr
			e.mu.Unlock()
			if hook != nil {
				hook(hash)
			}
			return err
		},
		Policy: func() warmup.EvictPolicy { e.mu.Lock(); defer e.mu.Unlock(); return e.policy },
		Fill: func(req warmup.FillRequest) (warmup.FillResult, error) {
			e.mu.Lock()
			e.reqs = append(e.reqs, req)
			e.fillAt = append(e.fillAt, time.Now())
			call, fn := len(e.reqs), e.fill
			e.mu.Unlock()
			return fn(call, req)
		},
		DeliveredAt: func(warmup.FileKey, int64) time.Time {
			e.mu.Lock()
			e.dcalls++
			n, fn := e.dcalls, e.delivery
			e.mu.Unlock()
			return fn(n)
		},
		EvictedSince: func(k warmup.FileKey, since time.Time) bool {
			e.mu.Lock()
			fn := e.evicted
			e.mu.Unlock()
			return fn != nil && fn(k, since)
		},
		Record: func(u Update) { e.mu.Lock(); e.recs = append(e.recs, u); e.mu.Unlock() },
		Queued: func(ks []warmup.FileKey) { e.mu.Lock(); e.queued = append(e.queued, ks...); e.mu.Unlock() },
		SwarmRead: func(hash string, at time.Time) {
			e.mu.Lock()
			e.reads = append(e.reads, read{hash, at})
			e.mu.Unlock()
		},
		Classify:    func(string) string { e.mu.Lock(); defer e.mu.Unlock(); return e.reason },
		RemoveCache: func(hash string) { e.mu.Lock(); e.removed = append(e.removed, hash); e.mu.Unlock() },
		Now:         func() time.Time { e.mu.Lock(); defer e.mu.Unlock(); return e.now },
		Log:         func(line string) { e.mu.Lock(); e.logs = append(e.logs, line); e.mu.Unlock() },
		Stop:        e.stop,
		Workers:     2,
		MaxQueued:   4096,
		RetryDelays: []time.Duration{},
		ResumePoll:  2 * time.Millisecond,
		EvictWait:   30 * time.Millisecond,
	}
	if tweak != nil {
		tweak(&d)
	}
	e.w = New(d)
	t.Cleanup(func() { close(e.stop) })
	return e
}

func (e *env) set(f func()) { e.mu.Lock(); f(); e.mu.Unlock() }

func (e *env) until(what string, cond func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			e.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func (e *env) nFills() int          { e.mu.Lock(); defer e.mu.Unlock(); return len(e.reqs) }
func (e *env) nRecs() int           { e.mu.Lock(); defer e.mu.Unlock(); return len(e.recs) }
func (e *env) nWakes() int          { e.mu.Lock(); defer e.mu.Unlock(); return len(e.wakes) }
func (e *env) fills(o string) int64 { return e.w.Stats().Fills[o] }

func (e *env) rec(i int) Update {
	e.mu.Lock()
	defer e.mu.Unlock()
	if i >= len(e.recs) {
		e.t.Fatalf("record %d of %d", i, len(e.recs))
	}
	return e.recs[i]
}

// quiet fails if anything else happens for a short while.
func (e *env) quiet(fills, recs int) {
	e.t.Helper()
	time.Sleep(60 * time.Millisecond)
	if e.nFills() != fills || e.nRecs() != recs {
		e.t.Fatalf("fills = %d, records = %d; want %d and %d", e.nFills(), e.nRecs(), fills, recs)
	}
}

func f(idx int) File { return File{Index: idx, Size: 1 << 30} }

func TestOffSwitchEnqueuesNothing(t *testing.T) {
	e := newEnv(t, nil)
	e.set(func() { e.enabled = false })
	e.w.Add("aa", []File{f(1)})
	if got := e.w.Stats().QueueFiles; got != 0 {
		t.Fatalf("queued %d files with the switch off", got)
	}
	e.quiet(0, 0)
}

func TestTwoAddsOfOneHashMergeAndNeverRunTogether(t *testing.T) {
	gate := make(chan struct{})
	e := newEnv(t, nil)
	var running, peak int
	e.set(func() {
		e.fill = func(call int, req warmup.FillRequest) (warmup.FillResult, error) {
			e.mu.Lock()
			running++
			peak = max(peak, running)
			e.mu.Unlock()
			if call == 1 {
				<-gate
			}
			e.mu.Lock()
			running--
			e.mu.Unlock()
			return warmup.FillResult{HeadBytes: 1}, nil
		}
	})
	e.w.Add("aa", []File{f(1)})
	e.until("the first fill", func() bool { return e.nFills() == 1 })
	// The same hash again while it runs: a pack filed episode by episode. Two workers
	// are free, and neither may take it.
	e.w.Add("aa", []File{f(2)})
	e.w.Add("aa", []File{f(2), f(3)})
	e.quiet(1, 0)
	close(gate)
	e.until("all three files", func() bool { return e.nRecs() == 3 })
	seen := map[int]bool{}
	e.mu.Lock()
	for _, r := range e.reqs {
		seen[r.Key.FileIdx] = true
	}
	e.mu.Unlock()
	if len(seen) != 3 || peak != 1 {
		t.Fatalf("files filled = %v, peak concurrency on one hash = %d; want 1, 2 and 3 and 1", seen, peak)
	}
}

func TestQueueFullDropsAndCounts(t *testing.T) {
	gate := make(chan struct{})
	e := newEnv(t, func(d *Deps) { d.Workers, d.MaxQueued = 1, 3 })
	e.set(func() {
		e.fill = func(call int, _ warmup.FillRequest) (warmup.FillResult, error) {
			if call == 1 {
				<-gate
			}
			return warmup.FillResult{HeadBytes: 1}, nil
		}
	})
	e.w.Add("aa", []File{f(1)})
	e.until("the worker to take it", func() bool { return e.nFills() == 1 })
	e.w.Add("bb", []File{f(1), f(2), f(3), f(4), f(5)})
	e.w.Add("cc", []File{f(1)})
	st := e.w.Stats()
	if st.QueueFiles != 3 || st.Fills[OutcomeDroppedQueueFull] != 3 {
		t.Fatalf("queued = %d, dropped = %d; want 3 and 3", st.QueueFiles, st.Fills[OutcomeDroppedQueueFull])
	}
	close(gate)
	e.until("the kept files", func() bool { return e.nRecs() == 4 })
	if got := e.w.Stats().QueueFiles; got != 0 {
		t.Fatalf("queue gauge = %d after the drain", got)
	}
}

// A fill that sees the stop func go true returns what it wrote and ErrStopped.
func stoppable(req warmup.FillRequest) (warmup.FillResult, error) {
	for !req.Stop() {
		time.Sleep(time.Millisecond)
	}
	return warmup.FillResult{HeadBytes: 4}, warmup.ErrStopped
}

func TestPlaybackPausesARunningFillAndItResumesWithNothingRecorded(t *testing.T) {
	e := newEnv(t, func(d *Deps) { d.Workers = 1 })
	open := false
	e.set(func() {
		e.openFn = func() bool { e.mu.Lock(); defer e.mu.Unlock(); return open }
		e.fill = func(call int, req warmup.FillRequest) (warmup.FillResult, error) {
			if call == 1 {
				return stoppable(req)
			}
			return warmup.FillResult{HeadBytes: 6}, nil
		}
	})
	e.w.Add("aa", []File{f(1)})
	e.until("the fill to start", func() bool { return e.nFills() == 1 })
	e.set(func() { open = true })
	e.until("the pause", func() bool { return e.w.Stats().Paused })
	e.quiet(1, 0) // paused: no second fill, nothing recorded
	if got := e.w.Stats().QueueFiles; got != 1 {
		t.Fatalf("the paused file is not requeued: queue = %d", got)
	}
	for _, o := range Outcomes {
		if e.fills(o) != 0 {
			t.Fatalf("a pause counted outcome %s", o)
		}
	}
	e.set(func() { open = false })
	e.until("the resumed fill", func() bool { return e.nRecs() == 1 })
	if st := e.w.Stats(); st.Paused || st.Fills[OutcomeWarmed] != 1 {
		t.Fatalf("after resume: %+v", st)
	}
}

func TestResumeNeedsTwoClearPollsInARow(t *testing.T) {
	e := newEnv(t, func(d *Deps) { d.Workers = 1 })
	// Poll 1 is the worker's check before it takes the job; polls 2 to 5 are the
	// resume polls: clear, open (the count restarts), clear, clear.
	script := []bool{true, false, true, false, false}
	var calls int
	e.set(func() {
		e.openFn = func() bool {
			e.mu.Lock()
			defer e.mu.Unlock()
			calls++
			return calls <= len(script) && script[calls-1]
		}
		e.fill = func(int, warmup.FillRequest) (warmup.FillResult, error) {
			e.mu.Lock()
			defer e.mu.Unlock()
			if calls < 6 {
				e.t.Errorf("the fill started after %d polls, want it to wait for all 5 of the script", calls)
			}
			return warmup.FillResult{HeadBytes: 1}, nil
		}
	})
	e.w.Add("aa", []File{f(1)})
	e.until("the fill after the resume", func() bool { return e.nRecs() == 1 })
}

func TestResumedFillKeepsTheEarlierDeliveryTime(t *testing.T) {
	// Decision 4: the swarm delivered at 20:00, the fill was paused, and at 23:00 it
	// completed from RAM. The row carries 20:00.
	e := newEnv(t, func(d *Deps) { d.Workers = 1 })
	open := false
	e.set(func() {
		e.openFn = func() bool { e.mu.Lock(); defer e.mu.Unlock(); return open }
		e.delivery = func(n int) time.Time {
			if n == 1 {
				return t20 // after the stopped attempt
			}
			return time.Time{} // the second query finds no stamp any more
		}
		e.fill = func(call int, req warmup.FillRequest) (warmup.FillResult, error) {
			if call == 1 {
				return stoppable(req)
			}
			return warmup.FillResult{HeadBytes: 6}, nil
		}
	})
	e.w.Add("aa", []File{f(1)})
	e.until("the fill to start", func() bool { return e.nFills() == 1 })
	e.set(func() { open = true })
	e.until("the pause", func() bool { return e.w.Stats().Paused })
	e.set(func() { open = false; e.now = t23 })
	e.until("the record", func() bool { return e.nRecs() == 1 })
	u := e.rec(0)
	if u.Readability != ReadWarmed || !u.DeliveredAt.Equal(t20) {
		t.Fatalf("record = %+v, want warmed with the 20:00 delivery", u)
	}
	// The stamp follows the record by a moment.
	e.until("the per-torrent stamp", func() bool { e.mu.Lock(); defer e.mu.Unlock(); return len(e.reads) > 0 })
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.reads) != 1 || !e.reads[0].at.Equal(t20) || e.reads[0].hash != "aa" {
		t.Fatalf("per-torrent stamp = %+v, want one at 20:00", e.reads)
	}
}

func TestFillRequestCarriesThePolicyTheTriggerAndTheProtectSet(t *testing.T) {
	e := newEnv(t, func(d *Deps) { d.Workers = 1 })
	e.set(func() { e.policy = warmup.EvictNone })
	e.w.Add("aa", []File{f(1)})
	e.until("the first fill", func() bool { return e.nRecs() == 1 })
	e.set(func() { e.policy = warmup.EvictNeverPlayed })
	prot := map[warmup.FileKey]bool{}
	e.set(func() {
		e.fill = func(_ int, req warmup.FillRequest) (warmup.FillResult, error) {
			prot[warmup.FileKey{Hash: "bb", FileIdx: 1}] = req.Protect(warmup.FileKey{Hash: "bb", FileIdx: 1})
			prot[warmup.FileKey{Hash: "bb", FileIdx: 2}] = req.Protect(warmup.FileKey{Hash: "bb", FileIdx: 2})
			prot[warmup.FileKey{Hash: "zz", FileIdx: 1}] = req.Protect(warmup.FileKey{Hash: "zz", FileIdx: 1})
			return warmup.FillResult{HeadBytes: 1}, nil
		}
	})
	e.w.Add("bb", []File{f(1), f(2)})
	e.until("the second job", func() bool { return e.nRecs() == 3 })
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.reqs[0].Policy != warmup.EvictNone || e.reqs[1].Policy != warmup.EvictNeverPlayed {
		t.Fatalf("policies = %v, %v; want the one in force at each fill", e.reqs[0].Policy, e.reqs[1].Policy)
	}
	for _, r := range e.reqs {
		if r.Trigger != warmup.TriggerWarmAdd || r.Stop == nil || r.FileSize != 1<<30 {
			t.Fatalf("request = %+v", r)
		}
	}
	if !prot[warmup.FileKey{Hash: "bb", FileIdx: 1}] || !prot[warmup.FileKey{Hash: "bb", FileIdx: 2}] || prot[warmup.FileKey{Hash: "zz", FileIdx: 1}] {
		t.Fatalf("protect set = %v, want both files of the job in flight and no other", prot)
	}
}

var errDisk = errors.New("disk full")

func TestOutcomeMapping(t *testing.T) {
	type row struct {
		name      string
		res       warmup.FillResult
		err       error
		delivered time.Time
		evicted   bool
		policy    warmup.EvictPolicy
		reason    string

		outcome, read, cache string
		wantDelivered        bool // the record carries the delivery time
		swarmRead            bool
		retry                bool
		cacheFailure         string
		skipsRest            bool
	}
	wrote := warmup.FillResult{HeadBytes: 10, TailBytes: 5}
	rows := []row{
		{name: "warmed", res: wrote, delivered: t20, outcome: OutcomeWarmed, read: ReadWarmed, cache: CacheCached, wantDelivered: true, swarmRead: true},
		{name: "present", res: wrote, outcome: OutcomePresent, read: ReadPresent, cache: CacheCached},
		{name: "already warm", res: warmup.FillResult{AlreadyWarm: true}, delivered: t20, outcome: OutcomeAlreadyWarm, cache: CacheCached},
		{name: "no room, never played", err: warmup.ErrNoRoom, policy: warmup.EvictNeverPlayed, outcome: OutcomeNoRoom, cache: CacheDroppedQuota},
		{name: "no room, no evidence", err: warmup.ErrNoRoom, policy: warmup.EvictNone, outcome: OutcomeNoRoom, cache: CacheDroppedNoEvidence},
		{name: "timeout", res: warmup.FillResult{HeadBytes: 1}, err: warmup.ErrTimeout, reason: "timeout", outcome: OutcomeFailed, read: ReadFailed},
		{name: "timeout after partial delivery", err: warmup.ErrTimeout, delivered: t20, reason: "timeout", outcome: OutcomeFailed, read: ReadFailed, wantDelivered: true},
		{name: "timeout, no peers", err: warmup.ErrTimeout, reason: "no_peers", outcome: OutcomeFailed, read: ReadFailed, skipsRest: true},
		{name: "timeout, not active", err: warmup.ErrTimeout, reason: "not_active", outcome: OutcomeFailed, read: ReadFailed, skipsRest: true},
		{name: "evicted, mark newer", err: warmup.ErrEvicted, delivered: t20, evicted: true, outcome: OutcomeEvicted, cache: CacheEvicted},
		{name: "evicted, no mark, no delivery", err: warmup.ErrEvicted, outcome: OutcomeLost, cache: CacheLost, retry: true, cacheFailure: "lost"},
		{name: "evicted, no mark, delivered", err: warmup.ErrEvicted, delivered: t20, outcome: OutcomeLost, read: ReadWarmed, cache: CacheLost, wantDelivered: true, swarmRead: true, retry: true, cacheFailure: "lost"},
		{name: "write failed", err: errDisk, outcome: OutcomeWriteFailed, cache: CacheWriteFailed, retry: true, cacheFailure: "write_failed"},
		{name: "write failed, delivered", err: errDisk, delivered: t20, outcome: OutcomeWriteFailed, read: ReadWarmed, cache: CacheWriteFailed, wantDelivered: true, swarmRead: true, retry: true, cacheFailure: "write_failed"},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			e := newEnv(t, func(d *Deps) { d.Workers = 1; d.RetryDelays = []time.Duration{15 * time.Millisecond} })
			e.set(func() {
				e.policy = r.policy
				if r.policy == 0 && !strings.HasPrefix(r.name, "no room") {
					e.policy = warmup.EvictNeverPlayed
				}
				e.reason = r.reason
				e.delivery = func(int) time.Time { return r.delivered }
				e.evicted = func(k warmup.FileKey, since time.Time) bool {
					if !since.Equal(t23) || k.Hash != "aa" {
						t.Errorf("EvictedSince(%v, %v): want the attempt's start", k, since)
					}
					return r.evicted
				}
				e.fill = func(int, warmup.FillRequest) (warmup.FillResult, error) { return r.res, r.err }
			})
			e.w.Add("aa", []File{f(1), f(2)})
			wantFills := 1
			if r.skipsRest {
				e.until("the failure", func() bool { return e.fills(OutcomeFailed) == 1 })
				e.quiet(1, 1) // the second file of the job is skipped, unrecorded
			} else if r.retry {
				wantFills = 4 // file 1, file 2, then each once more
				e.until("the retries", func() bool { return e.nFills() == 4 })
				e.quiet(4, 4)
			} else {
				wantFills = 2
				e.until("both files", func() bool { return e.nRecs() == 2 })
				e.quiet(2, 2)
			}
			_ = wantFills
			u := e.rec(0)
			if u.Key != (warmup.FileKey{Hash: "aa", FileIdx: 1}) || u.Readability != r.read || u.Cache != r.cache {
				t.Fatalf("record = %+v, want readability %q cache %q", u, r.read, r.cache)
			}
			if got := !u.DeliveredAt.IsZero(); got != r.wantDelivered || (got && !u.DeliveredAt.Equal(t20)) {
				t.Fatalf("record delivery = %v, want present=%v", u.DeliveredAt, r.wantDelivered)
			}
			if u.AttemptAt.IsZero() {
				t.Fatal("record has no attempt time")
			}
			e.mu.Lock()
			reads := len(e.reads)
			e.mu.Unlock()
			if (reads > 0) != r.swarmRead {
				t.Fatalf("per-torrent stamps = %d, want present=%v", reads, r.swarmRead)
			}
			st := e.w.Stats()
			if st.Fills[r.outcome] == 0 {
				t.Fatalf("outcome %s not counted: %+v", r.outcome, st.Fills)
			}
			if r.cacheFailure != "" && st.CacheFailures[r.cacheFailure] == 0 {
				t.Fatalf("cache failure %s not counted: %+v", r.cacheFailure, st.CacheFailures)
			}
			if r.cacheFailure == "" && (st.CacheFailures["lost"]+st.CacheFailures["write_failed"]) != 0 {
				t.Fatalf("cache failures counted for %s: %+v", r.name, st.CacheFailures)
			}
		})
	}
}

func TestFailedAndPresentNeverTouchThePerTorrentRecord(t *testing.T) {
	e := newEnv(t, func(d *Deps) { d.Workers = 1 })
	e.set(func() {
		e.delivery = func(int) time.Time { return time.Time{} }
		e.fill = func(call int, _ warmup.FillRequest) (warmup.FillResult, error) {
			if call == 1 {
				return warmup.FillResult{HeadBytes: 1}, nil // present
			}
			return warmup.FillResult{}, warmup.ErrTimeout // failed
		}
	})
	e.w.Add("aa", []File{f(1), f(2)})
	e.until("both", func() bool { return e.nRecs() == 2 })
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.reads) != 0 {
		t.Fatalf("per-torrent stamps = %+v, want none", e.reads)
	}
}

func TestAlreadyWarmAddsNothingUnlessThisFilesOwnEarlierAttemptDelivered(t *testing.T) {
	// Decision 5: windows a scan filled are not claimed by the warmer.
	e := newEnv(t, func(d *Deps) { d.Workers = 1 })
	e.set(func() {
		e.delivery = func(int) time.Time { return t20 } // a scan's stamp is there
		e.fill = func(int, warmup.FillRequest) (warmup.FillResult, error) {
			return warmup.FillResult{AlreadyWarm: true}, nil
		}
	})
	e.w.Add("aa", []File{f(1)})
	e.until("record", func() bool { return e.nRecs() == 1 })
	if u := e.rec(0); u.Readability != "" || !u.DeliveredAt.IsZero() || u.Cache != CacheCached {
		t.Fatalf("record = %+v, want only cache=cached", u)
	}
	e.mu.Lock()
	n := len(e.reads)
	e.mu.Unlock()
	if n != 0 {
		t.Fatalf("per-torrent stamps = %d for a fill that fetched nothing", n)
	}

	// Own earlier attempt: stopped after delivering, then found the windows complete.
	e2 := newEnv(t, func(d *Deps) { d.Workers = 1 })
	open := false
	e2.set(func() {
		e2.openFn = func() bool { e2.mu.Lock(); defer e2.mu.Unlock(); return open }
		e2.fill = func(call int, req warmup.FillRequest) (warmup.FillResult, error) {
			if call == 1 {
				return stoppable(req)
			}
			return warmup.FillResult{AlreadyWarm: true}, nil
		}
		e2.delivery = func(n int) time.Time {
			if n == 1 {
				return t20
			}
			return time.Time{}
		}
	})
	e2.w.Add("aa", []File{f(1)})
	e2.until("start", func() bool { return e2.nFills() == 1 })
	e2.set(func() { open = true })
	e2.until("pause", func() bool { return e2.w.Stats().Paused })
	e2.set(func() { open = false })
	e2.until("record", func() bool { return e2.nRecs() == 1 })
	if u := e2.rec(0); u.Readability != ReadWarmed || !u.DeliveredAt.Equal(t20) || u.Cache != CacheCached {
		t.Fatalf("record = %+v, want warmed at 20:00", u)
	}
}

func TestCacheSideFailuresRetryOnTheBackoffThenStop(t *testing.T) {
	delays := []time.Duration{10 * time.Millisecond, 30 * time.Millisecond, 50 * time.Millisecond}
	e := newEnv(t, func(d *Deps) { d.Workers = 1; d.RetryDelays = delays })
	e.set(func() {
		e.fill = func(int, warmup.FillRequest) (warmup.FillResult, error) { return warmup.FillResult{}, errDisk }
	})
	e.w.Add("aa", []File{f(1)})
	e.until("four attempts", func() bool { return e.nFills() == 4 })
	e.quiet(4, 4) // the schedule is spent: no fifth
	e.mu.Lock()
	for i, d := range delays {
		if gap := e.fillAt[i+1].Sub(e.fillAt[i]); gap < d {
			t.Errorf("retry %d came after %v, want at least %v", i+1, gap, d)
		}
	}
	e.mu.Unlock()
	st := e.w.Stats()
	if st.CacheFailures["write_failed"] != 4 || st.Fills[OutcomeWriteFailed] != 4 {
		t.Fatalf("counters = %+v %+v, want 4 each", st.CacheFailures, st.Fills)
	}
}

func TestRemovalStopsTheFillRecordsNothingAndWakesNothing(t *testing.T) {
	e := newEnv(t, func(d *Deps) { d.Workers = 1 })
	e.set(func() {
		e.live = false
		e.fill = func(call int, req warmup.FillRequest) (warmup.FillResult, error) {
			if call == 1 {
				return stoppable(req)
			}
			return warmup.FillResult{HeadBytes: 2}, nil
		}
	})
	e.w.Add("aa", []File{f(1), f(2)})
	e.until("the fill", func() bool { return e.nFills() == 1 })
	wakes := e.nWakes()
	e.w.Cancel("aa")
	e.until("cleanup", func() bool { e.mu.Lock(); defer e.mu.Unlock(); return len(e.removed) == 1 })
	e.quiet(1, 0)
	if e.nWakes() != wakes {
		t.Fatalf("woke %d times after the removal, want none", e.nWakes()-wakes)
	}
	e.mu.Lock()
	if e.removed[0] != "aa" {
		t.Fatalf("removed = %v", e.removed)
	}
	e.mu.Unlock()
	if got := e.w.Stats().QueueFiles; got != 0 {
		t.Fatalf("queue = %d after the removal", got)
	}

	// The same hash added again clears the tombstone.
	e.w.Add("aa", []File{f(1)})
	e.until("the new add", func() bool { return e.nRecs() == 1 })
}

// Removals that come in a burst (a library clean-out) each stop their own running fill:
// every fill ends, nothing is recorded or woken for them, and each hash is cleaned up.
func TestManyRemovalsStopManyRunningFills(t *testing.T) {
	const n = 8
	e := newEnv(t, func(d *Deps) { d.Workers = n })
	e.set(func() {
		e.live = false
		e.fill = func(_ int, req warmup.FillRequest) (warmup.FillResult, error) { return stoppable(req) }
	})
	hashes := make([]string, n)
	for i := range hashes {
		hashes[i] = strings.Repeat(string(rune('a'+i)), 2)
		e.w.Add(hashes[i], []File{f(1), f(2)})
	}
	e.until("every fill to start", func() bool { return e.nFills() == n })
	wakes := e.nWakes()

	var wg sync.WaitGroup
	for _, h := range hashes {
		wg.Add(1)
		go func() { defer wg.Done(); e.w.Cancel(h) }()
	}
	wg.Wait()

	e.until("every hash cleaned up", func() bool { e.mu.Lock(); defer e.mu.Unlock(); return len(e.removed) == n })
	e.quiet(n, 0)
	if e.nWakes() != wakes {
		t.Fatalf("woke %d times after the removals, want none", e.nWakes()-wakes)
	}
	if got := e.w.Stats().QueueFiles; got != 0 {
		t.Fatalf("queue = %d after the removals", got)
	}
}

func TestRemovalDuringTheWakeStopsBeforeAndAfterIt(t *testing.T) {
	e := newEnv(t, func(d *Deps) { d.Workers = 1 })
	entered, release := make(chan struct{}), make(chan struct{})
	e.set(func() {
		e.live = false
		e.wakeHook = func(string) { close(entered); <-release }
	})
	e.w.Add("aa", []File{f(1)})
	<-entered
	e.w.Cancel("aa")
	close(release) // the wake succeeds, but the torrent was removed meanwhile
	e.until("cleanup", func() bool { e.mu.Lock(); defer e.mu.Unlock(); return len(e.removed) == 1 })
	e.quiet(0, 0)
}

func TestACancelledHashIsNotWokenAtAll(t *testing.T) {
	gate := make(chan struct{})
	e := newEnv(t, func(d *Deps) { d.Workers = 1 })
	e.set(func() {
		e.live = false
		e.fill = func(call int, req warmup.FillRequest) (warmup.FillResult, error) {
			<-gate
			return warmup.FillResult{HeadBytes: 1}, nil
		}
	})
	e.w.Add("aa", []File{f(1)})
	e.until("the first job", func() bool { return e.nFills() == 1 })
	e.w.Add("bb", []File{f(1)}) // queued behind it
	e.w.Cancel("bb")
	close(gate)
	e.until("aa", func() bool { return e.nRecs() == 1 })
	e.quiet(1, 1)
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, w := range e.wakes {
		if w == "bb" {
			t.Fatal("a cancelled, queued hash was woken")
		}
	}
}

// Hard rule 3, the warmer's half: a hash the engine refuses to wake (it no longer
// stores it) gets no fill, no record and no retry. The engine's half is
// native.TestWakeStoredRefusesAHashTheEngineDoesNotStore.
func TestAnUnstoredHashIsNeverWoken(t *testing.T) {
	e := newEnv(t, nil)
	e.set(func() { e.live = false; e.wakeErr = errors.New("the engine does not store this torrent") })
	e.w.Add("aa", []File{f(1), f(2), f(3)})
	e.until("the wake error", func() bool { return e.fills(OutcomeWakeError) == 1 })
	e.quiet(0, 0)
	if e.nWakes() != 1 {
		t.Fatalf("wakes = %d, want one for the job: its other files are skipped", e.nWakes())
	}
	if got := e.w.Stats().QueueFiles; got != 0 {
		t.Fatalf("queue = %d", got)
	}
}

func TestALiveTorrentIsNotWokenAndAClosedOneIsWokenByHashAlone(t *testing.T) {
	e := newEnv(t, nil)
	e.w.Add("aa", []File{f(1)})
	e.until("fill", func() bool { return e.nRecs() == 1 })
	if e.nWakes() != 0 {
		t.Fatalf("a live torrent was woken %d times", e.nWakes())
	}
	e.set(func() { e.live = false })
	e.w.Add("bb", []File{f(1)})
	e.until("fill", func() bool { return e.nRecs() == 2 })
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.wakes) != 1 || e.wakes[0] != "bb" {
		t.Fatalf("wakes = %v, want the hash alone (WakeStored builds the bare magnet)", e.wakes)
	}
}

func TestStatsStartAtZeroWithEveryOutcome(t *testing.T) {
	e := newEnv(t, nil)
	st := e.w.Stats()
	if len(st.Fills) != len(Outcomes) {
		t.Fatalf("fills has %d series, want %d", len(st.Fills), len(Outcomes))
	}
	for _, o := range Outcomes {
		if v, ok := st.Fills[o]; !ok || v != 0 {
			t.Fatalf("outcome %s = %d, present %v; want 0 and present", o, v, ok)
		}
	}
	for _, r := range []string{"lost", "write_failed"} {
		if v, ok := st.CacheFailures[r]; !ok || v != 0 {
			t.Fatalf("cache failure %s = %d, present %v", r, v, ok)
		}
	}
	if st.QueueFiles != 0 || st.Paused || st.HeadBytes != 0 || st.TailBytes != 0 {
		t.Fatalf("stats = %+v", st)
	}
	var nilW *Warmer
	nilW.Add("aa", []File{f(1)})
	nilW.Cancel("aa")
	if got := nilW.Stats(); got.Fills == nil || len(got.Fills) != len(Outcomes) {
		t.Fatalf("nil warmer stats = %+v, want zeroed series", got)
	}
}

func TestBytesAndOneJSONLogLinePerAttempt(t *testing.T) {
	e := newEnv(t, func(d *Deps) { d.Workers = 1 })
	e.set(func() {
		e.fill = func(call int, _ warmup.FillRequest) (warmup.FillResult, error) {
			if call == 1 {
				return warmup.FillResult{HeadBytes: 100, TailBytes: 20, DeliveredPieces: 7, Evicted: 2}, nil
			}
			return warmup.FillResult{HeadBytes: 3}, warmup.ErrTimeout
		}
	})
	e.w.Add("AABB", []File{f(1), f(2)})
	e.until("both", func() bool { return e.nRecs() == 2 })
	st := e.w.Stats()
	if st.HeadBytes != 103 || st.TailBytes != 20 {
		t.Fatalf("bytes = %d head, %d tail", st.HeadBytes, st.TailBytes)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.logs) != 2 {
		t.Fatalf("log lines = %q, want one per attempt", e.logs)
	}
	for _, l := range e.logs {
		if !strings.HasPrefix(l, "[Warm] ") || strings.Contains(l, "path") {
			t.Fatalf("log line %q: want the [Warm] prefix and no path", l)
		}
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(strings.TrimPrefix(e.logs[0], "[Warm] ")), &m); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"hash": "aabb", "file": 1.0, "trigger": "add", "outcome": "warmed", "cache": "cached",
		"head_bytes": 100.0, "tail_bytes": 20.0, "delivered_pieces": 7.0, "evicted": 2.0}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("log field %s = %v, want %v", k, m[k], v)
		}
	}
	for _, k := range []string{"reason", "delivered_at", "duration_ms"} {
		if _, ok := m[k]; !ok {
			t.Errorf("log field %s missing: %v", k, m)
		}
	}
	if !strings.Contains(e.logs[1], `"reason":"timeout"`) || !strings.Contains(e.logs[1], `"outcome":"failed"`) {
		t.Errorf("failed line = %s", e.logs[1])
	}
}

func TestShutdownEndsTheWorkers(t *testing.T) {
	e := newEnv(t, nil)
	e.set(func() { e.enabled = true })
	close(e.stop)
	time.Sleep(20 * time.Millisecond)
	e.w.Add("aa", []File{f(1)})
	e.quiet(0, 0)
	e.stop = make(chan struct{}) // the cleanup closes a fresh one
}

func TestCancelWaitsForARecordInFlight(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	e := newEnv(t, func(d *Deps) {
		d.Workers = 1
		rec := d.Record
		d.Record = func(u Update) { close(entered); <-release; rec(u) }
	})
	e.w.Add("aa", []File{f(1)})
	<-entered
	done := make(chan struct{})
	go func() { e.w.Cancel("aa"); close(done) }()
	select {
	case <-done:
		t.Fatal("Cancel returned while a record was being written")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	<-done
	if e.nRecs() != 1 {
		t.Fatalf("records = %d, want the one in flight", e.nRecs())
	}
}

func TestSwitchingOffDropsTheQueueAndStopsTheFill(t *testing.T) {
	e := newEnv(t, func(d *Deps) { d.Workers = 1 })
	e.set(func() {
		e.fill = func(_ int, req warmup.FillRequest) (warmup.FillResult, error) { return stoppable(req) }
	})
	e.w.Add("aa", []File{f(1), f(2)})
	e.w.Add("bb", []File{f(1)})
	e.until("the first fill", func() bool { return e.nFills() == 1 })
	e.set(func() { e.enabled = false })
	e.until("the queue to drain", func() bool { return e.w.Stats().QueueFiles == 0 })
	e.quiet(1, 0)
	if got := e.fills(OutcomeSkipped); got != 0 {
		t.Fatalf("skipped = %d: switching off drops the queue, it does not skip files", got)
	}
}

func TestAPanicInAFillSkipsTheJobAndKeepsTheWorkerAlive(t *testing.T) {
	e := newEnv(t, func(d *Deps) { d.Workers = 1 })
	e.set(func() {
		e.fill = func(call int, _ warmup.FillRequest) (warmup.FillResult, error) {
			if call == 1 {
				panic("engine bug")
			}
			return warmup.FillResult{HeadBytes: 1}, nil
		}
	})
	e.w.Add("aa", []File{f(1), f(2)})
	e.w.Add("bb", []File{f(1)})
	e.until("the next job", func() bool { return e.nRecs() == 1 })
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.logs) == 0 || !strings.Contains(e.logs[0], "recovered") {
		t.Fatalf("logs = %q, want the recovery", e.logs)
	}
}

func TestFilesSkippedAfterAWakeErrorAreCounted(t *testing.T) {
	e := newEnv(t, nil)
	e.set(func() { e.live = false; e.wakeErr = errors.New("busy") })
	e.w.Add("aa", []File{f(1), f(2), f(3)})
	e.until("the skip", func() bool { return e.fills(OutcomeSkipped) == 2 })
	if e.fills(OutcomeWakeError) != 1 {
		t.Fatalf("wake_error = %d, want 1", e.fills(OutcomeWakeError))
	}
}

func TestProtectIgnoresTheCaseOfTheHash(t *testing.T) {
	var got bool
	e := newEnv(t, func(d *Deps) { d.Workers = 1 })
	e.set(func() {
		e.fill = func(_ int, req warmup.FillRequest) (warmup.FillResult, error) {
			got = req.Protect(warmup.FileKey{Hash: "AA", FileIdx: 1})
			return warmup.FillResult{HeadBytes: 1}, nil
		}
	})
	e.w.Add("aa", []File{f(1)})
	e.until("the fill", func() bool { return e.nRecs() == 1 })
	if !got {
		t.Fatal("an upper-case hash is not protected")
	}
}

// ---- the queued record (roadmap step 8) ----

func (e *env) queuedKeys() []warmup.FileKey {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]warmup.FileKey(nil), e.queued...)
}

func TestAddMarksEveryValidFileQueuedBeforeItEnqueues(t *testing.T) {
	e := newEnv(t, nil)
	gate := make(chan struct{})
	e.set(func() {
		e.fill = func(int, warmup.FillRequest) (warmup.FillResult, error) {
			<-gate
			return warmup.FillResult{HeadBytes: 1}, nil
		}
	})
	t.Cleanup(func() { close(gate) })
	e.w.Add("AA", []File{f(1), {Index: 2, Size: 0}, {Index: -1, Size: 5}, f(3)})
	got := e.queuedKeys()
	if len(got) != 2 || got[0] != (warmup.FileKey{Hash: "aa", FileIdx: 1}) || got[1] != (warmup.FileKey{Hash: "aa", FileIdx: 3}) {
		t.Fatalf("queued = %+v, want aa/1 and aa/3", got)
	}
}

func TestAddWithTheSwitchOffMarksNothingQueued(t *testing.T) {
	e := newEnv(t, nil)
	e.set(func() { e.enabled = false })
	e.w.Add("aa", []File{f(1)})
	if got := e.queuedKeys(); len(got) != 0 {
		t.Fatalf("queued = %+v with the switch off", got)
	}
}

// A file the full queue drops is still marked: nothing records it, and the mark is what
// lets catch-up retry it.
func TestAFileDroppedForAFullQueueStaysQueued(t *testing.T) {
	e := newEnv(t, func(d *Deps) { d.MaxQueued = 1; d.Workers = 1 })
	gate := make(chan struct{})
	e.set(func() {
		e.fill = func(int, warmup.FillRequest) (warmup.FillResult, error) {
			<-gate
			return warmup.FillResult{HeadBytes: 1}, nil
		}
	})
	t.Cleanup(func() { close(gate) })
	e.w.Add("aa", []File{f(1)})
	e.until("the first fill", func() bool { return e.nFills() == 1 })
	e.w.Add("bb", []File{f(1)}) // takes the one slot
	e.w.Add("cc", []File{f(1)}) // dropped: the queue is full
	if e.fills(OutcomeDroppedQueueFull) != 1 {
		t.Fatalf("dropped = %d", e.fills(OutcomeDroppedQueueFull))
	}
	var cc bool
	for _, k := range e.queuedKeys() {
		cc = cc || k.Hash == "cc"
	}
	if !cc {
		t.Fatalf("queued = %+v: the dropped file must be marked", e.queuedKeys())
	}
	e.quiet(1, 0) // and nothing was recorded for it
}

func TestEveryRecordedOutcomeClearsTheQueuedMark(t *testing.T) {
	cases := []struct {
		name  string
		fill  func(int, warmup.FillRequest) (warmup.FillResult, error)
		tweak func(*env)
	}{
		{"warmed", func(int, warmup.FillRequest) (warmup.FillResult, error) { return warmup.FillResult{HeadBytes: 1}, nil }, nil},
		{"already warm", func(int, warmup.FillRequest) (warmup.FillResult, error) {
			return warmup.FillResult{AlreadyWarm: true}, nil
		}, nil},
		{"failed", func(int, warmup.FillRequest) (warmup.FillResult, error) {
			return warmup.FillResult{}, warmup.ErrTimeout
		}, nil},
		{"dropped for quota", func(int, warmup.FillRequest) (warmup.FillResult, error) { return warmup.FillResult{}, warmup.ErrNoRoom }, nil},
		{"lost", func(int, warmup.FillRequest) (warmup.FillResult, error) {
			return warmup.FillResult{}, warmup.ErrEvicted
		}, nil},
		{"write failed", func(int, warmup.FillRequest) (warmup.FillResult, error) {
			return warmup.FillResult{}, errors.New("disk")
		}, nil},
		{"evicted", func(int, warmup.FillRequest) (warmup.FillResult, error) {
			return warmup.FillResult{}, warmup.ErrEvicted
		},
			func(e *env) { e.evicted = func(warmup.FileKey, time.Time) bool { return true } }},
	}
	for _, c := range cases {
		e := newEnv(t, nil)
		e.set(func() { e.fill = c.fill })
		if c.tweak != nil {
			e.set(func() { c.tweak(e) })
		}
		e.w.Add("aa", []File{f(1)})
		e.until(c.name+" record", func() bool { return e.nRecs() >= 1 })
		if u := e.rec(0); !u.ClearQueued {
			t.Errorf("%s: record %+v does not clear the queued mark", c.name, u)
		}
	}
}

// A wake error records nothing, so the queued mark stays: that is how catch-up finds it.
func TestAWakeErrorRecordsNothingAndLeavesTheMark(t *testing.T) {
	e := newEnv(t, nil)
	e.set(func() { e.live = false; e.wakeErr = errors.New("busy") })
	e.w.Add("aa", []File{f(1)})
	e.until("the wake error", func() bool { return e.fills(OutcomeWakeError) == 1 })
	e.quiet(0, 0)
	if len(e.queuedKeys()) != 1 {
		t.Fatalf("queued = %+v", e.queuedKeys())
	}
}
