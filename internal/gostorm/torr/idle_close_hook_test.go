package torr

import (
	"sync"
	"testing"
	"time"

	sets "flodni/internal/gostorm/settings"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
)

const idleHookHash = "89abcdef0123456789abcdef0123456789abcdef"

// idleFixture registers one wrapper in an empty BTServer, with its expiry time
// already past and no cache, so the ticker's UpdateStats closes it.
func idleFixture(t *testing.T) *Torrent {
	t.Helper()
	oldPath, oldRO := sets.Path, sets.ReadOnly
	sets.Path = t.TempDir()
	sets.InitSets(false, false)
	t.Cleanup(func() { sets.Path, sets.ReadOnly = oldPath, oldRO })

	h := metainfo.NewHashFromHex(idleHookHash)
	bt := NewBTS()
	tr := &Torrent{
		bt:          bt,
		TorrentSpec: &torrent.TorrentSpec{InfoHash: h},
		expiredTime: time.Now().Add(-time.Second),
	}
	bt.torrents[h] = tr
	return tr
}

func setOnIdleClose(t *testing.T, f func(string)) {
	t.Helper()
	prev := OnIdleClose
	OnIdleClose = f
	t.Cleanup(func() { OnIdleClose = prev })
}

// The hook reports the hash of a torrent the ticker closed for idleness, once.
func TestIdleCloseCallsTheHook(t *testing.T) {
	tr := idleFixture(t)
	var got []string
	setOnIdleClose(t, func(h string) { got = append(got, h) })

	tr.UpdateStats()

	if len(got) != 1 || got[0] != idleHookHash {
		t.Fatalf("hook calls = %v, want one for %s", got, idleHookHash)
	}
}

// A torrent that has not expired is left alone and the hook stays quiet; a nil hook
// is a no-op.
func TestIdleCloseHookQuietUnlessClosed(t *testing.T) {
	tr := idleFixture(t)
	tr.expiredTime = time.Now().Add(time.Hour)
	calls := 0
	setOnIdleClose(t, func(string) { calls++ })
	tr.UpdateStats()
	if calls != 0 {
		t.Fatalf("hook ran for a live torrent: %d", calls)
	}

	tr2 := idleFixture(t)
	setOnIdleClose(t, nil)
	tr2.UpdateStats() // must not panic
}

// The hook must run with muTorrent released: the label source can look at the
// torrent again, and a lock held here would deadlock it.
func TestIdleCloseHookRunsUnlocked(t *testing.T) {
	tr := idleFixture(t)
	done := make(chan struct{})
	setOnIdleClose(t, func(string) {
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			tr.muTorrent.Lock()
			tr.muTorrent.Unlock() //nolint:staticcheck // taking the lock is the check
		}()
		wg.Wait()
		close(done)
	})
	go tr.UpdateStats()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("hook ran with muTorrent held")
	}
}
