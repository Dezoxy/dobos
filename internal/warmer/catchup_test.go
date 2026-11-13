package warmer

import (
	"sync"
	"testing"
	"time"

	"flodni/internal/warmup"
)

// The catch-up feeder (Readiness step 3) against a fake target and short intervals.

type fedCall struct {
	hash  string
	files []File
	at    time.Time
}

type fakeTarget struct {
	mu       sync.Mutex
	busy     bool
	noRoom   bool
	failures int64
	refused  map[string]bool
	fed      []fedCall
	resets   int
	onFeed   func(t *fakeTarget, hash string) // runs under mu
}

func (t *fakeTarget) AddCatchup(hash string, files []File) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.fed = append(t.fed, fedCall{hash, files, time.Now()})
	t.busy = true
	if t.onFeed != nil {
		t.onFeed(t, hash)
	}
	return len(files)
}
func (t *fakeTarget) Idle() bool              { t.mu.Lock(); defer t.mu.Unlock(); return !t.busy }
func (t *fakeTarget) NoRoom() bool            { t.mu.Lock(); defer t.mu.Unlock(); return t.noRoom }
func (t *fakeTarget) ResetNoRoom()            { t.mu.Lock(); t.resets++; t.noRoom = false; t.mu.Unlock() }
func (t *fakeTarget) NotStored(h string) bool { t.mu.Lock(); defer t.mu.Unlock(); return t.refused[h] }
func (t *fakeTarget) CatchupCacheFailures() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.failures
}
func (t *fakeTarget) set(f func()) {
	t.mu.Lock()
	f()
	t.mu.Unlock()
}
func (t *fakeTarget) hashes() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []string
	for _, c := range t.fed {
		out = append(out, c.hash)
	}
	return out
}

// done finishes whatever the fake was fed: the next feed may come.
func (t *fakeTarget) done() { t.set(func() { t.busy = false }) }

// autoDone makes every feed complete at once.
func (t *fakeTarget) autoDone(more func(t *fakeTarget, hash string)) {
	t.onFeed = func(t *fakeTarget, hash string) {
		if more != nil {
			more(t, hash)
		}
		t.busy = false
	}
}

const perHourFor1ms = 3600 * 1000 // time.Hour / this = 1 ms

func fl(n int) []File {
	var out []File
	for i := 1; i <= n; i++ {
		out = append(out, File{Index: i, Size: 1 << 30})
	}
	return out
}

func newFeeder(t *fakeTarget, perHour int) (*Feeder, chan struct{}) {
	stop := make(chan struct{})
	return &Feeder{Target: t, PerHour: func() int { return perHour }, Stop: stop, Poll: time.Millisecond}, stop
}

func TestFeederFeedsOneHashOnlyWhenTheWarmerIsIdle(t *testing.T) {
	ft := &fakeTarget{}
	fd, _ := newFeeder(ft, perHourFor1ms)
	cands := []Candidate{{"aa", fl(2)}, {"bb", fl(1)}, {"cc", fl(1)}}
	resc := make(chan PassResult, 1)
	go func() { resc <- fd.Pass(cands) }()
	waitFor(t, "the first feed", func() bool { return len(ft.hashes()) == 1 })
	time.Sleep(40 * time.Millisecond) // busy (queued, running or paused): nothing more is fed
	if got := ft.hashes(); len(got) != 1 {
		t.Fatalf("fed %v while the warmer was busy", got)
	}
	ft.done()
	waitFor(t, "the second feed", func() bool { return len(ft.hashes()) == 2 })
	time.Sleep(20 * time.Millisecond)
	if got := ft.hashes(); len(got) != 2 {
		t.Fatalf("fed %v, want the second hash only until it is done", got)
	}
	ft.done()
	waitFor(t, "the third feed", func() bool { return len(ft.hashes()) == 3 })
	ft.done()
	res := <-resc
	if got := ft.hashes(); got[0] != "aa" || got[1] != "bb" || got[2] != "cc" {
		t.Fatalf("order = %v, want the candidates' order", got)
	}
	if res.Fed != 4 || res.Hashes != 3 || len(res.Left) != 0 {
		t.Fatalf("result = %+v", res)
	}
}

func TestFeederWaitsKTimesTheIntervalAfterKFiles(t *testing.T) {
	ft := &fakeTarget{}
	ft.autoDone(nil)
	const interval = 20 * time.Millisecond
	fd, _ := newFeeder(ft, int(time.Hour/interval))
	res := fd.Pass([]Candidate{{"aa", fl(3)}, {"bb", fl(1)}, {"cc", fl(2)}})
	if res.Fed != 6 {
		t.Fatalf("result = %+v", res)
	}
	ft.mu.Lock()
	defer ft.mu.Unlock()
	if gap := ft.fed[1].at.Sub(ft.fed[0].at); gap < 3*interval {
		t.Errorf("after 3 files the next hash came after %v, want at least %v", gap, 3*interval)
	}
	if gap := ft.fed[2].at.Sub(ft.fed[1].at); gap < interval {
		t.Errorf("after 1 file the next hash came after %v, want at least %v", gap, interval)
	}
}

func TestFeederReadsTheRateOnEachFeedAndZeroStops(t *testing.T) {
	ft := &fakeTarget{}
	ft.autoDone(nil)
	stop := make(chan struct{})
	var mu sync.Mutex
	reads := 0
	fd := &Feeder{Target: ft, Stop: stop, Poll: time.Millisecond, PerHour: func() int {
		mu.Lock()
		defer mu.Unlock()
		reads++
		if reads > 2 {
			return 0
		}
		return perHourFor1ms
	}}
	res := fd.Pass([]Candidate{{"aa", fl(1)}, {"bb", fl(2)}, {"cc", fl(3)}, {"dd", fl(4)}})
	if got := ft.hashes(); len(got) != 2 {
		t.Fatalf("fed %v, want two feeds before the rate went to 0", got)
	}
	if res.Left[LeftRateOff] != 7 || res.Fed != 3 {
		t.Fatalf("result = %+v, want 7 files left for rate 0", res)
	}
}

func TestFeederRechecksEachCandidateJustBeforeFeeding(t *testing.T) {
	ft := &fakeTarget{}
	ft.autoDone(nil)
	fd, _ := newFeeder(ft, perHourFor1ms)
	fd.Still = func(hash string, f File) bool { return !(hash == "bb" || (hash == "aa" && f.Index == 2)) }
	res := fd.Pass([]Candidate{{"aa", fl(3)}, {"bb", fl(2)}, {"cc", fl(1)}})
	ft.mu.Lock()
	defer ft.mu.Unlock()
	if len(ft.fed) != 2 || ft.fed[0].hash != "aa" || len(ft.fed[0].files) != 2 ||
		ft.fed[0].files[0].Index != 1 || ft.fed[0].files[1].Index != 3 || ft.fed[1].hash != "cc" {
		t.Fatalf("fed = %+v, want aa without file 2, then cc", ft.fed)
	}
	if res.Fed != 3 || res.Left[LeftNoLonger] != 3 {
		t.Fatalf("result = %+v, want 3 fed and 3 no longer candidates", res)
	}
}

func TestFeederEndsThePassOnTheNoRoomFlagAndReturnsWhatItLeft(t *testing.T) {
	ft := &fakeTarget{noRoom: true} // a stale flag from before the pass
	ft.autoDone(func(t *fakeTarget, hash string) {
		if hash == "bb" {
			t.noRoom = true
		}
	})
	fd, _ := newFeeder(ft, perHourFor1ms)
	res := fd.Pass([]Candidate{{"aa", fl(1)}, {"bb", fl(2)}, {"cc", fl(3)}, {"dd", fl(4)}})
	if got := ft.hashes(); len(got) != 2 {
		t.Fatalf("fed %v, want aa and bb, then the end of the pass", got)
	}
	if res.Left[LeftNoRoom] != 7 || res.Fed != 3 {
		t.Fatalf("result = %+v, want 7 files left for room", res)
	}
	if ft.resets == 0 {
		t.Fatal("the pass did not lower a stale no-room flag")
	}
}

func TestFeederEndsThePassAfterThreeCacheSideFailuresOfItsOwn(t *testing.T) {
	ft := &fakeTarget{failures: 9} // earlier passes' failures do not count
	ft.autoDone(func(t *fakeTarget, hash string) { t.failures++ })
	fd, _ := newFeeder(ft, perHourFor1ms)
	res := fd.Pass([]Candidate{{"a", fl(1)}, {"b", fl(1)}, {"c", fl(1)}, {"d", fl(2)}, {"e", fl(1)}})
	if got := ft.hashes(); len(got) != 3 {
		t.Fatalf("fed %v, want three feeds, one failure each", got)
	}
	if res.Left[LeftCacheFailures] != 3 || res.Fed != 3 {
		t.Fatalf("result = %+v", res)
	}
}

func TestFeederStopsWhereverItIsWaiting(t *testing.T) {
	for _, name := range []string{"busy warmer", "interval"} {
		ft := &fakeTarget{}
		perHour := perHourFor1ms
		if name == "interval" {
			ft.autoDone(nil)
			perHour = 1 // one file an hour: the wait is an hour
		}
		fd, stop := newFeeder(ft, perHour)
		resc := make(chan PassResult, 1)
		go func() { resc <- fd.Pass([]Candidate{{"aa", fl(1)}, {"bb", fl(2)}, {"cc", fl(3)}}) }()
		waitFor(t, "the first feed", func() bool { return len(ft.hashes()) == 1 })
		close(stop)
		select {
		case res := <-resc:
			if res.Left[LeftStopped] != 5 || len(ft.hashes()) != 1 {
				t.Fatalf("%s: result = %+v, fed %v", name, res, ft.hashes())
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: the feeder did not stop", name)
		}
	}
}

func TestFeederSeparatesAHashTheEngineRefusedFromBusy(t *testing.T) {
	ft := &fakeTarget{refused: map[string]bool{"aa": true}} // refused in an earlier pass
	ft.autoDone(func(t *fakeTarget, hash string) {
		if hash == "bb" {
			t.refused["bb"] = true // refused by the wake of this very feed
		}
	})
	fd, _ := newFeeder(ft, perHourFor1ms)
	res := fd.Pass([]Candidate{{"aa", fl(2)}, {"bb", fl(3)}, {"cc", fl(1)}})
	if got := ft.hashes(); len(got) != 2 || got[0] != "bb" || got[1] != "cc" {
		t.Fatalf("fed %v, want aa skipped without a feed", got)
	}
	if res.Left[LeftNotStored] != 5 || res.Fed != 4 {
		t.Fatalf("result = %+v, want 5 files not stored (2 skipped, 3 refused after the feed)", res)
	}
}

func TestFeederEndsOnlyAfterTheLastFeedIsDone(t *testing.T) {
	// The no-room flag of the last hash is raised while it runs: the pass must wait for
	// it, or the result would miss the end of the pass.
	ft := &fakeTarget{}
	fd, _ := newFeeder(ft, perHourFor1ms)
	resc := make(chan PassResult, 1)
	go func() { resc <- fd.Pass([]Candidate{{"aa", fl(1)}}) }()
	waitFor(t, "the feed", func() bool { return len(ft.hashes()) == 1 })
	select {
	case <-resc:
		t.Fatal("the pass ended while its last hash was still running")
	case <-time.After(30 * time.Millisecond):
	}
	ft.set(func() { ft.noRoom = true; ft.busy = false })
	res := <-resc
	if res.Fed != 1 || !res.NoRoom {
		t.Fatalf("result = %+v, want the no-room flag reported", res)
	}
}

// The feeder drives the real warmer end to end through its Target methods.
func TestFeederDrivesTheWarmer(t *testing.T) {
	var _ Target = (*Warmer)(nil)
	e := newEnv(t, func(d *Deps) { d.Workers = 1 })
	e.catchupFill(roomAvailable, func(warmup.FillRequest) (warmup.FillResult, error) {
		return warmup.FillResult{HeadBytes: 3}, nil
	})
	fd := &Feeder{Target: e.w, PerHour: func() int { return perHourFor1ms }, Stop: e.stop, Poll: time.Millisecond}
	res := fd.Pass([]Candidate{{"aa", fl(2)}, {"bb", fl(1)}})
	if res.Fed != 3 || len(res.Left) != 0 || e.nRecs() != 3 {
		t.Fatalf("result = %+v, records = %d", res, e.nRecs())
	}
	if c := e.w.Stats().ByTrigger[TriggerCatchup]; c.Fills[OutcomeWarmed] != 3 {
		t.Fatalf("stats = %+v", c)
	}
}

func TestCatchupCacheFailuresCountsOnlyCatchupFiles(t *testing.T) {
	e := newEnv(t, func(d *Deps) { d.Workers = 1 })
	e.set(func() {
		e.fill = func(_ int, req warmup.FillRequest) (warmup.FillResult, error) {
			if req.RoomOnly {
				return warmup.FillResult{}, nil
			}
			return warmup.FillResult{}, errDisk
		}
	})
	e.w.Add("aa", []File{f(1)})
	e.until("the add's failure", func() bool { return e.fills(OutcomeWriteFailed) == 1 })
	if got := e.w.CatchupCacheFailures(); got != 0 {
		t.Fatalf("failures = %d: an add file's failure is not catch-up's", got)
	}
	e.w.AddCatchup("bb", []File{f(1)})
	e.until("the catch-up failure", func() bool { return e.fills(OutcomeWriteFailed) == 2 })
	if got := e.w.CatchupCacheFailures(); got != 1 {
		t.Fatalf("failures = %d, want 1", got)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}
