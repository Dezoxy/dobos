package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"flodni/internal/warmup"
)

// Reader budget. A file that no media server has confirmed as playing can still be read
// end to end by a scanner, a hasher or a copy. This file counts what such a reader takes
// past the head and tail windows (which the warmup cache serves anyway) and logs a
// session once when it crosses a margin (phase a, measuring).
//
// Phase b, opt-in refusal: the predicate (budgetBlocksFetch), the per-handle hold
// (budgetGate), the metrics and the log lines. handle.go asks the gate at the first cache
// miss and the predicate at its four prefetch sites; pump.go parks on the predicate. With
// refuse_unconfirmed_reads off (the default) nothing here refuses or slows a read.
//
// The Prometheus counter is what the reader took past the windows, whichever path
// served it: outside the exempt windows every such byte is swarm cost, even when it
// was filled earlier. With the pump on, the engine fill happened through the pump or a
// prefetch, which is not attributed to the reader (not built; revisited after
// Reliability step 6). The log line splits the same bytes by the read path that
// answered, for debugging: swarm_bytes waited for the engine (srcFetchBlock, which can
// answer from pieces it already holds), cache_bytes came from the read-ahead cache
// (srcRACacheHit). Neither is a count of bytes downloaded.

const readerBudgetPrefix = "[ReaderBudget] "

// Indexes into the global counters.
const (
	rbUnconfirmed   = 0
	rbConfirmedLate = 1
)

var (
	// unconfirmedMargin is a var so tests can change it.
	unconfirmedMargin = func() int64 {
		if c := gc(); c != nil {
			return c.UnconfirmedReadMargin
		}
		return 64 << 20
	}

	// Since process start, for /metrics/prometheus.
	unconfirmedReadBytes atomic.Int64
	unconfirmedSessions  [2]atomic.Int64 // by rbUnconfirmed, rbConfirmedLate; resolved at close

	// Movie and episode play/resume webhooks, by whether they resolved to one file. The
	// gate arms on the matched count: refusal starts only once a webhook has matched a
	// file since the process started, so a media server that never sends one is never
	// cut off.
	playbackWebhooksMatched   atomic.Int64
	playbackWebhooksUnmatched atomic.Int64

	// refuseUnconfirmedReads is the opt-in switch, read at every call. A var so tests
	// can flip it.
	refuseUnconfirmedReads = func() bool {
		c := gc()
		return c != nil && c.RefuseUnconfirmedReads
	}

	// unconfirmedReadHold is how long the first refused read of a handle waits for a
	// webhook before it is refused; unconfirmedHoldPoll is how often the wait re-asks.
	// Vars so tests can shorten them; not config keys.
	unconfirmedReadHold = 15 * time.Second
	unconfirmedHoldPoll = 100 * time.Millisecond

	// Since process start, for /metrics/prometheus.
	unconfirmedRefusedReads          atomic.Int64
	unconfirmedHolds                 [2]atomic.Int64 // by rbHoldLifted, rbHoldRefused
	unconfirmedConfirmedAfterRefusal atomic.Int64    // sessions, once each
)

// Indexes into unconfirmedHolds.
const (
	rbHoldLifted  = 0
	rbHoldRefused = 1
)

// errnoBudgetRefused is what budgetGate returns for a refusal. It is private: MkvHandle.Read
// must map it to EIO before the kernel sees it (and before it reaches ttffReadFailed or the
// short-read repair), because a refusal is not the swarm failing to deliver. EDQUOT is a
// valid errno, so a leak would still be an answer the kernel can complete a request with.
// MkvHandle.Read maps it at both readInner call sites.
const errnoBudgetRefused = syscall.EDQUOT

// The rule that resolved a play or resume webhook, in the order they are tried.
const (
	ruleBasename = iota
	rulePathMap
	ruleIMDB
	ruleFuzzy
	ruleNone
	numWebhookRules
)

// webhookRuleNames are the values of the rule label of
// flodni_playback_webhook_match_rule_total, indexed by the constants above.
var webhookRuleNames = [numWebhookRules]string{"basename", "path_map", "imdb", "fuzzy", "none"}

// playbackWebhookRules counts play and resume webhooks by the rule that resolved them,
// since process start.
var playbackWebhookRules [numWebhookRules]atomic.Int64

func countPlaybackWebhook(matched bool) {
	if matched {
		playbackWebhooksMatched.Add(1)
	} else {
		playbackWebhooksUnmatched.Add(1)
	}
}

// tailWindowLen is the length of the tail window of a file of this size, from the
// page-aligned start every window user shares (warmup.TailStart).
func tailWindowLen(size int64) int64 {
	if size <= 0 {
		return 0
	}
	return size - warmup.TailStart(size)
}

// bytesOutsideWindows is how many bytes of the read [off, off+n) fall outside the head
// window [0, head) and the tail window [size-tail, size). Windows are clamped to the
// file, and where they overlap (a short file) nothing is outside.
func bytesOutsideWindows(off, n, size, head, tail int64) int64 {
	if n <= 0 || size <= 0 {
		return 0
	}
	midStart := min(head, size)
	midEnd := max(size-tail, 0)
	lo, hi := max(off, midStart), min(off+n, midEnd)
	if hi <= lo {
		return 0
	}
	return hi - lo
}

// countUnconfirmed adds one served read to the session's tally when the engine or the
// read-ahead cache answered it, it lies past the windows, and the file has no confirmed
// playback. Called from recordRead, the funnel every served read passes.
func (s *TTFFSession) countUnconfirmed(src ttffSource, n int, off int64) {
	if src != srcFetchBlock && src != srcRACacheHit {
		return
	}
	out := bytesOutsideWindows(off, int64(n), s.size, warmup.FileSize, tailWindowLen(s.size))
	if out == 0 || playbackConfirmed(s.path) {
		return
	}
	unconfirmedReadBytes.Add(out)
	if src == srcFetchBlock {
		s.unconfirmedSwarm.Add(out)
	} else {
		s.unconfirmedCache.Add(out)
	}
	if s.overMarginAt.Load() == 0 && s.unconfirmedSwarm.Load()+s.unconfirmedCache.Load() >= unconfirmedMargin() &&
		s.overMarginAt.CompareAndSwap(0, time.Now().UnixNano()) {
		s.logReaderBudget("over_margin", nil, 0)
		// A read that passed the closed check can cross after the close resolved.
		if s.closed.Load() {
			s.resolveReaderBudget()
		}
	}
}

// pastMargin reports whether the session has taken at least the margin past its windows
// since the last confirmation (the tally less budgetFloor). With margin 0 that holds from
// the start.
func (s *TTFFSession) pastMargin() bool {
	return s.unconfirmedSwarm.Load()+s.unconfirmedCache.Load()-s.budgetFloor.Load() >= unconfirmedMargin()
}

// overUnconfirmedMargin reports whether this session has taken at least the margin past
// its windows since the last confirmation and the file is unconfirmed now.
func (s *TTFFSession) overUnconfirmedMargin() bool {
	return s.pastMargin() && !playbackConfirmed(s.path)
}

// refusalEnforced reports whether the gate would refuse now: the switch is on and a play
// webhook has matched a file since start.
func refusalEnforced() bool {
	return refuseUnconfirmedReads() && playbackWebhooksMatched.Load() > 0
}

// blocks asks the predicate for one session: would a fetch for [off, off+n) of a file of
// size be refused? Cheapest checks first; the switch, the arming and the session lookup
// are the caller's.
func (s *TTFFSession) blocks(off, n, size int64) bool {
	if !s.pastMargin() {
		return false
	}
	if bytesOutsideWindows(off, n, size, warmup.FileSize, tailWindowLen(size)) == 0 {
		return false
	}
	return !playbackConfirmed(s.path)
}

// enforcedSession returns the session behind path while refusal is enforced, else nil.
func enforcedSession(path string) *TTFFSession {
	if !refusalEnforced() {
		return nil
	}
	if v, ok := sessions.Load(path); ok {
		return v.(*TTFFSession)
	}
	return nil
}

// budgetBlocksFetch is the refusal predicate: true when a fetch for [off, off+n) of path
// must not start. The switch is on and a play webhook has matched since start, the path
// has a session, the session is past its margin since the last confirmation, some
// requested byte lies outside the windows, and the file has no confirmed playback. Bytes
// already cached are the caller's to serve; the windows are exempt by offset.
func budgetBlocksFetch(path string, off, n, size int64) bool {
	s := enforcedSession(path)
	return s != nil && s.blocks(off, n, size)
}

// budgetHold is one handle's wait for a webhook. The first refused read starts it; reads
// that arrive meanwhile share its deadline; when it runs out every later read on the
// handle is refused at once. A confirmation (or the predicate letting go for any other
// reason) re-arms it. The zero value is ready.
type budgetHold struct {
	mu       sync.Mutex
	deadline time.Time // zero = no hold running
	expired  bool
	held     atomic.Bool // deadline or expired is set; lets rearm skip the lock
}

// begin returns the running hold's deadline, starting a hold when none runs. expired
// reports a hold that already ran out: the read is refused at once.
func (b *budgetHold) begin(now time.Time) (deadline time.Time, expired bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.expired {
		return time.Time{}, true
	}
	if b.deadline.IsZero() {
		b.deadline = now.Add(unconfirmedReadHold)
		b.held.Store(true)
	}
	return b.deadline, false
}

// lift ends a running hold; true for the one caller that ended it (it counts the lift).
func (b *budgetHold) lift() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	had := !b.deadline.IsZero() && !b.expired
	b.deadline, b.expired = time.Time{}, false
	b.held.Store(false)
	return had
}

// expire marks the hold run out; true for the one caller that did (it counts the refusal).
func (b *budgetHold) expire() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	first := !b.expired
	b.expired = true
	return first
}

// rearm forgets a hold without counting it (a gate call that finds nothing to refuse);
// cheap when nothing is held.
func (b *budgetHold) rearm() {
	if !b.held.Load() {
		return
	}
	b.lift()
}

// budgetGate is asked before a fetch for [off, off+n) starts. It returns 0 when the fetch
// may go ahead and errnoBudgetRefused when it must not (the caller maps that to EIO).
// A refused read is first held, once per handle, for unconfirmedReadHold: a webhook that
// arrives late confirms the file and lets the read through, so a play the media server
// confirmed after the margin is not cut. The hold polls the predicate; EINTR when ctx
// ends first. After the hold runs out, reads on the handle are refused at once, until a
// confirmation, a fresh margin or the switch going off re-arms it (a read inside a
// window neither starts nor re-arms a hold). The hold is tied to the session it started under: if that
// session closes, the hold is refused, not lifted (a closed session no longer has a
// tally to compare with). Each refusal and the hold's start refresh the session's
// lastReadAt, so a refused session is not idle-closed under a handle that is still
// open. Refusals never reach ttffReadFailed.
func (h *MkvHandle) budgetGate(ctx context.Context, off int64, n int) syscall.Errno {
	s := enforcedSession(h.path)
	if s == nil || !s.pastMargin() || playbackConfirmed(h.path) {
		h.budgetHold.rearm()
		return 0
	}
	if !s.blocks(off, int64(n), h.size) {
		return 0 // inside the windows: always served, hold untouched
	}
	start := time.Now()
	s.lastReadAt.Store(start.UnixNano())
	deadline, expired := h.budgetHold.begin(start)
	if expired {
		return s.refuse(off, 0)
	}
	tick := time.NewTicker(unconfirmedHoldPoll)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return syscall.EINTR
		case <-tick.C:
		}
		if s.closed.Load() {
			if h.budgetHold.expire() {
				unconfirmedHolds[rbHoldRefused].Add(1)
			}
			return s.refuse(off, time.Since(start))
		}
		if !refusalEnforced() || !s.blocks(off, int64(n), h.size) {
			if h.budgetHold.lift() {
				unconfirmedHolds[rbHoldLifted].Add(1)
			}
			return 0
		}
		if !time.Now().Before(deadline) {
			if h.budgetHold.expire() {
				unconfirmedHolds[rbHoldRefused].Add(1)
			}
			return s.refuse(off, time.Since(start))
		}
	}
}

// refuse accounts one refused read of the session and returns the refusal errno. The
// first refusal of a session writes the "refused" line.
func (s *TTFFSession) refuse(off int64, held time.Duration) syscall.Errno {
	s.lastReadAt.Store(time.Now().UnixNano())
	unconfirmedRefusedReads.Add(1)
	if s.refusedReads.Add(1) == 1 {
		s.logReaderBudget("refused", &off, held.Milliseconds())
	}
	return errnoBudgetRefused
}

// noteConfirmedLate is called when a webhook matched path as playing. It starts the
// session's refusal tally afresh (budgetFloor) and counts a session that had been refused
// once as confirmed after refusal. If the session had already crossed the margin, that crossing was a playback the budget would have
// cut: log it once. The session counter follows at close.
func noteConfirmedLate(path string) {
	v, ok := sessions.Load(path)
	if !ok {
		return
	}
	s := v.(*TTFFSession)
	s.confirmed.Store(true)
	// Before the early returns: a refusal needs the fresh start whether or not the
	// session ever crossed in phase a's sense.
	s.budgetFloor.Store(s.unconfirmedSwarm.Load() + s.unconfirmedCache.Load())
	if s.refusedReads.Load() > 0 && s.confirmedAfterRefusal.CompareAndSwap(false, true) {
		unconfirmedConfirmedAfterRefusal.Add(1)
	}
	if s.overMarginAt.Load() == 0 || s.closed.Load() || s.budgetResolved.Load() {
		return
	}
	if s.confirmedLateAt.CompareAndSwap(0, time.Now().UnixNano()) {
		s.logReaderBudget("confirmed_late", nil, 0)
	}
}

// resolveReaderBudget counts a session that crossed the margin exactly once, at close,
// as confirmed_late when a webhook confirmed it afterwards and unconfirmed otherwise.
// closeSession calls it inside its closeOnce.
func (s *TTFFSession) resolveReaderBudget() {
	if s.overMarginAt.Load() == 0 || !s.budgetResolved.CompareAndSwap(false, true) {
		return
	}
	if s.confirmedLateAt.Load() != 0 {
		unconfirmedSessions[rbConfirmedLate].Add(1)
	} else {
		unconfirmedSessions[rbUnconfirmed].Add(1)
		// The comm was read at the crossing (logReaderBudget): the caller may be gone by now.
		comm := ""
		if p := s.overMarginComm.Load(); p != nil {
			comm = *p
		}
		webhookStatus.noteUnconfirmedRead(comm)
	}
}

// noteReaderCaller records who opened path, from the FUSE request context. It is the
// latest Open on the path, not necessarily the process that reads.
func noteReaderCaller(path string, pid, uid uint32) {
	if v, ok := sessions.Load(path); ok {
		s := v.(*TTFFSession)
		s.callerPid.Store(pid)
		s.callerUID.Store(uid)
	}
}

// readerComm is the process name of pid, or "" when it cannot be read (no pid, a
// process in another PID namespace, or one that already exited).
func readerComm(pid uint32) string {
	if pid == 0 {
		return ""
	}
	b, err := os.ReadFile(filepath.Join("/proc", strconv.FormatUint(uint64(pid), 10), "comm"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

type readerBudgetRecord struct {
	Event      string  `json:"event"`
	Hash       string  `json:"hash"`
	FileIndex  int     `json:"file_index"`
	Size       int64   `json:"size"`
	SwarmBytes int64   `json:"swarm_bytes"`
	CacheBytes int64   `json:"cache_bytes"`
	Margin     *int64  `json:"margin,omitempty"`
	OpenAgeMS  *int64  `json:"open_age_ms,omitempty"`
	Comm       *string `json:"comm,omitempty"`
	Pid        *uint32 `json:"pid,omitempty"`
	UID        *uint32 `json:"uid,omitempty"`
	Enforced   *bool   `json:"enforced,omitempty"`
	LateMS     *int64  `json:"late_ms,omitempty"`
	// refused event: the offset of the first refused read and how long it was held.
	Offset *int64 `json:"offset,omitempty"`
	HeldMS *int64 `json:"held_ms,omitempty"`
	// confirmed_late event: reads the gate refused before the confirmation.
	RefusedReads *int64 `json:"refused_reads,omitempty"`
}

// logReaderBudget writes one line with no path or file name: the hash and file index
// identify the file, as in the [Session] line. comm is read here, not per read.
//
// off and heldMS are set for the "refused" event only.
func (s *TTFFSession) logReaderBudget(event string, off *int64, heldMS int64) {
	rec := readerBudgetRecord{
		Event: event, Hash: s.hash, FileIndex: s.fileIndex, Size: s.size,
		SwarmBytes: s.unconfirmedSwarm.Load(), CacheBytes: s.unconfirmedCache.Load(),
	}
	if event == "confirmed_late" {
		late := max((s.confirmedLateAt.Load()-s.overMarginAt.Load())/int64(time.Millisecond), 0)
		refused := s.refusedReads.Load()
		rec.LateMS, rec.RefusedReads = &late, &refused
	} else {
		margin := unconfirmedMargin()
		age := max(time.Since(time.Unix(0, s.openedAt.Load())).Milliseconds(), 0)
		comm, pid, uid, enforced := readerComm(s.callerPid.Load()), s.callerPid.Load(), s.callerUID.Load(), refusalEnforced()
		rec.Margin, rec.OpenAgeMS = &margin, &age
		rec.Comm, rec.Pid, rec.UID, rec.Enforced = &comm, &pid, &uid, &enforced
		if event == "over_margin" {
			s.overMarginComm.Store(&comm)
		}
		if event == "refused" {
			rec.Offset, rec.HeldMS = off, &heldMS
		}
	}
	line, err := json.Marshal(rec)
	if err != nil {
		logger.Printf("WARNING: reader budget line for %s not written: %v", s.hash, err)
		return
	}
	logger.Printf("%s%s", readerBudgetPrefix, line)
}
