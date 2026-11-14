package daemon

import (
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"flodni/internal/metadb"
	"flodni/internal/warmup"
)

var wt0 = time.Unix(1_800_000_000, 0)

func warmIndex(t *testing.T) *fileStateIndex {
	t.Helper()
	x := newFileStateIndex(nil, 90)
	now := wt0
	x.now = func() time.Time { return now }
	return x
}

func snap(t *testing.T, x *fileStateIndex, hash string, idx int) fileHealth {
	t.Helper()
	for _, f := range x.WarmSnapshot()[hash] {
		if f.FileIndex == idx {
			return f
		}
	}
	t.Fatalf("no record for %s/%d: %+v", hash, idx, x.WarmSnapshot())
	return fileHealth{}
}

func ts(sec int64) *string { return unixOrNil(sec) }

// RecordWarm's rules: empty fields leave the stored value, the delivery time only
// moves forward, warmed resets the failure streak, failed counts it.
func TestRecordWarmTransitions(t *testing.T) {
	x := warmIndex(t)
	k := fk("AA", 1)
	d1, d2 := wt0.Add(-3*time.Hour), wt0.Add(-time.Hour)

	if !x.RecordWarm(warmUpdate{key: k, readability: "warmed", cache: "cached", deliveredAt: d1}) {
		t.Fatal("first record reported no change")
	}
	got := snap(t, x, "aa", 1)
	want := fileHealth{FileIndex: 1, Readability: "warmed", Cache: "cached", LastSwarmDeliveryAt: ts(d1.Unix())}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("after warmed:\n got %+v\nwant %+v", got, want)
	}

	// The same update again changes nothing; an older delivery never moves the time back.
	if x.RecordWarm(warmUpdate{key: k, readability: "warmed", cache: "cached", deliveredAt: d1}) {
		t.Fatal("an identical update reported a change")
	}
	if x.RecordWarm(warmUpdate{key: k, deliveredAt: d1.Add(-time.Hour)}) {
		t.Fatal("an older delivery reported a change")
	}
	if !x.RecordWarm(warmUpdate{key: k, deliveredAt: d2}) {
		t.Fatal("a newer delivery reported no change")
	}
	if got := snap(t, x, "aa", 1); got.Readability != "warmed" || got.Cache != "cached" ||
		!reflect.DeepEqual(got.LastSwarmDeliveryAt, ts(d2.Unix())) {
		t.Fatalf("empty fields must leave the stored ones: %+v", got)
	}

	// Two failed attempts: the streak starts once and counts twice.
	a1, a2 := wt0.Add(-30*time.Minute), wt0.Add(-10*time.Minute)
	x.RecordWarm(warmUpdate{key: k, readability: "failed", attemptAt: a1})
	x.RecordWarm(warmUpdate{key: k, readability: "failed", attemptAt: a2})
	got = snap(t, x, "aa", 1)
	if got.Readability != "failed" || got.FailedAttempts != 2 ||
		!reflect.DeepEqual(got.FirstFailedAt, ts(a1.Unix())) || !reflect.DeepEqual(got.LastAttemptAt, ts(a2.Unix())) {
		t.Fatalf("after two failures: %+v", got)
	}
	if got.Cache != "cached" || !reflect.DeepEqual(got.LastSwarmDeliveryAt, ts(d2.Unix())) {
		t.Fatalf("a failure must keep the cache state and the delivery time: %+v", got)
	}

	// warmed resets the streak.
	x.RecordWarm(warmUpdate{key: k, readability: "warmed", cache: "cached", deliveredAt: wt0})
	got = snap(t, x, "aa", 1)
	if got.Readability != "warmed" || got.FailedAttempts != 0 || got.FirstFailedAt != nil {
		t.Fatalf("warmed must reset the streak: %+v", got)
	}

	// present touches neither the streak nor the delivery time.
	x.RecordWarm(warmUpdate{key: fk("bb", 0), readability: "failed", attemptAt: a1})
	x.RecordWarm(warmUpdate{key: fk("bb", 0), readability: "present", cache: "cached"})
	if got := snap(t, x, "bb", 0); got.Readability != "present" || got.FailedAttempts != 1 || got.LastSwarmDeliveryAt != nil {
		t.Fatalf("present: %+v", got)
	}

	// present never replaces warmed; failed does, and keeps the delivery time.
	x.RecordWarm(warmUpdate{key: fk("dd", 0), readability: "warmed", cache: "cached", deliveredAt: d1})
	if x.RecordWarm(warmUpdate{key: fk("dd", 0), readability: "present", cache: "cached"}) {
		t.Fatal("present replaced warmed")
	}
	x.RecordWarm(warmUpdate{key: fk("dd", 0), readability: "failed", attemptAt: a1})
	if got := snap(t, x, "dd", 0); got.Readability != "failed" || !reflect.DeepEqual(got.LastSwarmDeliveryAt, ts(d1.Unix())) {
		t.Fatalf("failed after warmed: %+v", got)
	}

	if x.RecordWarm(warmUpdate{key: fk("cc", -1), readability: "warmed"}) {
		t.Fatal("a key that is not <hash>-<index> was recorded")
	}
}

// fakeWarmStore records the order of writes and can hold the first SaveFileWarm.
type fakeWarmStore struct {
	mu      sync.Mutex
	log     []string
	saved   []metadb.FileWarm
	rows    []metadb.FileState
	hold    chan struct{} // closed or sent to release a held save
	inSave  chan struct{} // receives once a held save has started
	saveErr error
	onSave  func()
}

func (s *fakeWarmStore) add(e string) { s.mu.Lock(); s.log = append(s.log, e); s.mu.Unlock() }
func (s *fakeWarmStore) order() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.log...)
}

func (s *fakeWarmStore) SaveFileWarm(w metadb.FileWarm) error {
	if s.onSave != nil {
		s.onSave()
	}
	if s.hold != nil {
		s.inSave <- struct{}{}
		<-s.hold
	}
	s.add("save")
	s.mu.Lock()
	s.saved = append(s.saved, w)
	s.mu.Unlock()
	return s.saveErr
}
func (s *fakeWarmStore) MarkFilesEvicted([]metadb.FileKey, time.Time) error {
	s.add("evict")
	return nil
}
func (s *fakeWarmStore) MarkFilePlayed(string, int, time.Time) error { return nil }
func (s *fakeWarmStore) ClearFileEvicted(string, int) error          { return nil }
func (s *fakeWarmStore) DeleteFileStates(string) error               { return nil }
func (s *fakeWarmStore) LoadFileStates() ([]metadb.FileState, error) { return s.rows, nil }

// A store write never happens under x.mu: IsPlayed runs under the cache's quota lock.
func TestRecordWarmWritesWithoutHoldingTheIndexMutex(t *testing.T) {
	x := warmIndex(t)
	st := &fakeWarmStore{}
	var free atomic.Bool
	st.onSave = func() {
		if x.mu.TryLock() {
			x.mu.Unlock()
			free.Store(true)
		}
	}
	x.store = st
	x.RecordWarm(warmUpdate{key: fk("aa", 1), readability: "warmed", cache: "cached", deliveredAt: wt0})
	if !free.Load() {
		t.Fatal("RecordWarm wrote to the store with x.mu held")
	}
	if len(st.saved) != 1 || st.saved[0].Hash != "aa" || st.saved[0].UpdatedAt != wt0.Unix() ||
		st.saved[0].Readability != "warmed" || st.saved[0].LastSwarmDeliveryAt != wt0.Unix() {
		t.Fatalf("saved = %+v", st.saved)
	}
	// An unchanged update writes nothing.
	x.RecordWarm(warmUpdate{key: fk("aa", 1), cache: "cached"})
	if len(st.saved) != 1 {
		t.Fatalf("an unchanged update wrote: %+v", st.saved)
	}
}

// A warm write in flight and an eviction reach the store in the order they took the
// index, so a stale 'cached' cannot land after 'evicted'.
func TestWarmMuOrdersRecordWarmAndEviction(t *testing.T) {
	x := warmIndex(t)
	st := &fakeWarmStore{hold: make(chan struct{}), inSave: make(chan struct{}, 1)}
	x.store = st
	done := make(chan struct{}, 2)
	go func() {
		x.RecordWarm(warmUpdate{key: fk("aa", 1), readability: "warmed", cache: "cached", deliveredAt: wt0})
		done <- struct{}{}
	}()
	<-st.inSave // the warm write is in the store, holding warmMu
	go func() {
		x.OnEvicted([]warmup.Eviction{{Key: fk("aa", 1)}})
		done <- struct{}{}
	}()
	time.Sleep(50 * time.Millisecond)
	if got := st.order(); len(got) != 0 {
		t.Fatalf("the eviction reached the store while the warm write was in flight: %v", got)
	}
	st.hold <- struct{}{}
	<-done
	<-done
	if got := st.order(); !reflect.DeepEqual(got, []string{"save", "evict"}) {
		t.Fatalf("store order = %v", got)
	}
}

// Eviction sets the cache state in memory and never the readability.
func TestOnEvictedSetsCacheAndKeepsReadability(t *testing.T) {
	x := warmIndex(t)
	x.RecordWarm(warmUpdate{key: fk("aa", 1), readability: "warmed", cache: "cached", deliveredAt: wt0})
	x.OnEvicted([]warmup.Eviction{{Key: fk("AA", 1)}, {Key: fk("zz", 4)}})
	if got := snap(t, x, "aa", 1); got.Readability != "warmed" || got.Cache != "evicted" {
		t.Fatalf("after eviction: %+v", got)
	}
	if got := snap(t, x, "zz", 4); got.Readability != "unknown" || got.Cache != "evicted" {
		t.Fatalf("a never-recorded file after eviction: %+v", got)
	}
}

func TestWarmSnapshotOmitsPlayedOnlyRowsAndSorts(t *testing.T) {
	x := warmIndex(t)
	x.MarkPlayed(fk("played", 1))
	x.RecordWarm(warmUpdate{key: fk("aa", 3), readability: "present", cache: "cached"})
	x.RecordWarm(warmUpdate{key: fk("aa", 1), cache: "cached"})
	s := x.WarmSnapshot()
	if _, ok := s["played"]; ok {
		t.Fatalf("a played-only row was served: %+v", s)
	}
	if len(s["aa"]) != 2 || s["aa"][0].FileIndex != 1 || s["aa"][1].FileIndex != 3 {
		t.Fatalf("aa = %+v", s["aa"])
	}
	if s["aa"][0].Readability != "unknown" {
		t.Fatalf("an empty readability must be served as unknown: %+v", s["aa"][0])
	}
	if got := newFileStateIndex(nil, 90).WarmSnapshot(); got == nil || len(got) != 0 {
		t.Fatalf("empty snapshot = %v", got)
	}
}

// The record survives a restart through the store, and attach keeps the played mark.
func TestWarmRecordRoundTripsThroughTheStore(t *testing.T) {
	db := testStore(t, t.TempDir()+"/s.db")
	x := warmIndex(t)
	x.store = db
	x.MarkPlayed(fk("aa", 1))
	x.RecordWarm(warmUpdate{key: fk("aa", 1), readability: "failed", cache: "cached",
		deliveredAt: wt0.Add(-time.Hour), attemptAt: wt0})
	x.OnEvicted([]warmup.Eviction{{Key: fk("aa", 1)}})

	y := warmIndex(t)
	if err := y.attach(db); err != nil {
		t.Fatal(err)
	}
	got := snap(t, y, "aa", 1)
	want := fileHealth{FileIndex: 1, Readability: "failed", Cache: "evicted", FailedAttempts: 1,
		FirstFailedAt: ts(wt0.Unix()), LastAttemptAt: ts(wt0.Unix()), LastSwarmDeliveryAt: ts(wt0.Add(-time.Hour).Unix())}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("after restart:\n got %+v\nwant %+v", got, want)
	}
	if !y.EverPlayed(fk("aa", 1)) || !y.HasEvictedMark(fk("aa", 1)) {
		t.Fatal("attach lost the marks")
	}
}

func TestRecordWarmLogsAStoreFailureAndKeepsMemory(t *testing.T) {
	x := warmIndex(t)
	var logged int
	x.logf = func(string, ...interface{}) { logged++ }
	x.store = &fakeWarmStore{saveErr: errors.New("disk")}
	x.RecordWarm(warmUpdate{key: fk("aa", 1), readability: "warmed", cache: "cached", deliveredAt: wt0})
	if logged != 1 || snap(t, x, "aa", 1).Readability != "warmed" {
		t.Fatalf("logged %d", logged)
	}
}

// --- the recorder -----------------------------------------------------------------

func recorderIndex(t *testing.T, delivered func(warmup.FileKey, int64) time.Time) *fileStateIndex {
	t.Helper()
	x := warmIndex(t)
	x.deliveredAt = delivered
	return x
}

// The row carries the engine's stamp, not the current time (decision 4).
func TestRecorderStampsTheDeliveryTimeNotNow(t *testing.T) {
	stamp := wt0.Add(-3 * time.Hour)
	x := recorderIndex(t, func(warmup.FileKey, int64) time.Time { return stamp })
	x.windowsCached(fk("AA", 2), 1<<30)
	x.drainPending()
	got := snap(t, x, "aa", 2)
	if got.Readability != "warmed" || got.Cache != "cached" || !reflect.DeepEqual(got.LastSwarmDeliveryAt, ts(stamp.Unix())) {
		t.Fatalf("row = %+v", got)
	}
}

// No stamp: the windows are cached, and nothing is claimed about the swarm.
func TestRecorderWithNoStampRecordsCachedOnly(t *testing.T) {
	x := recorderIndex(t, func(warmup.FileKey, int64) time.Time { return time.Time{} })
	x.windowsCached(fk("aa", 2), 1<<30)
	x.drainPending()
	got := snap(t, x, "aa", 2)
	if got.Readability != "unknown" || got.Cache != "cached" || got.LastSwarmDeliveryAt != nil {
		t.Fatalf("row = %+v", got)
	}
}

// A file already cached and warmed is not queued again; one evicted since is.
func TestRecorderSkipsAWarmedCachedFileAndRequeuesAnEvictedOne(t *testing.T) {
	var queries atomic.Int32
	x := recorderIndex(t, func(warmup.FileKey, int64) time.Time { queries.Add(1); return wt0 })
	x.windowsCached(fk("aa", 1), 10)
	x.drainPending()
	x.windowsCached(fk("aa", 1), 10)
	x.drainPending()
	if queries.Load() != 1 {
		t.Fatalf("engine queried %d times for a file that was already recorded", queries.Load())
	}
	x.OnEvicted([]warmup.Eviction{{Key: fk("aa", 1)}})
	x.windowsCached(fk("aa", 1), 10)
	x.drainPending()
	if got := snap(t, x, "aa", 1); got.Cache != "cached" || got.Readability != "warmed" || queries.Load() != 2 {
		t.Fatalf("after re-cache: %+v, %d queries", got, queries.Load())
	}
}

// The hook target is memory only: with a store that blocks, windowsCached still returns.
func TestWindowsCachedNeverTouchesTheStore(t *testing.T) {
	x := recorderIndex(t, func(warmup.FileKey, int64) time.Time { return wt0 })
	st := &fakeWarmStore{hold: make(chan struct{}), inSave: make(chan struct{}, 1)}
	x.store = st
	returned := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			x.windowsCached(fk("aa", i), 10)
		}
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("windowsCached blocked")
	}
	if len(st.order()) != 0 {
		t.Fatal("windowsCached wrote to the store")
	}
}

// The goroutine drains on a signal and stops with the channel.
func TestRecorderGoroutineDrainsAndStops(t *testing.T) {
	x := recorderIndex(t, func(warmup.FileKey, int64) time.Time { return wt0 })
	stop := make(chan struct{})
	finished := make(chan struct{})
	go func() { x.runRecorder(stop); close(finished) }()
	x.windowsCached(fk("aa", 1), 10)
	deadline := time.Now().Add(3 * time.Second)
	for {
		if len(x.WarmSnapshot()["aa"]) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the recorder never recorded")
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(stop)
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("the recorder did not stop")
	}
}

func TestRecorderCountsTheMountFills(t *testing.T) {
	w0, c0 := warmFillsMountWarmed.Load(), warmFillsMountCached.Load()
	x := recorderIndex(t, func(k warmup.FileKey, _ int64) time.Time {
		if k.FileIdx == 1 {
			return wt0
		}
		return time.Time{}
	})
	x.windowsCached(fk("aa", 1), 10)
	x.windowsCached(fk("aa", 2), 10)
	x.drainPending()
	if warmFillsMountWarmed.Load()-w0 != 1 || warmFillsMountCached.Load()-c0 != 1 {
		t.Fatalf("warmed +%d, cached +%d", warmFillsMountWarmed.Load()-w0, warmFillsMountCached.Load()-c0)
	}
}

// A file recorded as cached with no stamp is queried again on the next hook: an
// unchanged answer writes nothing, and a stamp that appears later makes it warmed.
func TestRecorderRequeuesACachedFileUntilAStampAppears(t *testing.T) {
	var stamp time.Time
	x := recorderIndex(t, func(warmup.FileKey, int64) time.Time { return stamp })
	st := &fakeWarmStore{}
	x.store = st
	for i := 0; i < 2; i++ {
		x.windowsCached(fk("aa", 1), 10)
		x.drainPending()
	}
	if len(st.saved) != 1 || snap(t, x, "aa", 1).Readability != "unknown" {
		t.Fatalf("cached-only: %d writes, %+v", len(st.saved), snap(t, x, "aa", 1))
	}
	stamp = wt0.Add(-time.Hour)
	x.windowsCached(fk("aa", 1), 10)
	x.drainPending()
	got := snap(t, x, "aa", 1)
	if len(st.saved) != 2 || got.Readability != "warmed" || !reflect.DeepEqual(got.LastSwarmDeliveryAt, ts(stamp.Unix())) {
		t.Fatalf("after the stamp: %d writes, %+v", len(st.saved), got)
	}
}

// A panic in the engine call is recovered and the other files still record.
func TestRecorderRecoversPerKey(t *testing.T) {
	x := recorderIndex(t, func(k warmup.FileKey, _ int64) time.Time {
		if k.FileIdx == 1 {
			panic("engine")
		}
		return wt0
	})
	var logged int
	x.logf = func(string, ...interface{}) { logged++ }
	x.windowsCached(fk("aa", 1), 10)
	x.windowsCached(fk("aa", 2), 10)
	x.drainPending()
	if logged != 1 || len(x.WarmSnapshot()["aa"]) != 1 {
		t.Fatalf("logged %d, snapshot %+v", logged, x.WarmSnapshot())
	}
}

// Forgetting a torrent also drops its pending files, so a removed torrent is not
// recorded again; the stop path drains what is left.
func TestForgetHashDropsPendingAndStopDrains(t *testing.T) {
	x := recorderIndex(t, func(warmup.FileKey, int64) time.Time { return wt0 })
	x.windowsCached(fk("gone", 1), 10)
	x.ForgetHash("GONE")
	x.drainPending()
	if len(x.WarmSnapshot()) != 0 {
		t.Fatalf("a forgotten torrent was recorded: %+v", x.WarmSnapshot())
	}
	x.windowsCached(fk("aa", 1), 10)
	stop := make(chan struct{})
	close(stop)
	x.runRecorder(stop)
	if len(x.WarmSnapshot()["aa"]) != 1 {
		t.Fatal("the stop path did not drain")
	}
}

func TestOnEvictedKeepsTheEvictionTime(t *testing.T) {
	x := warmIndex(t)
	x.OnEvicted([]warmup.Eviction{{Key: fk("aa", 1)}})
	x.mu.Lock()
	at := x.m[fk("aa", 1)].evictedAt
	x.mu.Unlock()
	if at != wt0.Unix() || !x.HasEvictedMark(fk("aa", 1)) {
		t.Fatalf("evictedAt = %d", at)
	}
}
