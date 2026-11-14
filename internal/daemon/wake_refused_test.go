package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"flodni/internal/gostorm/native"
	"flodni/internal/metadb"
	"flodni/internal/vfs"

	"github.com/hanwen/go-fuse/v2/fuse"
)

// A strict wake refuses a torrent the engine does not store (native.ErrNotStored).
// That is not a swarm failing: the read answers EIO at once, fetches nothing, marks
// no failed read, leaves the readability record's streak alone and can never become
// a dead-swarm verdict. The handle latches the refusal, so a second read makes no
// engine call.

const notStoredPaths = 5 // library paths whose torrent the engine refuses

func refusingEngine(t *testing.T) *fakeEngine {
	t.Helper()
	fe := useEngineReadPath(t)
	fe.wakeFn = func(context.Context, string, int) error {
		return fmt.Errorf("add torrent: %w", native.ErrNotStored)
	}
	return fe
}

// refusedEnv is a refusing engine, the readability recorder on a real database and
// notStoredPaths sessions, each with its own handle; the first one is read.
func refusedEnv(t *testing.T, slot bool) (fe *fakeEngine, db *metadb.DB, s *TTFFSession, h *MkvHandle, hs []*MkvHandle) {
	t.Helper()
	fe = refusingEngine(t)
	db = readabilityEnv(t, 0, false) // torrentLive false: every read on a miss wakes
	// Registered after readabilityEnv, so it runs before that restores the logger and
	// the other globals the background WarmLanding prefetch still reads.
	t.Cleanup(func() { settled(t) })
	for i := range notStoredPaths {
		path := fmt.Sprintf("/m/gone%d.mkv", i)
		sess := &TTFFSession{path: path, size: 1 << 30, hash: rHash}
		sess.openedAt.Store(time.Now().Add(-time.Minute).UnixNano())
		sess.lastOff.Store(-1)
		sessions.Store(path, sess)
		hh := engineHandle(path, 1<<30)
		hh.hash = rHash
		hh.magnet, hh.wake = "magnet:?xt=urn:btih:"+rHash, fe.Wake
		hs = append(hs, hh)
		if i == 0 {
			s, h = sess, hh
		}
	}
	if slot {
		// A handle that held a pump slot: the refusal must not need another.
		masterDataSemaphore <- struct{}{}
		t.Cleanup(func() { <-masterDataSemaphore })
		h.hasSlot.Store(true)
	}
	t.Cleanup(func() {
		for _, hh := range hs {
			sessions.Delete(hh.path)
		}
	})
	return fe, db, s, h, hs
}

// settled waits for the background prefetches to end. A new handle's first read past
// the head starts one (WarmLanding) before the wake is tried, so it is the one engine
// call a refused first read can still make; it adds nothing to the engine.
func settled(t *testing.T) {
	t.Helper()
	waitUntil(t, "background prefetches to end", func() bool {
		idle := true
		app.inFlightPrefetches.Range(func(_, _ any) bool { idle = false; return false })
		return idle
	})
}

func TestNotStoredReadAnswersEIOAndIsNotADeadSwarm(t *testing.T) {
	newTestApp(t)
	for _, slot := range []bool{false, true} {
		t.Run(fmt.Sprintf("slot=%v", slot), func(t *testing.T) {
			fe, db, s, h, _ := refusedEnv(t, slot)
			verdicts := deadSwarmVerdictCounts()
			refused := wakeRefusedCount.Load()

			res, errno := h.Read(context.Background(), make([]byte, 256*1024), wireOff)
			if res != nil || errno != syscall.EIO {
				t.Fatalf("Read = (%v, %v), want (nil, EIO)", res, errno)
			}
			settled(t)
			if n := len(fe.aheadCalls()); n != 0 {
				t.Errorf("FetchAhead calls = %d, want none", n)
			}
			blocks := len(fe.blockCalls())
			if fe.wakeCount() != 1 {
				t.Errorf("wakes = %d, want 1", fe.wakeCount())
			}
			if s.readFailed.Load() || s.failedReads.Load() != 0 {
				t.Errorf("session marked a failed read (readFailed=%v failedReads=%d)", s.readFailed.Load(), s.failedReads.Load())
			}

			// The second read makes no engine call at all.
			if res, errno := h.Read(context.Background(), make([]byte, 256*1024), wireOff+4*mb); res != nil || errno != syscall.EIO {
				t.Fatalf("second Read = (%v, %v), want (nil, EIO)", res, errno)
			}
			if fe.wakeCount() != 1 {
				t.Errorf("wakes after the second read = %d, want 1", fe.wakeCount())
			}
			settled(t)
			if n := len(fe.aheadCalls()); n != 0 {
				t.Errorf("FetchAhead calls after the second read = %d, want none", n)
			}
			if n := len(fe.blockCalls()); n != blocks {
				t.Errorf("FetchBlock calls rose from %d to %d on the second read", blocks, n)
			}
			if got := wakeRefusedCount.Load() - refused; got != 1 {
				t.Errorf("flodni_wake_refused_total rose by %d, want 1 (one per handle)", got)
			}

			// Closing the session condemns nothing, and the record keeps its own reason.
			s.closeSession()
			for k, v := range deadSwarmVerdictCounts() {
				if v != verdicts[k] {
					t.Errorf("dead-swarm verdict %v rose to %d", k, v)
				}
			}
			rows := records(t, db)
			if len(rows) != 1 || rows[0].Hash != rHash {
				t.Fatalf("records = %+v, want one for %s", rows, rHash)
			}
			r := rows[0]
			if r.FailCount != 0 || r.FirstFail != 0 || r.LastSwarmRead != 0 {
				t.Errorf("record = %+v: a refusal must not count as a failed swarm read", r)
			}
			if r.LastReason != "not_stored" || r.LastSource != failSourceMount || r.LastFail == 0 {
				t.Errorf("record = %+v, want last reason not_stored from the mount", r)
			}
		})
	}
}

// A different handle on another path whose torrent is stored is untouched by the
// refusal of the first.
func TestNotStoredLatchIsPerHandle(t *testing.T) {
	_, _, _, h, hs := refusedEnv(t, false)
	if _, errno := h.readInner(context.Background(), make([]byte, 256*1024), wireOff); errno != errnoWakeRefused {
		t.Fatalf("readInner = %v, want errnoWakeRefused", errno)
	}
	if !h.wakeRefused.Load() {
		t.Fatal("the refusal was not latched")
	}
	for _, other := range hs[1:] {
		if other.wakeRefused.Load() {
			t.Fatalf("handle %s latched by another handle's refusal", other.path)
		}
	}
}

// A refusal met by the short-read repair is EIO too, and the repair stops there.
func TestNotStoredSubReadIsEIO(t *testing.T) {
	_, _, _, h, _ := refusedEnv(t, false)
	calls := 0
	readInnerCall = func(_ *MkvHandle, _ context.Context, dest []byte, _ int64) (fuse.ReadResult, syscall.Errno) {
		calls++
		if calls == 1 {
			return fuse.ReadResultData(dest[:10]), 0 // short away from EOF: the repair runs
		}
		return nil, errnoWakeRefused
	}
	t.Cleanup(func() { readInnerCall = (*MkvHandle).readInner })
	if res, errno := h.Read(context.Background(), make([]byte, 4096), 0); res != nil || errno != syscall.EIO {
		t.Fatalf("Read = (%v, %v), want (nil, EIO)", res, errno)
	}
	if calls != 2 {
		t.Errorf("readInner calls = %d, want 2 (no further repair)", calls)
	}
}

func TestOpenOfANotStoredTorrent(t *testing.T) {
	fe := refusingEngine(t)
	readabilityEnv(t, 0, false)
	dir := t.TempDir()
	url := "http://127.0.0.1:8090/stream?link=" + rHash + "&index=1&play"
	node := func(path string) *VirtualMkvNode {
		return &VirtualMkvNode{vMeta: &vfs.Metadata{URL: url, Path: path, Size: 1 << 20}, wake: fe.Wake}
	}
	stub := filepath.Join(dir, "there.mkv")
	if err := os.WriteFile(stub, []byte("stub"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := wakeRefusedCount.Load()

	if _, _, errno := node(stub).Open(context.Background(), 0); errno != syscall.EIO {
		t.Errorf("Open(stub on disk) = %v, want EIO", errno)
	}
	if _, _, errno := node(filepath.Join(dir, "gone.mkv")).Open(context.Background(), 0); errno != syscall.ENOENT {
		t.Errorf("Open(stub gone) = %v, want ENOENT", errno)
	}
	if got := wakeRefusedCount.Load() - before; got != 2 {
		t.Errorf("flodni_wake_refused_total rose by %d, want 2", got)
	}
}

// Any other wake failure (the engine busy, not connected) is not a refusal and is not
// latched. With the torrent still not running, the wake is stored-only, so its failure
// says nothing about the swarm: the read answers EIO with no fetch, no failed-read mark
// and no dead-swarm verdict.
func TestOtherWakeErrorsAreNotLatchedAndNotASwarmFailure(t *testing.T) {
	newTestApp(t)
	fe, db, s, h, _ := refusedEnv(t, false)
	fe.wakeFn = func(context.Context, string, int) error { return errors.New("wake semaphore exhausted (system busy)") }
	verdicts := deadSwarmVerdictCounts()

	res, errno := h.Read(context.Background(), make([]byte, 256*1024), wireOff)
	if res != nil || errno != syscall.EIO {
		t.Fatalf("Read = (%v, %v), want (nil, EIO)", res, errno)
	}
	settled(t)
	if h.wakeRefused.Load() {
		t.Fatal("a busy engine was latched as a refusal")
	}
	if n := len(fe.aheadCalls()); n != 0 {
		t.Errorf("FetchAhead calls = %d, want none", n)
	}
	if s.readFailed.Load() || s.failedReads.Load() != 0 {
		t.Errorf("session marked a failed read (readFailed=%v failedReads=%d)", s.readFailed.Load(), s.failedReads.Load())
	}
	s.closeSession()
	for k, v := range deadSwarmVerdictCounts() {
		if v != verdicts[k] {
			t.Errorf("dead-swarm verdict %v rose to %d", k, v)
		}
	}
	if rows := records(t, db); len(rows) != 0 {
		t.Errorf("records = %+v, want none", rows)
	}
}
