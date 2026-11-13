package warmer

import (
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"flodni/internal/warmup"
)

// Catch-up (Readiness step 3), PR A: the per-file trigger and the pre-flight. Nothing
// calls AddCatchup yet; these tests drive it directly.

// catchupFill answers the RoomOnly pre-flight with pre and every other call with real.
func (e *env) catchupFill(pre func() (warmup.FillResult, error), real func(warmup.FillRequest) (warmup.FillResult, error)) {
	e.set(func() {
		e.fill = func(_ int, req warmup.FillRequest) (warmup.FillResult, error) {
			if req.RoomOnly {
				return pre()
			}
			return real(req)
		}
	})
}

func roomAvailable() (warmup.FillResult, error) { return warmup.FillResult{}, nil }

func TestACatchupFileIsPreflightedBeforeTheWakeThenFilledUnderEvictNone(t *testing.T) {
	policyCalls := 0
	e := newEnv(t, func(d *Deps) {
		d.Workers = 1
		pol := d.Policy
		d.Policy = func() warmup.EvictPolicy { policyCalls++; return pol() }
	})
	var fillsAtWake int
	e.set(func() {
		e.live = false
		e.policy = warmup.EvictNeverPlayed // the add policy would evict; catch-up must not
		e.wakeHook = func(string) { fillsAtWake = len(e.reqs) }
	})
	e.catchupFill(roomAvailable, func(warmup.FillRequest) (warmup.FillResult, error) {
		return warmup.FillResult{HeadBytes: 10, TailBytes: 5, DeliveredPieces: 2}, nil
	})
	if n := e.w.AddCatchup("aa", []File{f(1)}); n != 1 {
		t.Fatalf("AddCatchup queued %d, want 1", n)
	}
	e.until("the record", func() bool { return e.nRecs() == 1 })
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.reqs) != 2 || !e.reqs[0].RoomOnly || e.reqs[1].RoomOnly {
		t.Fatalf("requests = %+v, want a RoomOnly pre-flight then the fill", e.reqs)
	}
	if fillsAtWake != 1 {
		t.Fatalf("the wake came after %d fills, want 1: the pre-flight comes before it", fillsAtWake)
	}
	for _, r := range e.reqs {
		if r.Policy != warmup.EvictNone || r.Trigger != TriggerCatchup {
			t.Fatalf("request = %+v, want EvictNone and the catch-up trigger", r)
		}
	}
	if policyCalls != 0 {
		t.Fatalf("Policy() was read %d times for a catch-up file", policyCalls)
	}
	if len(e.wakes) != 1 || e.wakes[0] != "aa" {
		t.Fatalf("wakes = %v", e.wakes)
	}
	if u := e.recs[0]; u.Cache != CacheCached || u.Readability != ReadWarmed {
		t.Fatalf("record = %+v", u)
	}
	st := e.w.Stats()
	c, a := st.ByTrigger[TriggerCatchup], st.ByTrigger[Trigger]
	if c.Fills[OutcomeWarmed] != 1 || c.HeadBytes != 10 || c.TailBytes != 5 {
		t.Fatalf("catch-up stats = %+v", c)
	}
	if a.HeadBytes != 0 || a.TailBytes != 0 || a.Fills[OutcomeWarmed] != 0 {
		t.Fatalf("add stats = %+v, want zero", a)
	}
	if st.Fills[OutcomeWarmed] != 1 || st.HeadBytes != 10 {
		t.Fatalf("totals = %+v", st)
	}
}

func TestACatchupFileThatIsAlreadyWarmRecordsCachedAndWakesNothing(t *testing.T) {
	e := newEnv(t, func(d *Deps) { d.Workers = 1 })
	e.set(func() { e.live = false })
	e.catchupFill(func() (warmup.FillResult, error) { return warmup.FillResult{AlreadyWarm: true}, nil },
		func(warmup.FillRequest) (warmup.FillResult, error) {
			t.Error("a warm file was filled")
			return warmup.FillResult{}, nil
		})
	e.w.AddCatchup("aa", []File{f(1), f(2)})
	e.until("both records", func() bool { return e.nRecs() == 2 })
	e.quiet(2, 2)
	if e.nWakes() != 0 {
		t.Fatalf("an already warm file woke the torrent %d times", e.nWakes())
	}
	for i := 0; i < 2; i++ {
		if u := e.rec(i); u.Cache != CacheCached || u.Readability != "" || u.DeliveredAt != (time.Time{}) {
			t.Fatalf("record %d = %+v, want cache=cached and readability unchanged", i, u)
		}
	}
	if c := e.w.Stats().ByTrigger[TriggerCatchup]; c.Fills[OutcomeAlreadyWarm] != 2 {
		t.Fatalf("catch-up stats = %+v", c)
	}
}

// A catch-up file at a full quota wakes nothing, records nothing (above all not
// dropped_no_evidence), raises the flag and leaves the rest of its job.
func TestACatchupFileAtAFullQuotaWakesNothingAndRecordsNothing(t *testing.T) {
	e := newEnv(t, func(d *Deps) { d.Workers = 1 })
	e.set(func() { e.live = false; e.policy = warmup.EvictNeverPlayed })
	e.catchupFill(func() (warmup.FillResult, error) { return warmup.FillResult{}, warmup.ErrNoRoom },
		func(warmup.FillRequest) (warmup.FillResult, error) {
			t.Error("a file without room was filled")
			return warmup.FillResult{}, nil
		})
	if e.w.NoRoom() {
		t.Fatal("the flag is raised before any attempt")
	}
	e.w.AddCatchup("aa", []File{f(1), f(2), f(3)})
	e.until("the flag", e.w.NoRoom)
	e.until("the drain", e.w.Idle)
	e.quiet(1, 0) // one pre-flight, no fill, no record
	if e.nWakes() != 0 {
		t.Fatalf("a full quota woke the torrent %d times", e.nWakes())
	}
	st := e.w.Stats()
	c := st.ByTrigger[TriggerCatchup]
	if c.Fills[OutcomeNoRoom] != 1 || c.Fills[OutcomeSkipped] != 2 || st.ByTrigger[Trigger].Fills[OutcomeNoRoom] != 0 {
		t.Fatalf("stats = %+v", st.ByTrigger)
	}
	e.w.ResetNoRoom()
	if e.w.NoRoom() {
		t.Fatal("ResetNoRoom left the flag up")
	}
}

func TestAPausedCatchupFileResumesUnderEvictNoneWithItsTrigger(t *testing.T) {
	e := newEnv(t, func(d *Deps) { d.Workers = 1 })
	open := false
	var realCalls atomic.Int32
	e.set(func() {
		e.policy = warmup.EvictNeverPlayed
		e.openFn = func() bool { e.mu.Lock(); defer e.mu.Unlock(); return open }
	})
	e.catchupFill(roomAvailable, func(req warmup.FillRequest) (warmup.FillResult, error) {
		if realCalls.Add(1) == 1 {
			return stoppable(req)
		}
		return warmup.FillResult{HeadBytes: 6}, nil
	})
	e.w.AddCatchup("aa", []File{f(1)})
	e.until("the first real fill", func() bool { return realCalls.Load() >= 1 })
	e.set(func() { open = true })
	e.until("the pause", func() bool { return e.w.Stats().Paused })
	e.set(func() { open = false })
	e.until("the resumed fill", func() bool { return e.nRecs() == 1 })
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.reqs) != 4 { // pre-flight, stopped fill, pre-flight, fill
		t.Fatalf("requests = %d, want 4", len(e.reqs))
	}
	for i, r := range e.reqs {
		if r.Policy != warmup.EvictNone || r.Trigger != TriggerCatchup || r.RoomOnly != (i%2 == 0) {
			t.Fatalf("request %d = %+v: the trigger and EvictNone must survive the requeue", i, r)
		}
	}
}

func TestACatchupRetryKeepsItsTriggerAndEvictNone(t *testing.T) {
	e := newEnv(t, func(d *Deps) { d.Workers = 1; d.RetryDelays = []time.Duration{time.Millisecond} })
	e.set(func() { e.policy = warmup.EvictNeverPlayed })
	e.catchupFill(roomAvailable, func(warmup.FillRequest) (warmup.FillResult, error) {
		return warmup.FillResult{}, errDisk
	})
	e.w.AddCatchup("aa", []File{f(1)})
	e.until("the retry's fill", func() bool { return e.nRecs() == 2 })
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.reqs) != 4 {
		t.Fatalf("requests = %d, want pre-flight and fill twice", len(e.reqs))
	}
	for i, r := range e.reqs {
		if r.Policy != warmup.EvictNone || r.Trigger != TriggerCatchup {
			t.Fatalf("request %d = %+v", i, r)
		}
	}
	if c := e.w.Stats().ByTrigger[TriggerCatchup]; c.Fills[OutcomeWriteFailed] != 2 {
		t.Fatalf("catch-up stats = %+v", c)
	}
}

func TestAnAddUpgradesAFileQueuedByCatchupToTheAddTrigger(t *testing.T) {
	e := newEnv(t, func(d *Deps) { d.Workers = 1 })
	open := true // a pause keeps the file queued
	e.set(func() {
		e.policy = warmup.EvictNeverPlayed
		e.openFn = func() bool { e.mu.Lock(); defer e.mu.Unlock(); return open }
	})
	e.w.AddCatchup("aa", []File{f(1), f(2)})
	e.w.Add("aa", []File{f(1)})
	if got := e.w.Stats().QueueFiles; got != 2 {
		t.Fatalf("queue = %d, want 2: the add must upgrade the queued file, not queue it twice", got)
	}
	e.set(func() { open = false })
	e.until("both fills", func() bool { return e.nRecs() == 2 })
	e.mu.Lock()
	defer e.mu.Unlock()
	var adds, catchups int
	for _, r := range e.reqs {
		switch {
		case r.RoomOnly:
		case r.Trigger == warmup.TriggerWarmAdd && r.Policy == warmup.EvictNeverPlayed:
			adds++
		case r.Trigger == TriggerCatchup && r.Policy == warmup.EvictNone:
			catchups++
		default:
			t.Fatalf("request = %+v", r)
		}
	}
	if adds != 1 || catchups != 1 {
		t.Fatalf("adds = %d, catch-ups = %d; want 1 and 1 (file 1 upgraded, file 2 still catch-up)", adds, catchups)
	}
}

func TestACatchupAddDoesNotDowngradeAQueuedAdd(t *testing.T) {
	e := newEnv(t, func(d *Deps) { d.Workers = 1 })
	open := true
	e.set(func() { e.openFn = func() bool { e.mu.Lock(); defer e.mu.Unlock(); return open } })
	e.w.Add("aa", []File{f(1)})
	e.w.AddCatchup("aa", []File{f(1)})
	e.set(func() { open = false })
	e.until("the fill", func() bool { return e.nRecs() == 1 })
	e.quiet(1, 1)
	if r := e.reqs[0]; r.RoomOnly || r.Trigger != warmup.TriggerWarmAdd {
		t.Fatalf("request = %+v, want the add fill", r)
	}
}

// Hard rule 3, the catch-up half of TestAnUnstoredHashIsNeverWoken: the refusal is
// remembered so the feeder can tell it from a busy warmer, and the job's other files
// are left, with no fill and no record.
func TestAnUnstoredHashIsNeverWokenByCatchup(t *testing.T) {
	e := newEnv(t, func(d *Deps) { d.Workers = 1 })
	e.set(func() { e.live = false; e.wakeErr = fmt.Errorf("%w: no record", ErrNotStored) })
	e.catchupFill(roomAvailable, func(warmup.FillRequest) (warmup.FillResult, error) {
		t.Error("an unstored hash was filled")
		return warmup.FillResult{}, nil
	})
	if e.w.NotStored("aa") {
		t.Fatal("NotStored before any wake")
	}
	e.w.AddCatchup("aa", []File{f(1), f(2), f(3)})
	e.until("the wake error", func() bool { return e.fills(OutcomeWakeError) == 1 })
	e.until("the drain", e.w.Idle)
	e.quiet(1, 0) // the pre-flight only
	if e.nWakes() != 1 {
		t.Fatalf("wakes = %d, want one for the job", e.nWakes())
	}
	if !e.w.NotStored("aa") || e.w.NotStored("bb") {
		t.Fatal("NotStored must name the refused hash and no other")
	}
	if c := e.w.Stats().ByTrigger[TriggerCatchup]; c.Fills[OutcomeWakeError] != 1 || c.Fills[OutcomeSkipped] != 2 {
		t.Fatalf("catch-up stats = %+v", c)
	}
	// A plain error is busy, not refused.
	e.set(func() { e.wakeErr = errors.New("too many pending wakes") })
	e.w.AddCatchup("cc", []File{f(1)})
	e.until("the second wake error", func() bool { return e.fills(OutcomeWakeError) == 2 })
	if e.w.NotStored("cc") {
		t.Fatal("a busy wake was taken for a refusal")
	}
}

// Hard rule 3, the catch-up half of TestACancelledHashIsNotWokenAtAll.
func TestACancelledCatchupHashIsNotWokenAtAll(t *testing.T) {
	gate := make(chan struct{})
	e := newEnv(t, func(d *Deps) { d.Workers = 1 })
	e.set(func() { e.live = false })
	e.catchupFill(roomAvailable, func(warmup.FillRequest) (warmup.FillResult, error) {
		<-gate
		return warmup.FillResult{HeadBytes: 1}, nil
	})
	e.w.AddCatchup("aa", []File{f(1)})
	e.until("the first job", func() bool { return e.nFills() == 2 })
	e.w.AddCatchup("bb", []File{f(1)}) // queued behind it
	e.w.Cancel("bb")
	if n := e.w.AddCatchup("bb", []File{f(1)}); n != 0 {
		t.Fatalf("AddCatchup of a removed hash queued %d: only Add clears a tombstone", n)
	}
	close(gate)
	e.until("aa", func() bool { return e.nRecs() == 1 })
	e.quiet(2, 1)
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, w := range e.wakes {
		if w == "bb" {
			t.Fatal("a cancelled, queued catch-up hash was woken")
		}
	}
}

func TestACatchupAddIsIgnoredWhileTheSwitchIsOff(t *testing.T) {
	e := newEnv(t, nil)
	e.set(func() { e.enabled = false })
	if n := e.w.AddCatchup("aa", []File{f(1)}); n != 0 {
		t.Fatalf("queued %d", n)
	}
	e.quiet(0, 0)
	var nilW *Warmer
	if nilW.AddCatchup("aa", []File{f(1)}) != 0 || nilW.NoRoom() || !nilW.Idle() || nilW.NotStored("aa") {
		t.Fatal("a nil Warmer must be inert")
	}
	nilW.ResetNoRoom()
}

func TestIdleMeansNothingQueuedRunningOrPaused(t *testing.T) {
	e := newEnv(t, func(d *Deps) { d.Workers = 1 })
	gate := make(chan struct{})
	e.set(func() {
		e.fill = func(int, warmup.FillRequest) (warmup.FillResult, error) { <-gate; return warmup.FillResult{}, nil }
	})
	if !e.w.Idle() {
		t.Fatal("a new warmer is not idle")
	}
	e.w.Add("aa", []File{f(1)})
	e.until("running", func() bool { return e.nFills() == 1 })
	if e.w.Idle() {
		t.Fatal("idle while a job runs")
	}
	close(gate)
	e.until("idle", e.w.Idle)
}

// A requeue puts the catch-up files of a paused job in front of an add queued meanwhile.
// When the quota is then found full, only the catch-up files are left.
func TestACatchupFileAtAFullQuotaLeavesTheAddFilesOfItsJobToRun(t *testing.T) {
	e := newEnv(t, func(d *Deps) { d.Workers = 1 })
	open := false
	var pre, real atomic.Int32
	e.set(func() {
		e.policy = warmup.EvictNeverPlayed
		e.openFn = func() bool { e.mu.Lock(); defer e.mu.Unlock(); return open }
	})
	e.catchupFill(func() (warmup.FillResult, error) {
		if pre.Add(1) == 1 {
			return warmup.FillResult{}, nil
		}
		return warmup.FillResult{}, warmup.ErrNoRoom
	}, func(req warmup.FillRequest) (warmup.FillResult, error) {
		if real.Add(1) == 1 {
			return stoppable(req)
		}
		return warmup.FillResult{HeadBytes: 1}, nil
	})
	e.w.AddCatchup("aa", []File{f(1), f(2)})
	e.until("the catch-up fill", func() bool { return real.Load() == 1 })
	e.w.Add("aa", []File{f(3)}) // pending behind the running job
	e.set(func() { open = true })
	e.until("the pause", func() bool { return e.w.Stats().Paused })
	e.set(func() { open = false })
	e.until("the add file's record", func() bool { return e.nRecs() == 1 })
	e.until("the drain", e.w.Idle)
	st := e.w.Stats()
	c, a := st.ByTrigger[TriggerCatchup], st.ByTrigger[Trigger]
	if c.Fills[OutcomeNoRoom] != 1 || c.Fills[OutcomeSkipped] != 1 || a.Fills[OutcomeSkipped] != 0 ||
		a.Fills[OutcomeWarmed] != 1 || !e.w.NoRoom() {
		t.Fatalf("stats = %+v", st.ByTrigger)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	last := e.reqs[len(e.reqs)-1]
	if last.RoomOnly || last.Key.FileIdx != 3 || last.Trigger != warmup.TriggerWarmAdd || last.Policy != warmup.EvictNeverPlayed {
		t.Fatalf("last request = %+v, want the add file under its own policy", last)
	}
}

func TestAnAddFileGoesAheadOfTheCatchupFilesOfAMergedJob(t *testing.T) {
	e := newEnv(t, func(d *Deps) { d.Workers = 1 })
	open := true
	e.set(func() { e.openFn = func() bool { e.mu.Lock(); defer e.mu.Unlock(); return open } })
	e.w.AddCatchup("aa", []File{f(1), f(2)})
	e.w.Add("aa", []File{f(3)})
	e.set(func() { open = false })
	e.until("three records", func() bool { return e.nRecs() == 3 })
	if e.rec(0).Key.FileIdx != 3 {
		t.Fatalf("first record is file %d, want the add file 3", e.rec(0).Key.FileIdx)
	}
}

func TestACatchupPreflightErrorWakesNothingAndRecordsAWriteFailure(t *testing.T) {
	e := newEnv(t, func(d *Deps) { d.Workers = 1; d.RetryDelays = []time.Duration{} })
	e.set(func() { e.live = false })
	e.catchupFill(func() (warmup.FillResult, error) { return warmup.FillResult{}, errDisk },
		func(warmup.FillRequest) (warmup.FillResult, error) {
			t.Error("a failed pre-flight went on to fill")
			return warmup.FillResult{}, nil
		})
	e.w.AddCatchup("aa", []File{f(1)})
	e.until("the record", func() bool { return e.nRecs() == 1 })
	e.quiet(1, 1)
	if e.nWakes() != 0 {
		t.Fatalf("wakes = %d, want none", e.nWakes())
	}
	if u := e.rec(0); u.Cache != CacheWriteFailed {
		t.Fatalf("record = %+v, want cache=write_failed", u)
	}
	if e.w.CatchupCacheFailures() != 1 {
		t.Fatalf("catch-up cache failures = %d", e.w.CatchupCacheFailures())
	}
}

func TestACatchupFileThatWasPreflightedWarmRecordsNoAttemptTime(t *testing.T) {
	e := newEnv(t, func(d *Deps) { d.Workers = 1 })
	e.catchupFill(func() (warmup.FillResult, error) { return warmup.FillResult{AlreadyWarm: true}, nil }, nil)
	e.w.AddCatchup("aa", []File{f(1)})
	e.until("the record", func() bool { return e.nRecs() == 1 })
	if u := e.rec(0); !u.AttemptAt.IsZero() {
		t.Fatalf("AttemptAt = %v for an attempt that never ran", u.AttemptAt)
	}
}

func TestIdleIsFalseWhilePlaybackIsOpen(t *testing.T) {
	e := newEnv(t, nil)
	e.set(func() { e.openFn = func() bool { return true } })
	if e.w.Idle() {
		t.Fatal("idle during a playback with an empty queue")
	}
}
