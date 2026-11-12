package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"flodni/internal/warmup"
)

// Reader budget, phase a: measurement only. A file that no media server has confirmed
// as playing can still be read end to end by a scanner, a hasher or a copy. This file
// counts what such a reader takes past the head and tail windows (which the warmup
// cache serves anyway) and logs a session once when it crosses a margin. Nothing here
// refuses or slows a read; overUnconfirmedMargin is the predicate phase b will call,
// and no caller uses it for enforcement.
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

	// Movie and episode play/resume webhooks, by whether they resolved to one file. Phase b's
	// guard is designed to arm on the matched count (not built).
	playbackWebhooksMatched   atomic.Int64
	playbackWebhooksUnmatched atomic.Int64
)

func countPlaybackWebhook(matched bool) {
	if matched {
		playbackWebhooksMatched.Add(1)
	} else {
		playbackWebhooksUnmatched.Add(1)
	}
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
	out := bytesOutsideWindows(off, int64(n), s.size, warmup.FileSize, warmup.TailWarmupSize)
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
		s.logReaderBudget("over_margin")
		// A read that passed the closed check can cross after the close resolved.
		if s.closed.Load() {
			s.resolveReaderBudget()
		}
	}
}

// overUnconfirmedMargin reports whether this session has taken at least the margin past
// its windows and the file is still unconfirmed. This is the predicate phase b would
// call before a fetch; phase a has no caller that refuses anything.
func (s *TTFFSession) overUnconfirmedMargin() bool {
	return s.overMarginAt.Load() != 0 && !playbackConfirmed(s.path)
}

// noteConfirmedLate is called when a webhook matched path as playing. If its session
// had already crossed the margin, that crossing was a playback the budget would have
// cut: log it once. The session counter follows at close.
func noteConfirmedLate(path string) {
	v, ok := sessions.Load(path)
	if !ok {
		return
	}
	s := v.(*TTFFSession)
	if s.overMarginAt.Load() == 0 || s.closed.Load() || s.budgetResolved.Load() {
		return
	}
	if s.confirmedLateAt.CompareAndSwap(0, time.Now().UnixNano()) {
		s.logReaderBudget("confirmed_late")
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
}

// logReaderBudget writes one line with no path or file name: the hash and file index
// identify the file, as in the [Session] line. comm is read here, not per read.
func (s *TTFFSession) logReaderBudget(event string) {
	rec := readerBudgetRecord{
		Event: event, Hash: s.hash, FileIndex: s.fileIndex, Size: s.size,
		SwarmBytes: s.unconfirmedSwarm.Load(), CacheBytes: s.unconfirmedCache.Load(),
	}
	if event == "confirmed_late" {
		late := max((s.confirmedLateAt.Load()-s.overMarginAt.Load())/int64(time.Millisecond), 0)
		rec.LateMS = &late
	} else {
		margin := unconfirmedMargin()
		age := max(time.Since(time.Unix(0, s.openedAt.Load())).Milliseconds(), 0)
		comm, pid, uid, enforced := readerComm(s.callerPid.Load()), s.callerPid.Load(), s.callerUID.Load(), false
		rec.Margin, rec.OpenAgeMS = &margin, &age
		rec.Comm, rec.Pid, rec.UID, rec.Enforced = &comm, &pid, &uid, &enforced
	}
	line, err := json.Marshal(rec)
	if err != nil {
		logger.Printf("WARNING: reader budget line for %s not written: %v", s.hash, err)
		return
	}
	logger.Printf("%s%s", readerBudgetPrefix, line)
}
