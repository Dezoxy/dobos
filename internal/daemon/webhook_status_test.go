package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"flodni/internal/config"
	"flodni/internal/metadb"
)

// The play-webhook health marks (Reliability step 7): three Unix-second times kept in
// memory, persisted at most once an hour each, and one predicate over them.

var whT0 = time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)

const day = 24 * time.Hour

type markClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *markClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *markClock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type markWrite struct {
	name string
	at   int64
}

// fakeMarkStore records writes. block, when set, holds every SetMark until it is closed.
type fakeMarkStore struct {
	mu     sync.Mutex
	writes []markWrite
	loaded map[string]int64
	block  chan struct{}
	wrote  chan markWrite
}

func newFakeMarkStore() *fakeMarkStore { return &fakeMarkStore{wrote: make(chan markWrite, 16)} }

func (f *fakeMarkStore) SetMark(name string, at time.Time) error {
	if f.block != nil {
		<-f.block
	}
	f.mu.Lock()
	w := markWrite{name, at.Unix()}
	f.writes = append(f.writes, w)
	f.mu.Unlock()
	f.wrote <- w
	return nil
}

func (f *fakeMarkStore) LoadMarks() (map[string]int64, error) { return f.loaded, nil }

func (f *fakeMarkStore) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.writes)
}

// useMarks swaps in fresh marks on a fake clock, a log buffer and a warn-days value.
func useMarks(t *testing.T, warnDays int) (*webhookMarks, *markClock, *bytes.Buffer) {
	t.Helper()
	clock := &markClock{t: whT0}
	var buf bytes.Buffer
	var mu sync.Mutex
	m := newWebhookMarks()
	m.now = clock.Now
	m.logf = func(f string, v ...interface{}) {
		mu.Lock()
		defer mu.Unlock()
		buf.WriteString(strings.TrimSpace(fmt.Sprintf(f, v...)) + "\n")
	}
	prevM, prevD := webhookStatus, webhookWarnDays
	webhookStatus, webhookWarnDays = m, func() int { return warnDays }
	t.Cleanup(func() { webhookStatus, webhookWarnDays = prevM, prevD })
	t.Cleanup(m.waitPersist) // runs first: no goroutine outlives the swap
	return m, clock, &buf
}

func TestWebhookWarnPredicate(t *testing.T) {
	now := whT0
	ago := func(d time.Duration) int64 { return now.Add(-d).Unix() }
	tests := []struct {
		name                 string
		warnDays             int
		unconfirmed, matched int64
		want                 bool
	}{
		{"never matched, unconfirmed read today", 7, ago(time.Hour), 0, true},
		{"never matched, no unconfirmed read", 7, 0, 0, false},
		{"never matched, unconfirmed read long ago", 7, ago(30 * day), 0, false},
		{"matched within N days", 7, ago(time.Hour), ago(2 * day), false},
		{"matched long ago, unconfirmed read recent", 7, ago(time.Hour), ago(30 * day), true},
		{"matched long ago, unconfirmed read old too", 7, ago(20 * day), ago(30 * day), false},
		{"matched just inside the window", 7, ago(time.Hour), ago(7*day - time.Minute), false},
		{"matched just outside the window", 7, ago(time.Hour), ago(7*day + time.Minute), true},
		{"warning off", 0, ago(time.Hour), 0, false},
		{"negative days is off", -1, ago(time.Hour), 0, false},
		{"future matched stamp counts as now", 7, ago(time.Hour), now.Add(40 * day).Unix(), false},
		{"future unconfirmed stamp counts as now", 7, now.Add(40 * day).Unix(), 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := webhookWarn(now, tt.warnDays, tt.unconfirmed, tt.matched); got != tt.want {
				t.Fatalf("webhookWarn = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMarkStampsAreThrottledToOneWritePerHourPerMark(t *testing.T) {
	m, clock, _ := useMarks(t, 7)
	st := newFakeMarkStore()
	m.attach(st)

	m.noteWebhook(true)
	m.noteWebhook(true)
	clock.Add(59 * time.Minute)
	m.noteWebhook(true)
	if st.count() != 1 {
		t.Fatalf("writes within the hour = %d, want 1", st.count())
	}
	m.noteWebhook(false) // another mark: its own throttle
	if st.count() != 2 {
		t.Fatalf("a different mark did not write: %d", st.count())
	}
	clock.Add(2 * time.Minute)
	m.noteWebhook(true)
	if st.count() != 3 || st.writes[2].name != markPlayWebhookMatched || st.writes[2].at != clock.Now().Unix() {
		t.Fatalf("after the hour: %+v", st.writes)
	}
	// The in-memory time moved on every stamp, written or not.
	if got := m.matched.at.Load(); got != clock.Now().Unix() {
		t.Fatalf("in-memory matched time %d, want %d", got, clock.Now().Unix())
	}
}

func TestMarksSurviveAReopenedDatabaseAndAFutureStampIsClamped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := metadb.New(path, log.Default())
	if err != nil {
		t.Fatal(err)
	}
	m, clock, _ := useMarks(t, 7)
	m.attach(db)
	m.noteWebhook(true)
	m.noteWebhook(false)
	m.noteUnconfirmedRead("vlc")
	m.waitPersist()
	_ = db.Close()

	db, err = metadb.New(path, log.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := db.SetMark(markUnconfirmedRead, clock.Now().Add(400*day)); err != nil { // a clock error in the past
		t.Fatal(err)
	}
	m2, _, _ := useMarks(t, 7)
	m2.attach(db)
	if got := m2.matched.at.Load(); got != whT0.Unix() {
		t.Fatalf("matched after reopen = %d, want %d", got, whT0.Unix())
	}
	if got := m2.unmatched.at.Load(); got != whT0.Unix() {
		t.Fatalf("unmatched after reopen = %d", got)
	}
	if got := m2.unconfirmed.at.Load(); got != whT0.Unix() {
		t.Fatalf("a future unconfirmed stamp was not clamped to now: %d", got)
	}
}

func TestWebhookHandlerStampsMatchedAndUnmatchedPlaysOnly(t *testing.T) {
	playedEnv(t)
	m, _, _ := useMarks(t, 7)

	postWebhook(t, evPayload("media.stop"))
	postWebhook(t, `{"event":"media.play","Metadata":{"title":"Song","librarySectionType":"artist"}}`)
	if m.matched.at.Load() != 0 || m.unmatched.at.Load() != 0 {
		t.Fatal("a stop or a non-video play stamped a mark")
	}
	postWebhook(t, `{"event":"media.play","Metadata":{"title":"Nothing Registered","librarySectionType":"movie"}}`)
	if m.matched.at.Load() != 0 || m.unmatched.at.Load() != whT0.Unix() {
		t.Fatalf("unmatched play: matched %d unmatched %d", m.matched.at.Load(), m.unmatched.at.Load())
	}
	postWebhook(t, evPayload("media.play"))
	if m.matched.at.Load() != whT0.Unix() {
		t.Fatalf("matched play did not stamp: %d", m.matched.at.Load())
	}
}

// crossUnconfirmed closes a session that crossed the margin with no confirmation.
func crossUnconfirmed(t *testing.T, comm string) *TTFFSession {
	t.Helper()
	s := budgetEnv(t, 1<<20)
	s.callerPid.Store(0)
	s.countUnconfirmed(srcFetchBlock, 4<<20, 100<<20)
	if comm != "" {
		s.overMarginComm.Store(&comm)
	}
	s.closeSession()
	return s
}

func TestUnconfirmedCloseStampsAndConfirmedLateDoesNot(t *testing.T) {
	m, _, _ := useMarks(t, 7)
	crossUnconfirmed(t, "")
	if m.unconfirmed.at.Load() != whT0.Unix() {
		t.Fatalf("unconfirmed close did not stamp: %d", m.unconfirmed.at.Load())
	}

	m2, _, _ := useMarks(t, 7)
	s := budgetEnv(t, 1<<20)
	s.countUnconfirmed(srcFetchBlock, 4<<20, 100<<20)
	noteConfirmedLate(s.path)
	s.closeSession()
	if m2.unconfirmed.at.Load() != 0 {
		t.Fatalf("a confirmed-late close stamped: %d", m2.unconfirmed.at.Load())
	}
}

// The FUSE read path closes sessions: the stamp is in memory, the SQLite write is not
// waited for.
func TestUnconfirmedCloseNeverWaitsForTheDatabase(t *testing.T) {
	m, _, _ := useMarks(t, 7)
	st := newFakeMarkStore()
	st.block = make(chan struct{})
	m.attach(st)
	done := make(chan struct{})
	go func() { crossUnconfirmed(t, ""); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("closeSession waited for the mark write")
	}
	if m.unconfirmed.at.Load() == 0 {
		t.Fatal("no in-memory stamp")
	}
	close(st.block)
	select {
	case w := <-st.wrote:
		if w.name != markUnconfirmedRead {
			t.Fatalf("wrote %+v", w)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the write never happened")
	}
}

func TestEvaluateLogsOnlyTransitionsAndNamesTheFix(t *testing.T) {
	m, clock, buf := useMarks(t, 7)
	m.evaluate(7)
	if buf.Len() != 0 {
		t.Fatalf("clear state logged: %q", buf)
	}
	m.noteUnconfirmedRead("vlc") // raises it: the close evaluates
	m.evaluate(7)
	m.evaluate(7)
	m.noteUnconfirmedRead("vlc")
	out := buf.String()
	if strings.Count(out, "WARNING") != 1 || !strings.Contains(out, "no play webhook in 7 days while plays happen; see README > Play webhook") {
		t.Fatalf("warning line: %q", out)
	}
	if strings.Contains(out, "false warning") {
		t.Fatalf("a non-hasher reader marked as possibly false: %q", out)
	}
	buf.Reset()
	clock.Add(time.Minute)
	m.noteWebhook(true)
	m.evaluate(7)
	m.evaluate(7)
	if strings.Count(buf.String(), "[Webhook]") != 1 || strings.Contains(buf.String(), "WARNING") ||
		!strings.Contains(buf.String(), "a play webhook matched a file") {
		t.Fatalf("clear transition: %q", buf)
	}
}

func TestMatchedStampWritesOnlyAfterTheConfirmation(t *testing.T) {
	path, _ := playedEnv(t)
	m, _, _ := useMarks(t, 7)
	st := newFakeMarkStore()
	st.block = make(chan struct{})
	m.attach(st)
	done := make(chan struct{})
	go func() {
		defer close(done)
		req := httptest.NewRequest(http.MethodPost, "/plex/webhook", strings.NewReader(evPayload("media.play")))
		req.Header.Set("Content-Type", "application/json")
		handlePlexWebhook(httptest.NewRecorder(), req)
	}()
	deadline := time.Now().Add(3 * time.Second)
	for !playbackConfirmed(path) {
		if time.Now().After(deadline) {
			close(st.block)
			<-done
			t.Fatal("the play stayed unconfirmed while the mark write was blocked")
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(st.block)
	<-done
}

func TestWarningLineMarksPossiblyFalseOnlyForHashersAndAnalysisTools(t *testing.T) {
	for _, tt := range []struct {
		comms     []string
		wantFalse bool
	}{
		{[]string{"sha256sum"}, true},
		{[]string{"ffprobe", "mediainfo", "md5sum"}, true},
		{[]string{"sha256sum", "vlc"}, false},
		{[]string{"sha256sum", ""}, false}, // unknown caller: cannot say
		{[]string{"cp"}, false},
		{nil, false},
	} {
		m, _, buf := useMarks(t, 0) // off while the callers are counted: one evaluation, at the end
		for _, c := range tt.comms {
			m.noteUnconfirmedRead(c)
		}
		if len(tt.comms) == 0 {
			m.unconfirmed.at.Store(whT0.Unix()) // restored from the database: no caller known
		}
		m.evaluate(7)
		got := strings.Contains(buf.String(), "possibly a false warning")
		if got != tt.wantFalse {
			t.Errorf("%v: possibly-false = %v, want %v: %q", tt.comms, got, tt.wantFalse, buf)
		}
		if !strings.Contains(buf.String(), "WARNING") {
			t.Errorf("%v: the warning must still fire: %q", tt.comms, buf)
		}
	}
}

func TestAMatchedWebhookResetsTheCallerTally(t *testing.T) {
	m, _, buf := useMarks(t, 7)
	m.noteUnconfirmedRead("sha256sum")
	m.noteWebhook(true)
	buf.Reset()
	m.noteUnconfirmedRead("vlc")
	m.matched.at.Store(0) // a later stretch with no webhook
	m.evaluate(7)
	if strings.Contains(buf.String(), "possibly a false warning") {
		t.Fatalf("a reader from before the last webhook marked the warning: %q", buf)
	}
}

func TestStartupLine(t *testing.T) {
	now := whT0
	got := webhookStartupLine(now, 0, false, false, 7, "keep-alive: off")
	want := "[Webhook] last matched play webhook: never; played-file protection: inactive (no play recorded); " +
		"reader budget refusal: off (opt-in); warn after 7 days; keep-alive: off"
	if got != want {
		t.Fatalf("\n got %q\nwant %q", got, want)
	}
	got = webhookStartupLine(now, now.Add(-3*day).Unix(), true, true, 0, "keep-alive: on")
	for _, w := range []string{
		"last matched play webhook: 2026-11-10 (3 d ago)", "played-file protection: active",
		"reader budget refusal: on (arms at the first matched play webhook after this start)", "warning: off",
	} {
		if !strings.Contains(got, w) {
			t.Errorf("%q lacks %q", got, w)
		}
	}
	if !strings.Contains(got, "keep-alive: on") {
		t.Fatalf("the keep-alive field is missing: %q", got)
	}
}

func healthWithWarnings(w func() []string) healthChecks {
	return healthChecks{mount: okCheck, engine: okCheck, stateDB: okCheck, warnings: w, timeout: time.Second}
}

func TestHealthWarningsAlwaysPresentAndNeverChangeStatusOrCode(t *testing.T) {
	// Nil stubs and clear: [] in the body, not null, not absent.
	for _, w := range []func() []string{nil, func() []string { return nil }, func() []string { return []string{} }} {
		rec, rep := serveHealth(t, healthWithWarnings(w), http.MethodGet)
		if rec.Code != 200 || rep.Status != "ok" || !strings.Contains(rec.Body.String(), `"warnings":[]`) {
			t.Fatalf("clear: %d %q %s", rec.Code, rep.Status, rec.Body)
		}
	}
	rec, rep := serveHealth(t, healthWithWarnings(func() []string { return []string{"no_play_webhook"} }), http.MethodGet)
	if rec.Code != 200 || rep.Status != "ok" {
		t.Fatalf("a warning changed status or code: %d %q", rec.Code, rep.Status)
	}
	if len(rep.Warnings) != 1 || rep.Warnings[0] != "no_play_webhook" {
		t.Fatalf("warnings = %v", rep.Warnings)
	}
	if strings.ContainsAny(rec.Body.String()[strings.Index(rec.Body.String(), `"warnings"`):], "0123456789/") {
		t.Fatalf("the warnings part carries digits or paths: %s", rec.Body)
	}
	// A real failure still decides the code, with the warning alongside.
	h := healthWithWarnings(func() []string { return []string{"no_play_webhook"} })
	h.engine = downCheck
	rec, rep = serveHealth(t, h, http.MethodGet)
	if rec.Code != 503 || rep.Status != "degraded" || len(rep.Warnings) != 1 {
		t.Fatalf("degraded with a warning: %d %q %v", rec.Code, rep.Status, rep.Warnings)
	}
	// The real wiring computes it from the marks, no check.
	m, _, _ := useMarks(t, 7)
	var body struct {
		Warnings []string `json:"warnings"`
	}
	if newHealthChecks().warnings == nil {
		t.Fatal("the daemon's health checks are not wired to the warnings")
	}
	real := healthWithWarnings(activeWarnings) // the wiring newHealthChecks uses, with stubbed checks
	rec = httptestGet(real, t)
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Warnings == nil || len(body.Warnings) != 0 {
		t.Fatalf("fresh daemon warnings = %v (%s)", body.Warnings, rec.Body)
	}
	m.noteUnconfirmedRead("vlc")
	m.waitPersist()
	rec = httptestGet(real, t)
	body.Warnings = nil
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body.Warnings) != 1 || body.Warnings[0] != "no_play_webhook" {
		t.Fatalf("raised warnings = %v", body.Warnings)
	}
}

func TestWarningGaugeAndTimestampsStartAtZeroAndFollowThePredicate(t *testing.T) {
	m, _, _ := useMarks(t, 7)
	body := scrape(t).Body.String()
	for _, want := range []string{
		`flodni_warning{name="no_play_webhook"} 0`,
		`flodni_playback_webhook_last_timestamp_seconds{matched="true"} 0`,
		`flodni_playback_webhook_last_timestamp_seconds{matched="false"} 0`,
		`flodni_unconfirmed_read_last_session_timestamp_seconds 0`,
	} {
		if !strings.Contains(body, want+"\n") {
			t.Errorf("series %q missing", want)
		}
	}
	m.noteUnconfirmedRead("vlc")
	body = scrape(t).Body.String()
	if !strings.Contains(body, `flodni_warning{name="no_play_webhook"} 1`+"\n") ||
		strings.Contains(body, "flodni_unconfirmed_read_last_session_timestamp_seconds 0\n") {
		t.Fatalf("raised state not exported:\n%s", body)
	}
	m.noteWebhook(true)
	if !strings.Contains(scrape(t).Body.String(), `flodni_warning{name="no_play_webhook"} 0`+"\n") {
		t.Fatal("the gauge did not clear after a matched webhook")
	}
	webhookWarnDays = func() int { return 0 }
	m.matched.at.Store(0)
	if !strings.Contains(scrape(t).Body.String(), `flodni_warning{name="no_play_webhook"} 0`+"\n") {
		t.Fatal("warn days 0 still exports 1")
	}
}

func TestWebhookWarnDaysReadsTheConfigLive(t *testing.T) {
	prev := globalConfig.Load()
	t.Cleanup(func() { globalConfig.Store(prev) })
	globalConfig.Store(&config.Config{PlaybackWebhookWarnDays: 3})
	if got := configuredWebhookWarnDays(); got != 3 {
		t.Fatalf("got %d, want 3", got)
	}
	globalConfig.Store(&config.Config{PlaybackWebhookWarnDays: 0})
	if got := configuredWebhookWarnDays(); got != 0 {
		t.Fatalf("got %d, want 0", got)
	}
	globalConfig.Store(nil)
	if got := configuredWebhookWarnDays(); got != 7 {
		t.Fatalf("no config: got %d, want 7", got)
	}
}

func TestPlayRecordedReportsAnyRecordedPlay(t *testing.T) {
	x := newFileStateIndex(nil, 90)
	if x.PlayRecorded() {
		t.Fatal("fresh index reports a play")
	}
	x.MarkPlayed(fk("h", 1))
	if !x.PlayRecorded() {
		t.Fatal("a recorded play is not reported")
	}
}

func httptestGet(h healthChecks, t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.handler(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	return rec
}
