package torrstor

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"flodni/internal/gostorm/settings"

	"github.com/anacrolix/torrent"
	torrenttypes "github.com/anacrolix/torrent/types"
)

// A priority change can alter only the order of what the engine wants, and the engine
// wakes no idle peer for that. The cache asks the peers itself after a change it makes,
// once per burst of changes.

// countWakes replaces the wake with a counter and a short coalescing window.
func countWakes(t *testing.T, tor *torrent.Torrent) *atomic.Int32 {
	t.Helper()
	woken := new(atomic.Int32)
	prev, prevW := wakePeers, wakeWindow
	wakeWindow = 5 * time.Millisecond
	wakePeers = func(got *torrent.Torrent) {
		if got != tor {
			t.Error("woke the wrong torrent")
		}
		woken.Add(1)
		prev(got) // the real call must not deadlock under the cache's locks
	}
	t.Cleanup(func() { wakePeers, wakeWindow = prev, prevW })
	return woken
}

func waitWakes(t *testing.T, woken *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for woken.Load() < want {
		if time.Now().After(deadline) {
			t.Fatalf("woken = %d, want %d", woken.Load(), want)
		}
		time.Sleep(2 * time.Millisecond)
	}
	time.Sleep(40 * time.Millisecond) // a wrong extra wake would have landed by now
	if got := woken.Load(); got != want {
		t.Fatalf("woken = %d, want %d", got, want)
	}
}

func TestMarkCompleteWakesThePeers(t *testing.T) {
	tor, cache := newPriorityTestCache(t)
	tor.DownloadPieces(3, 4)
	woken := countWakes(t, tor)
	if err := cache.pieces[3].MarkComplete(); err != nil {
		t.Fatal(err)
	}
	waitWakes(t, woken, 1)
}

func TestClearPriorityWakesThePeersOnlyWhenItDropsOne(t *testing.T) {
	tor, cache := newPriorityTestCache(t)
	woken := countWakes(t, tor)

	cache.clearPriority() // nothing to drop
	waitWakes(t, woken, 0)
	cache.muPriority.Lock()
	cache.localPriority[3] = torrent.PiecePriorityNow
	tor.SetPiecePriorities(map[int]torrenttypes.PiecePriority{3: torrent.PiecePriorityNow})
	cache.muPriority.Unlock()
	cache.clearPriority() // no reader covers piece 3
	waitWakes(t, woken, 1)
	if got := tor.PieceState(3).Priority; got != torrent.PiecePriorityNone {
		t.Fatalf("priority = %v, want None", got)
	}
}

func TestWakesWithinTheWindowBecomeOne(t *testing.T) {
	tor, cache := newPriorityTestCache(t)
	woken := countWakes(t, tor)
	wakeWindow = 50 * time.Millisecond
	for i := 0; i < 3; i++ {
		cache.scheduleWake(tor)
	}
	waitWakes(t, woken, 1)
	cache.scheduleWake(tor) // a later burst wakes again
	waitWakes(t, woken, 2)
}

// setLoadPriority wakes only when a priority differs from what the cache set before.
func TestSetLoadPriorityWakesOnlyOnAChange(t *testing.T) {
	oldSets := settings.BTsets
	settings.BTsets = &settings.BTSets{CacheSize: 400 * 16 << 10, ReaderReadAHead: 95, ConnectionsLimit: 8}
	t.Cleanup(func() { settings.BTsets = oldSets })
	// 2000 pieces of 16 KiB: the middle is outside the file's first and last 8 MiB, which
	// setLoadPriority leaves alone.
	tor, cache := newPriorityTestCacheN(t, 2000)
	woken := countWakes(t, tor)

	file := tor.Files()[0]
	r := &Reader{cache: cache, file: file, isUse: true, offset: 1000 * (16 << 10), readahead: 5 * (16 << 10)}
	cache.muReaders.Lock()
	cache.readers[r] = struct{}{}
	cache.muReaders.Unlock()
	ranges := []Range{{0, 1999, file}}

	cache.setLoadPriority(ranges)
	waitWakes(t, woken, 1)
	if got := tor.PieceState(1000).Priority; got != torrent.PiecePriorityNow {
		t.Fatalf("setup: piece 1000 priority %v, want Now", got)
	}
	cache.setLoadPriority(ranges) // the same priorities again
	waitWakes(t, woken, 1)

	r.mu.Lock()
	r.offset += 3 * (16 << 10) // a Readahead piece becomes Now
	r.mu.Unlock()
	cache.setLoadPriority(ranges)
	waitWakes(t, woken, 2)
	if got := tor.PieceState(1003).Priority; got != torrent.PiecePriorityNow {
		t.Fatalf("piece 1003 priority %v, want Now", got)
	}
}

// wakePeers is AllowDataUpload, which also clears the engine's "uploads disallowed" flag.
// That is harmless only while Flodni never disallows uploads: nothing outside the vendored
// engine may mention DisallowDataUpload (the method or the spec field).
func TestFlodniNeverDisallowsDataUpload(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..") // internal/
	var hits []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == "anacrolix-torrent" {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), "DisallowDataUpload") {
			hits = append(hits, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("DisallowDataUpload is used in %v: wakePeers would undo it", hits)
	}
}
