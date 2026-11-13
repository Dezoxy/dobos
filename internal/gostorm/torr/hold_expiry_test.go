package torr

import (
	"testing"
	"time"

	"flodni/internal/gostorm/torr/state"
	"flodni/internal/gostorm/torr/storage/torrstor"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
)

// The expiry decision: a torrent whose timer passed, with a cache and no reader,
// expires unless the injected hold predicate keeps it. The predicate is asked last and
// only then.
func TestExpiryVerdictTable(t *testing.T) {
	yes := func() bool { return true }
	no := func() bool { return false }
	for _, c := range []struct {
		name        string
		stat        state.TorrentStat
		timerPassed bool
		hasCache    bool
		readers     int
		held        func() bool
		want        bool
	}{
		{"idle, not held", state.TorrentWorking, true, true, 0, no, true},
		{"idle, held", state.TorrentWorking, true, true, 0, yes, false},
		{"idle, nil predicate", state.TorrentWorking, true, true, 0, nil, true},
		{"no cache expires even if held", state.TorrentWorking, true, false, 0, yes, true},
		{"getting info never expires", state.TorrentGettingInfo, true, true, 0, no, false},
		{"preload never expires", state.TorrentPreload, true, true, 0, no, false},
		{"timer not passed", state.TorrentWorking, false, true, 0, no, false},
		{"reader open", state.TorrentWorking, true, true, 1, no, false},
	} {
		if got := expiryVerdict(c.stat, c.timerPassed, c.hasCache, c.readers, c.held); got != c.want {
			t.Errorf("%s: expire = %v, want %v", c.name, got, c.want)
		}
	}
	// The predicate is not asked unless the torrent would otherwise expire.
	asked := false
	spy := func() bool { asked = true; return true }
	expiryVerdict(state.TorrentWorking, false, true, 0, spy)
	expiryVerdict(state.TorrentWorking, true, true, 2, spy)
	expiryVerdict(state.TorrentGettingInfo, true, true, 0, spy)
	if asked {
		t.Error("the hold predicate ran for a torrent that was not about to expire")
	}
}

func setHoldExpiry(t *testing.T, f func(string) bool) {
	t.Helper()
	prev := HoldExpiry
	HoldExpiry = f
	t.Cleanup(func() { HoldExpiry = prev })
}

func heldFixture(t *testing.T) *Torrent {
	t.Helper()
	tr := idleFixture(t)
	tr.cache = &torrstor.Cache{}
	tr.Stat = state.TorrentWorking
	return tr
}

// expired() asks the predicate with the torrent's hash; IsPriority no longer holds a
// torrent by itself, so the ceiling and the cap are enforceable.
func TestExpiredAsksTheHoldPredicate(t *testing.T) {
	tr := heldFixture(t)
	var got []string
	setHoldExpiry(t, func(h string) bool { got = append(got, h); return true })
	if tr.expired() {
		t.Fatal("held torrent expired")
	}
	if len(got) != 1 || got[0] != idleHookHash {
		t.Fatalf("predicate calls = %v, want one for %s", got, idleHookHash)
	}

	tr.IsPriority.Store(true)
	setHoldExpiry(t, func(string) bool { return false })
	if !tr.expired() {
		t.Fatal("IsPriority alone must not hold a torrent past its idle timeout")
	}
	setHoldExpiry(t, nil)
	if !tr.expired() {
		t.Fatal("nil predicate: the torrent must expire")
	}
}

// A panicking predicate means no hold, and muTorrent is not left locked.
func TestExpiredRecoversFromAPanickingPredicate(t *testing.T) {
	tr := heldFixture(t)
	setHoldExpiry(t, func(string) bool { panic("boom") })
	if !tr.expired() {
		t.Fatal("a panicking predicate must default to no hold")
	}
}

// The ticker's UpdateStats on a held idle torrent keeps its stats moving: speed back to
// zero, not streaming, no close, no idle-close hook.
func TestUpdateStatsKeepsRunningWhileHeld(t *testing.T) {
	tr := heldFixture(t)
	tr.DownloadSpeed = 5e6
	tr.UploadSpeed = 1e6
	before := time.Now().Add(-time.Minute)
	tr.lastTimeSpeed = before
	// The predicate drops the cache after answering, under the lock, so the ticker's
	// trailing read-ahead adjustment (a goroutine) has nothing to run against.
	setHoldExpiry(t, func(string) bool { tr.cache = nil; return true })
	closes := 0
	setOnIdleClose(t, func(string) { closes++ })

	tr.UpdateStats()

	if closes != 0 {
		t.Fatal("a held torrent was closed")
	}
	if _, ok := tr.bt.torrents[metainfo.NewHashFromHex(idleHookHash)]; !ok {
		t.Fatal("a held torrent left the registry")
	}
	if tr.DownloadSpeed != 0 || tr.UploadSpeed != 0 {
		t.Fatalf("speeds frozen on a held idle torrent: %v / %v", tr.DownloadSpeed, tr.UploadSpeed)
	}
	if !tr.lastTimeSpeed.After(before) {
		t.Fatal("stats block did not run: lastTimeSpeed unchanged")
	}
	if tr.IsStreaming() {
		t.Fatal("a held idle torrent reports as streaming")
	}
}

// The predicate runs under muTorrent: a call back into the wrapper that takes the lock
// would deadlock, so the wrapper must not hand it anything that does. Pinned by
// running expired() with the lock held and a predicate that touches no engine state.
func TestHoldPredicateRunsUnderTheLockWithoutEngineAccess(t *testing.T) {
	tr := heldFixture(t)
	tr.TorrentSpec = &torrent.TorrentSpec{InfoHash: metainfo.NewHashFromHex(idleHookHash)}
	tr.muTorrent.Lock()
	setHoldExpiry(t, func(string) bool { return true })
	held := !tr.expired()
	tr.muTorrent.Unlock()
	if !held {
		t.Fatal("not held")
	}
}
