package daemon

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"flodni/internal/library"
	"flodni/internal/metadb"
	"flodni/internal/promtext"
	"flodni/internal/warmer"
	"flodni/internal/warmup"
)

// Roadmap step 8: warm_catchup_scope. "all" is the behaviour of catch-up before the
// scope existed; "retries" feeds only the files warm on add attempted and did not
// finish.

func TestCatchupStateTableScopeRetries(t *testing.T) {
	now := wt0
	ago := func(d time.Duration) int64 { return now.Add(-d).Unix() }
	cases := []struct {
		name string
		in   catchupFile
		want string
	}{
		{"failed 21 h ago", catchupFile{warm: fileWarm{readability: readFailed, failedAttempts: 1, lastAttemptAt: ago(21 * time.Hour)}}, statePending},
		{"failed 19 h ago", catchupFile{warm: fileWarm{readability: readFailed, failedAttempts: 1, lastAttemptAt: ago(19 * time.Hour)}}, stateRetryWait},
		{"failed 4 times", catchupFile{warm: fileWarm{readability: readFailed, failedAttempts: 4, lastAttemptAt: ago(100 * time.Hour)}}, stateGaveUp},
		{"dropped for quota", catchupFile{warm: fileWarm{cache: "dropped_quota", lastAttemptAt: ago(25 * time.Hour)}}, statePending},
		{"dropped for quota 1 h ago waits", catchupFile{warm: fileWarm{cache: "dropped_quota", lastAttemptAt: ago(time.Hour)}}, stateRetryWait},
		{"dropped for no evidence", catchupFile{warm: fileWarm{cache: "dropped_no_evidence", lastAttemptAt: ago(25 * time.Hour)}}, statePending},
		{"lost", catchupFile{warm: fileWarm{cache: "lost", lastAttemptAt: ago(25 * time.Hour)}}, statePending},
		{"write_failed", catchupFile{warm: fileWarm{cache: "write_failed", lastAttemptAt: ago(25 * time.Hour)}}, statePending},
		{"queued 2 h ago", catchupFile{warm: fileWarm{queuedAt: ago(2 * time.Hour)}}, statePending},
		{"queued just over 1 h ago", catchupFile{warm: fileWarm{queuedAt: ago(time.Hour + time.Second)}}, statePending},
		{"queued 59 minutes ago", catchupFile{warm: fileWarm{queuedAt: ago(59 * time.Minute)}}, stateOutOfScope},
		{"queued 2 h ago, a sibling failed", catchupFile{hashFailedRecently: true, warm: fileWarm{queuedAt: ago(2 * time.Hour)}}, stateRetryWait},
		{"queued 2 h ago, refused by WakeStored", catchupFile{notStored: true, warm: fileWarm{queuedAt: ago(2 * time.Hour)}}, stateNotStored},
		{"no record: the cold backlog", catchupFile{}, stateOutOfScope},
		{"cached only, disk incomplete", catchupFile{warm: fileWarm{cache: cacheCached, readability: readWarmed, lastAttemptAt: ago(48 * time.Hour)}}, stateOutOfScope},
		{"present only", catchupFile{warm: fileWarm{cache: cacheCached, readability: readPresent}}, stateOutOfScope},
		{"a sibling failed, this one is cold", catchupFile{hashFailedRecently: true}, stateOutOfScope},
		{"refused by WakeStored and cold", catchupFile{notStored: true}, stateOutOfScope},
		{"evicted, failed", catchupFile{evicted: true, warm: fileWarm{readability: readFailed}}, stateEvicted},
		{"evicted by cache state, queued long ago", catchupFile{warm: fileWarm{cache: cacheEvicted, queuedAt: ago(5 * time.Hour)}}, stateEvicted},
		{"evicted mark, lost", catchupFile{evicted: true, warm: fileWarm{cache: "lost"}}, stateEvicted},
		{"warm on disk, queued long ago", catchupFile{complete: true, warm: fileWarm{queuedAt: ago(5 * time.Hour)}}, stateWarm},
	}
	for _, c := range cases {
		c.in.retriesOnly = true
		if got := catchupState(c.in, now); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

// Scope "all" ignores the queued record: the same files, the same answers as before.
func TestCatchupStateScopeAllIgnoresTheQueuedRecord(t *testing.T) {
	now := wt0
	if got := catchupState(catchupFile{}, now); got != statePending {
		t.Fatalf("no record: %s", got)
	}
	if got := catchupState(catchupFile{warm: fileWarm{queuedAt: now.Add(-5 * time.Hour).Unix()}}, now); got != statePending {
		t.Fatalf("queued: %s", got)
	}
	if got := catchupState(catchupFile{warm: fileWarm{cache: cacheCached}}, now); got != statePending {
		t.Fatalf("cached only: %s", got)
	}
}

func TestQueuedIsNotAnAPIRecord(t *testing.T) {
	x := warmIndex(t)
	x.RecordWarm(warmUpdate{key: fk("AA", 1), queuedAt: wt0})
	if got := x.WarmSnapshot()["aa"]; len(got) != 0 {
		t.Fatalf("a queued-only row shows in the API: %+v", got)
	}
}

func scopeEnv(t *testing.T, scope string) *catchupEnv {
	e := newCatchupEnv(t)
	e.c.scope = func() string { return scope }
	return e
}

func TestScopeRetriesFeedsOnlyFilesWarmOnAddDidNotFinish(t *testing.T) {
	e := scopeEnv(t, "retries")
	now := e.now
	e.items = []library.Item{
		stub("cold", 1, 90), stub("cached", 1, 90), stub("drop", 1, 90), stub("lost", 1, 90),
		stub("wf", 1, 90), stub("fail", 1, 90), stub("qold", 1, 90), stub("qnew", 1, 90),
	}
	e.x.RecordWarm(warmUpdate{key: fk("cached", 1), cache: cacheCached, attemptAt: now.Add(-48 * time.Hour)})
	e.x.RecordWarm(warmUpdate{key: fk("drop", 1), cache: "dropped_quota", attemptAt: now.Add(-48 * time.Hour)})
	e.x.RecordWarm(warmUpdate{key: fk("lost", 1), cache: "lost", attemptAt: now.Add(-48 * time.Hour)})
	e.x.RecordWarm(warmUpdate{key: fk("wf", 1), cache: "write_failed", attemptAt: now.Add(-48 * time.Hour)})
	e.x.RecordWarm(warmUpdate{key: fk("fail", 1), readability: readFailed, attemptAt: now.Add(-48 * time.Hour)})
	e.x.RecordWarm(warmUpdate{key: fk("qold", 1), queuedAt: now.Add(-2 * time.Hour)})
	e.x.RecordWarm(warmUpdate{key: fk("qnew", 1), queuedAt: now.Add(-10 * time.Minute)})
	e.c.runPass()
	fed := strings.Join(e.tg.feeds(), " ")
	for _, want := range []string{"drop/1", "lost/1", "wf/1", "fail/1", "qold/1"} {
		if !strings.Contains(fed, want) {
			t.Errorf("fed %q, missing %s", fed, want)
		}
	}
	for _, not := range []string{"cold/", "cached/", "qnew/"} {
		if strings.Contains(fed, not) {
			t.Errorf("fed %q, must not contain %s", fed, not)
		}
	}
	if g := e.gauge.get(); g[stateOutOfScope] != 3 || g[statePending] != 5 {
		t.Fatalf("gauge = %v, want out_of_scope 3 and pending 5", g)
	}
}

func TestScopeAllStillFeedsTheColdBacklog(t *testing.T) {
	for _, scope := range []string{"all", ""} {
		e := scopeEnv(t, scope)
		e.items = []library.Item{stub("cold", 1, 90), stub("cached", 1, 80)}
		e.x.RecordWarm(warmUpdate{key: fk("cached", 1), cache: cacheCached, attemptAt: e.now.Add(-48 * time.Hour)})
		e.c.runPass()
		if got := strings.Join(e.tg.feeds(), " "); got != "cold/1 cached/1" {
			t.Fatalf("scope %q fed %q, want both", scope, got)
		}
		if g := e.gauge.get(); g[stateOutOfScope] != 0 {
			t.Fatalf("scope %q: out_of_scope = %d", scope, g[stateOutOfScope])
		}
	}
}

func TestAPassLogsAndGaugesTheScopeItRan(t *testing.T) {
	e := scopeEnv(t, "retries")
	e.items = []library.Item{stub("cold", 1, 90)}
	e.c.runPass()
	e.logMu.Lock()
	var start, end map[string]interface{}
	if len(e.log) != 2 {
		e.logMu.Unlock()
		t.Fatalf("log = %q", e.log)
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(e.log[0], "[WarmCatchup] ")), &start); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(e.log[1], "[WarmCatchup] ")), &end); err != nil {
		t.Fatal(err)
	}
	e.logMu.Unlock()
	if start["scope"] != "retries" || end["scope"] != "retries" || start["out_of_scope"] != float64(1) {
		t.Fatalf("start = %v, end = %v", start, end)
	}
	var pw promtext.Writer
	collectCatchupProm(&pw, e.gauge)
	out := string(pw.Bytes())
	for _, want := range []string{
		`flodni_warm_catchup_scope{scope="retries"} 1`,
		`flodni_warm_catchup_scope{scope="all"} 0`,
		`flodni_warm_catchup_files{state="out_of_scope"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestAnUnsetScopeRunsAsAll(t *testing.T) {
	e := newCatchupEnv(t) // no scope func at all
	e.items = []library.Item{stub("cold", 1, 90)}
	e.c.runPass()
	e.logMu.Lock()
	defer e.logMu.Unlock()
	if !strings.Contains(e.log[0], `"scope":"all"`) {
		t.Fatalf("start line = %s", e.log[0])
	}
}

// ---- the queued record ----

func TestWarmOnAddWritesTheQueuedRecordAtEnqueueAndAnyOutcomeClearsIt(t *testing.T) {
	x := warmIndex(t)
	d := addWarmerDeps(x, make(chan struct{}))
	d.Queued([]warmup.FileKey{fk("AA", 1), fk("AA", 2)})
	if w, _ := x.CatchupFact(fk("aa", 1)); w.queuedAt != wt0.Unix() {
		t.Fatalf("queued_at = %d, want %d", w.queuedAt, wt0.Unix())
	}
	// Every outcome the warmer records passes ClearQueued, and none of them touches
	// anything but the fields it names.
	d.Record(warmer.Update{Key: fk("AA", 1), Cache: warmer.CacheDroppedQuota, ClearQueued: true})
	w, _ := x.CatchupFact(fk("aa", 1))
	if w.queuedAt != 0 || w.cache != warmer.CacheDroppedQuota {
		t.Fatalf("after an outcome: %+v", w)
	}
	if w2, _ := x.CatchupFact(fk("aa", 2)); w2.queuedAt == 0 {
		t.Fatal("an outcome of file 1 cleared file 2")
	}
}

func TestAClearedQueuedRecordLeavesReadabilityAndTheStreakAlone(t *testing.T) {
	x := warmIndex(t)
	k := fk("aa", 1)
	x.RecordWarm(warmUpdate{key: k, readability: readFailed, attemptAt: wt0.Add(-30 * time.Hour)})
	x.RecordWarm(warmUpdate{key: k, readability: readFailed, attemptAt: wt0.Add(-25 * time.Hour)})
	x.RecordWarm(warmUpdate{key: k, queuedAt: wt0.Add(-2 * time.Hour)})
	before, _ := x.CatchupFact(k)
	// A dropped_quota, write_failed or lost outcome (no swarm evidence) clears the
	// queued record and writes the cache state; the readability and the streak stay.
	for _, c := range []string{"dropped_quota", "write_failed", "lost"} {
		x.RecordWarm(warmUpdate{key: k, cache: c, attemptAt: wt0, clearQueued: true})
		got, _ := x.CatchupFact(k)
		if got.queuedAt != 0 || got.cache != c || got.readability != readFailed ||
			got.failedAttempts != before.failedAttempts || got.firstFailedAt != before.firstFailedAt {
			t.Fatalf("%s: %+v, before %+v", c, got, before)
		}
	}
}

func TestQueuedRowsSurviveARestartAndBecomeCandidates(t *testing.T) {
	// The daemon died with a file queued: the row is in the database with queued_at set.
	st := &fakeWarmStore{}
	x1 := warmIndex(t)
	if err := x1.attach(st); err != nil {
		t.Fatal(err)
	}
	x1.RecordWarm(warmUpdate{key: fk("AA", 3), queuedAt: wt0.Add(-2 * time.Hour)})
	if len(st.saved) != 1 || st.saved[0].QueuedAt != wt0.Add(-2*time.Hour).Unix() {
		t.Fatalf("saved = %+v", st.saved)
	}
	row := st.saved[0]
	st2 := &fakeWarmStore{rows: []metadb.FileState{{Hash: row.Hash, FileIdx: row.FileIdx, QueuedAt: row.QueuedAt}}}
	x2 := warmIndex(t)
	if err := x2.attach(st2); err != nil {
		t.Fatal(err)
	}
	w, ev := x2.CatchupFact(fk("aa", 3))
	if got := catchupState(catchupFile{warm: w, evicted: ev, retriesOnly: true}, wt0); got != statePending {
		t.Fatalf("after a restart: %s (%+v)", got, w)
	}
	// And an outcome clears the stored column.
	x2.RecordWarm(warmUpdate{key: fk("aa", 3), cache: cacheCached, clearQueued: true})
	if got := st2.saved[len(st2.saved)-1]; got.QueuedAt != 0 {
		t.Fatalf("stored queued_at = %d after an outcome", got.QueuedAt)
	}
}
