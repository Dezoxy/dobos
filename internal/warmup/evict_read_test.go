package warmup

import (
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// readOrderEnv is evictEnv with the three files of TestEvictionOrder: a, b, c, written
// 3h, 2h and 1h ago, 400 MiB each, so one file is over the quota. It restores the order.
func readOrderEnv(t *testing.T, order string) *DiskWarmupCache {
	t.Helper()
	d := evictEnv(t)
	SetEvictionOrder(order)
	t.Cleanup(func() { SetEvictionOrder(EvictOrderWritten) })
	for i, stem := range []string{"a-1", "b-1", "c-1"} {
		age := time.Duration(3-i) * time.Hour
		sparseFile(t, d.dir+"/"+stem+warmupSuffix, 300*mb, age)
		sparseFile(t, d.dir+"/"+stem+tailSuffix, 100*mb, age)
	}
	setTotal(d, 1200*mb)
	return d
}

// stampRead records a cache hit for the file as if it happened age ago.
func stampRead(d *DiskWarmupCache, hash string, idx int, age time.Duration) {
	n := new(atomic.Int64)
	n.Store(time.Now().Add(-age).UnixNano())
	d.lastRead.Store(FileKey{Hash: hash, FileIdx: idx}, n)
}

// evictOne runs the quota check and returns the one victim's stem.
func evictOne(t *testing.T, d *DiskWarmupCache) string {
	t.Helper()
	d.mu.Lock()
	ev, room := d.enforceQuotaLocked(0, evictOpts{trigger: "test"})
	d.mu.Unlock()
	if !room || len(ev) != 1 {
		t.Fatalf("room=%v evicted=%+v, want one victim", room, ev)
	}
	return ev[0].Key.Hash + "-1"
}

// The default order (written) is today's: cache hits change nothing, whatever was read.
func TestEvictionOrderWrittenIgnoresReads(t *testing.T) {
	d := readOrderEnv(t, EvictOrderWritten)
	d.noteRead("a", 1) // the oldest-written file is read just now
	if _, ok := d.lastRead.Load(FileKey{"a", 1}); ok {
		t.Fatal("the default order must not track reads")
	}
	stampRead(d, "a", 1, 0) // even a stamp that is there is not read
	if got := evictOne(t, d); got != "a-1" {
		t.Fatalf("evicted %s, want a-1: the oldest written goes under the default order", got)
	}
}

func TestEvictionOrderReadEvictsLeastRecentlyRead(t *testing.T) {
	d := readOrderEnv(t, EvictOrderRead)
	stampRead(d, "a", 1, time.Minute)    // oldest written, read a minute ago
	stampRead(d, "b", 1, 90*time.Minute) // read 90 min ago, still newer than its write
	stampRead(d, "c", 1, 10*time.Minute) // written last, read 10 min ago
	if got := evictOne(t, d); got != "b-1" {
		t.Fatalf("evicted %s, want b-1: the least recently read", got)
	}
}

// A file never read since start ranks by its write time.
func TestEvictionOrderReadFallsBackToWriteTime(t *testing.T) {
	d := readOrderEnv(t, EvictOrderRead)
	stampRead(d, "a", 1, time.Minute) // only a was read: b (2h) is older than c (1h)
	if got := evictOne(t, d); got != "b-1" {
		t.Fatalf("evicted %s, want b-1: the oldest write among the unread", got)
	}
}

func TestEvictionOrderReadKeepsNeverPlayedFirst(t *testing.T) {
	d := readOrderEnv(t, EvictOrderRead)
	IsPlayed = func(k FileKey) bool { return k.Hash != "c" } // a and b played, c never
	stampRead(d, "c", 1, 0)                                  // c is the most recently read
	stampRead(d, "a", 1, 5*time.Hour)
	if got := evictOne(t, d); got != "c-1" {
		t.Fatalf("evicted %s, want c-1: a never-played file goes before any played one", got)
	}
}

func TestEvictionOrderReadHonoursProtect(t *testing.T) {
	d := readOrderEnv(t, EvictOrderRead)
	stampRead(d, "b", 1, 5*time.Hour)
	d.mu.Lock()
	ev, _ := d.enforceQuotaLocked(0, evictOpts{trigger: "test",
		protect: func(k FileKey) bool { return k.Hash == "b" }})
	d.mu.Unlock()
	if len(ev) != 1 || ev[0].Key.Hash == "b" {
		t.Fatalf("evicted %+v: a protected file is never a victim", ev)
	}
}

func TestCacheHitStampsReadOnlyWithReadOrder(t *testing.T) {
	for _, order := range []string{EvictOrderWritten, EvictOrderRead} {
		d := evictEnv(t)
		SetEvictionOrder(order)
		t.Cleanup(func() { SetEvictionOrder(EvictOrderWritten) })
		if err := os.WriteFile(d.filePath("h", 4), make([]byte, 16), 0o644); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 8)
		if n, _ := d.ReadAt("h", 4, buf, 0); n != 8 {
			t.Fatalf("%s: read %d bytes, want 8", order, n)
		}
		_, stamped := d.lastRead.Load(FileKey{"h", 4})
		if stamped != (order == EvictOrderRead) {
			t.Fatalf("%s: stamped = %v", order, stamped)
		}
	}
}
