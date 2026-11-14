package daemon

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"flodni/internal/promtext"
)

// Play-webhook health (Reliability step 7). Played-file protection, the reader budget
// refusal and the planned keep-alive all key on a media server webhook that arrives and
// matches a file; without one they silently do nothing. This file keeps three times,
// raises an advisory warning when readers play but no webhook matched for N days, and
// reports it. The warning never changes /api/health's status or HTTP code.
//
// The times are Unix seconds, in memory (atomics) and in the state database's
// daemon_marks table, written at most once an hour per mark: a Plex progress ping
// is a media.resume every few seconds. With the state database off they live since start.

const (
	markPlayWebhookMatched   = "play_webhook_matched"
	markPlayWebhookUnmatched = "play_webhook_unmatched"
	markUnconfirmedRead      = "unconfirmed_read"

	// warningNoPlayWebhook is the one word /api/health and flodni_warning use.
	warningNoPlayWebhook = "no_play_webhook"

	markWriteInterval        = time.Hour
	webhookWarnDefaultDays   = 7
	webhookEvaluateInterval  = time.Hour
	webhookWarnReadmeSection = "README > Play webhook"
)

type markStore interface {
	SetMark(name string, at time.Time) error
	LoadMarks() (map[string]int64, error)
}

// markCell is one mark: its time and the time of its last database write.
type markCell struct {
	at    atomic.Int64
	wrote atomic.Int64
}

// raise moves the time forward only.
func (c *markCell) raise(t int64) {
	for {
		cur := c.at.Load()
		if t <= cur || c.at.CompareAndSwap(cur, t) {
			return
		}
	}
}

type webhookMarks struct {
	now  func() time.Time
	logf func(format string, v ...interface{})

	matched, unmatched, unconfirmed markCell

	smu   sync.Mutex
	store markStore
	wg    sync.WaitGroup // database writes started off the caller's goroutine

	// Callers of the unconfirmed sessions since start or the last matched webhook, by
	// process name: a known hasher or analysis tool, or anything else (including unknown).
	hashers, others atomic.Int64

	warned atomic.Bool // the last evaluation raised the warning
}

func newWebhookMarks() *webhookMarks {
	return &webhookMarks{
		now: time.Now,
		logf: func(f string, v ...interface{}) {
			if logger != nil {
				logger.Printf(f, v...)
			}
		},
	}
}

// webhookStatus is the process-wide instance; tests swap it.
var webhookStatus = newWebhookMarks()

// webhookWarnDays is playback_webhook_warn_days, read at every call. A var so tests can
// set it.
var webhookWarnDays = configuredWebhookWarnDays

func configuredWebhookWarnDays() int {
	if c := gc(); c != nil {
		return c.PlaybackWebhookWarnDays
	}
	return webhookWarnDefaultDays
}

func (m *webhookMarks) cell(name string) *markCell {
	switch name {
	case markPlayWebhookMatched:
		return &m.matched
	case markPlayWebhookUnmatched:
		return &m.unmatched
	case markUnconfirmedRead:
		return &m.unconfirmed
	}
	return nil
}

// attach loads the stored times and keeps the store for later writes. A stored time in
// the future (a clock that was wrong once) is taken as now in memory. The database keeps
// the larger value, so the clamp repeats at every start until the clock catches up.
// A load error leaves the marks since-start only; writes still go to the store.
func (m *webhookMarks) attach(s markStore) {
	marks, err := s.LoadMarks()
	if err != nil {
		m.logf("WARNING: [Webhook] cannot load the play webhook times: %v (kept since start only)", err)
	}
	now := m.now().Unix()
	for name, at := range marks {
		if c := m.cell(name); c != nil {
			at = min(at, now)
			c.raise(at)
			c.wrote.Store(at)
		}
	}
	m.smu.Lock()
	m.store = s
	m.smu.Unlock()
}

// stamp raises a mark to now and reports whether its database write is due: at most one
// per mark per hour. The caller that gets true owns the write.
func (m *webhookMarks) stamp(c *markCell) (at time.Time, due bool) {
	at = m.now()
	c.raise(at.Unix())
	m.smu.Lock()
	has := m.store != nil
	m.smu.Unlock()
	if !has {
		return at, false
	}
	last := c.wrote.Load()
	if at.Unix()-last < int64(markWriteInterval/time.Second) {
		return at, false
	}
	return at, c.wrote.CompareAndSwap(last, at.Unix())
}

func (m *webhookMarks) persist(name string, at time.Time) {
	m.smu.Lock()
	s := m.store
	m.smu.Unlock()
	if s == nil {
		return
	}
	if err := s.SetMark(name, at); err != nil {
		m.logf("WARNING: [Webhook] cannot store the %s time: %v", name, err)
	}
}

// noteWebhook stamps a movie or episode play or resume webhook. The write, when due,
// runs on the webhook handler's goroutine: it is once an hour and must not block a read.
func (m *webhookMarks) noteWebhook(matched bool) {
	name, c := markPlayWebhookUnmatched, &m.unmatched
	if matched {
		name, c = markPlayWebhookMatched, &m.matched
	}
	at, due := m.stamp(c)
	if due {
		m.persist(name, at)
	}
	if matched {
		// Callers from before this webhook say nothing about a warning raised after it.
		m.hashers.Store(0)
		m.others.Store(0)
		m.evaluate(webhookWarnDays())
	}
}

// noteUnconfirmedRead stamps a session that crossed the margin and closed unconfirmed.
// It runs inside closeSession, which a FUSE read can reach: the stamp and the evaluation
// (atomics and at most one log line) run inline, the database write, when due, runs on
// its own goroutine so a read never waits for SQLite.
func (m *webhookMarks) noteUnconfirmedRead(comm string) {
	if isHasherOrAnalysisTool(comm) {
		m.hashers.Add(1)
	} else {
		m.others.Add(1)
	}
	if at, due := m.stamp(&m.unconfirmed); due {
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			m.persist(markUnconfirmedRead, at)
		}()
	}
	m.evaluate(webhookWarnDays())
}

// waitPersist waits for writes started off the caller's goroutine.
func (m *webhookMarks) waitPersist() { m.wg.Wait() }

// webhookWarn is the predicate: a reader session closed unconfirmed within warnDays and
// no matched play webhook arrived within warnDays (never matched counts). warnDays 0 is
// off. A stamp later than now is taken as now.
func webhookWarn(now time.Time, warnDays int, unconfirmedAt, matchedAt int64) bool {
	if warnDays <= 0 || unconfirmedAt <= 0 {
		return false
	}
	n := now.Unix()
	window := int64(warnDays) * 86400
	if n-min(unconfirmedAt, n) > window {
		return false
	}
	return matchedAt <= 0 || n-min(matchedAt, n) > window
}

func (m *webhookMarks) warning(warnDays int) bool {
	return webhookWarn(m.now(), warnDays, m.unconfirmed.at.Load(), m.matched.at.Load())
}

// activeWarnings is /api/health's list: atomics only, never the database.
func activeWarnings() []string {
	if webhookStatus.warning(webhookWarnDays()) {
		return []string{warningNoPlayWebhook}
	}
	return nil
}

// possiblyFalse: every unconfirmed session counted since start (or the last matched
// webhook) came from a hasher or analysis tool, which reads a file end to end without
// anyone playing it.
func (m *webhookMarks) possiblyFalse() bool {
	return m.hashers.Load() > 0 && m.others.Load() == 0
}

// evaluate logs when the warning turns on or off, not on every call.
func (m *webhookMarks) evaluate(warnDays int) {
	on := m.warning(warnDays)
	if m.warned.Swap(on) == on {
		return
	}
	if !on {
		why := "no reader session closed unconfirmed in the window (no play webhook matched either)"
		switch {
		case warnDays <= 0:
			why = "playback_webhook_warn_days is 0"
		case !webhookWarn(m.now(), warnDays, m.now().Unix(), m.matched.at.Load()):
			why = "a play webhook matched a file"
		}
		m.logf("[Webhook] the no-play-webhook warning is clear: %s", why)
		return
	}
	recentUnmatched := warnDays > 0 && m.unmatched.at.Load() > 0 &&
		m.now().Unix()-min(m.unmatched.at.Load(), m.now().Unix()) <= int64(warnDays)*86400
	m.logf("%s", webhookWarningLine(warnDays, recentUnmatched, m.possiblyFalse()))
}

func webhookWarningLine(warnDays int, unmatchedRecently, possiblyFalse bool) string {
	s := fmt.Sprintf("WARNING: [Webhook] no play webhook in %d days while plays happen; see %s", warnDays, webhookWarnReadmeSection)
	if unmatchedRecently {
		s += " (webhooks did arrive, but none matched a file)"
	}
	if possiblyFalse {
		s += " (possibly a false warning: every unconfirmed reader since start or the last matched webhook was a hasher or analysis tool)"
	}
	return s
}

// hasherComms are process names that read a file end to end with nobody playing it. The
// kernel truncates comm to 15 bytes; every name here fits.
var hasherComms = map[string]bool{
	"md5sum": true, "sha1sum": true, "sha224sum": true, "sha256sum": true,
	"sha384sum": true, "sha512sum": true, "b2sum": true, "cksum": true, "xxhsum": true,
	"ffprobe": true, "mediainfo": true,
}

func isHasherOrAnalysisTool(comm string) bool { return hasherComms[comm] }

// webhookStartupLine is the one line that says what the webhook-keyed features need and
// whether a webhook ever matched. keepalive is the keep-alive's field (keepaliveStartupField),
// so "never matched" reads next to "keep-alive configured but inactive".
func webhookStartupLine(now time.Time, matchedAt int64, playRecorded, refuseOn bool, warnDays int, keepalive string) string {
	last := "never"
	if matchedAt > 0 {
		t := time.Unix(min(matchedAt, now.Unix()), 0).UTC()
		last = fmt.Sprintf("%s (%d d ago)", t.Format("2006-01-02"), int(now.Sub(t)/(24*time.Hour)))
	}
	prot := "inactive (no play recorded)"
	if playRecorded {
		prot = "active"
	}
	refuse := "off (opt-in)"
	if refuseOn {
		refuse = "on (arms at the first matched play webhook after this start)"
	}
	warn := "warning: off"
	if warnDays > 0 {
		warn = fmt.Sprintf("warn after %d days", warnDays)
	}
	return fmt.Sprintf("[Webhook] last matched play webhook: %s; played-file protection: %s; reader budget refusal: %s; %s; %s",
		last, prot, refuse, warn, keepalive)
}

// startWebhookStatus logs the startup line, evaluates once and then every hour until stop
// closes. Called after the state database block, so the stored times are loaded.
func startWebhookStatus(m *webhookMarks, stop <-chan struct{}) {
	refuse := false
	if c := gc(); c != nil {
		refuse = c.RefuseUnconfirmedReads
	}
	days := webhookWarnDays()
	minutes, maxTorrents := keepaliveSettings()
	m.logf("%s", webhookStartupLine(m.now(), m.matched.at.Load(), fileStates.PlayRecorded(), refuse, days,
		keepaliveStartupField(minutes, maxTorrents, m.matched.at.Load() > 0)))
	m.evaluate(days)
	go func() {
		t := time.NewTicker(webhookEvaluateInterval)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				m.evaluate(webhookWarnDays())
			}
		}
	}()
}

// collectWebhookHealthProm writes the webhook-health series: the last times (0 = never)
// and the advisory warning as a 0 or 1 gauge, the same predicate as /api/health.
func collectWebhookHealthProm(w *promtext.Writer) {
	m := webhookStatus
	w.Gauge("flodni_playback_webhook_last_timestamp_seconds",
		"Unix time of the last playback start or resume webhook for a movie or an episode, matched to one file or not. 0 = none seen. Persisted hourly with the state database.",
		promSample(m.matched.at.Load(), lbl("matched", "true")),
		promSample(m.unmatched.at.Load(), lbl("matched", "false")))
	w.Gauge("flodni_unconfirmed_read_last_session_timestamp_seconds",
		"Unix time of the last session that crossed the unconfirmed-read margin and closed unconfirmed. 0 = none seen.",
		promSample(m.unconfirmed.at.Load()))
	var warn int64
	if m.warning(webhookWarnDays()) {
		warn = 1
	}
	w.Gauge("flodni_warning",
		"1 while an advisory warning holds. no_play_webhook: readers closed unconfirmed in the last playback_webhook_warn_days and no play webhook matched a file in that time. Never affects /api/health status.",
		promSample(warn, lbl("name", warningNoPlayWebhook)))
}
