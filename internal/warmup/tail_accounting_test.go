package warmup

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The tracked total gates the quota, so tail writes must count like head writes.

func tailTotal(d *DiskWarmupCache) int64 { return atomic.LoadInt64(&d.totalSize) }

func TestTailWriteCountsGrowthOnly(t *testing.T) {
	d := indexCache(t, t.TempDir())
	const fileSize = 8 << 20 // tail window is the last 1 MiB
	tailStart := int64(fileSize - 1<<20)

	d.writeTail("h", 1, bytes.Repeat([]byte{1}, 256<<10), tailStart, fileSize)
	if got := tailTotal(d); got != 256<<10 {
		t.Fatalf("first write: total=%d want %d", got, 256<<10)
	}
	d.writeTail("h", 1, bytes.Repeat([]byte{1}, 256<<10), tailStart, fileSize) // same range
	if got := tailTotal(d); got != 256<<10 {
		t.Fatalf("rewrite must not double count: total=%d", got)
	}
	d.writeTail("h", 1, bytes.Repeat([]byte{2}, 256<<10), tailStart+256<<10, fileSize)
	if got := tailTotal(d); got != 512<<10 {
		t.Fatalf("extension: total=%d want %d", got, 512<<10)
	}
	fi, err := os.Stat(d.tailPath("h", 1))
	if err != nil || fi.Size() != tailTotal(d) {
		t.Fatalf("tracked total must equal the file's apparent size: %v %v", fi, err)
	}
}

func TestRemoveHashReturnsTailBytes(t *testing.T) {
	d := indexCache(t, t.TempDir())
	const fileSize = 8 << 20
	d.writeTail("h", 1, bytes.Repeat([]byte{1}, 512<<10), fileSize-1<<20, fileSize)
	d.RemoveHash("h")
	if got := tailTotal(d); got != 0 {
		t.Fatalf("total after removal = %d, want 0", got)
	}
}

func TestTailGrowthOverQuotaIsEnforcedByNextHead(t *testing.T) {
	oldGB := diskQuotaGB
	t.Cleanup(func() { diskQuotaGB = oldGB })
	diskQuotaGB = 1
	const quota = int64(1) << 30

	d := indexCache(t, t.TempDir())
	// A sparse head file that nearly fills the quota by apparent size, the basis the
	// startup walk and the eviction walk both use. It is the oldest file.
	oldPath := d.filePath("old", 1)
	f, err := os.Create(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(quota - 256<<10); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(oldPath, past, past); err != nil {
		t.Fatal(err)
	}
	atomic.StoreInt64(&d.totalSize, quota-256<<10)

	const fileSize = 8 << 20
	d.writeTail("new", 1, bytes.Repeat([]byte{2}, 512<<10), fileSize-1<<20, fileSize)

	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("a tail write must not evict: %v", err)
	}
	if got := tailTotal(d); got <= quota {
		t.Fatalf("total = %d, want it counted past the quota %d", got, quota)
	}

	d.WriteChunk("head", 1, bytes.Repeat([]byte{3}, 64<<10), 0)
	waitFor(t, "oldest file evicted by the next head", func() bool {
		_, serr := os.Stat(oldPath)
		return os.IsNotExist(serr)
	})
	if _, err := os.Stat(d.tailPath("new", 1)); err != nil {
		t.Fatalf("new tail must survive: %v", err)
	}
}

func TestTailCountSurvivesRestartWalk(t *testing.T) {
	dir := t.TempDir()
	d := indexCache(t, dir)
	const fileSize = 8 << 20
	d.writeTail("a", 1, bytes.Repeat([]byte{1}, 300<<10), fileSize-1<<20, fileSize)
	d.writeTail("b", 2, bytes.Repeat([]byte{2}, 512<<10), fileSize-1<<20, fileSize)

	fresh := &DiskWarmupCache{dir: dir}
	total, _, _, err := fresh.scanExisting(false)
	if err != nil {
		t.Fatal(err)
	}
	if total != tailTotal(d) || total != 812<<10 {
		t.Fatalf("restart walk=%d tracked=%d want %d", total, tailTotal(d), 812<<10)
	}
}

func TestTailFirstWriteAtOffsetCountsTheHole(t *testing.T) {
	d := indexCache(t, t.TempDir())
	const fileSize = 8 << 20
	d.writeTail("h", 1, bytes.Repeat([]byte{1}, 128<<10), fileSize-1<<20+256<<10, fileSize)
	if got := tailTotal(d); got != 384<<10 {
		t.Fatalf("total = %d, want %d (hole + data, apparent size)", got, 384<<10)
	}
}

func TestTailAccountingUnderConcurrency(t *testing.T) {
	oldGB := diskQuotaGB
	t.Cleanup(func() { diskQuotaGB = oldGB })
	diskQuotaGB = 1

	dir := t.TempDir()
	d := indexCache(t, dir)
	const fileSize = 8 << 20
	tailStart := int64(fileSize - 1<<20)

	done := make(chan struct{})
	go func() {
		defer close(done)
		var wg sync.WaitGroup
		for g := 0; g < 8; g++ {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				// Tails share and split files and race RemoveHash. Heads use hashes
				// that are never removed: a head write racing RemoveHash on the same
				// hash already drifts the total (head path, not under tailMu).
				hash := fmt.Sprintf("h%d", g%3)
				for i := 0; i < 40; i++ {
					off := tailStart + int64(i%4)*(128<<10)
					d.writeTail(hash, 1, bytes.Repeat([]byte{byte(g)}, 128<<10), off, fileSize)
					d.WriteChunk(fmt.Sprintf("head%d", g%3), 2, bytes.Repeat([]byte{byte(g)}, 64<<10), 0)
					if i%10 == 9 && g == 0 {
						d.RemoveHash(hash)
					}
				}
			}(g)
		}
		wg.Wait()
	}()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("concurrent tail/head/remove writes did not terminate (deadlock?)")
	}

	// Head writes go through the writer goroutine, so the tracked total and the disk
	// can differ while one is in flight. Poll until the queue is empty and they agree.
	diskSum := func() int64 {
		var sum int64
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), warmupSuffix) || strings.HasSuffix(e.Name(), tailSuffix) {
				if fi, err := e.Info(); err == nil {
					sum += fi.Size()
				}
			}
		}
		return sum
	}
	deadline := time.Now().Add(10 * time.Second)
	for len(d.writeCh) != 0 || tailTotal(d) != diskSum() {
		if time.Now().After(deadline) {
			t.Fatalf("tracked total %d != apparent size on disk %d (queue=%d)", tailTotal(d), diskSum(), len(d.writeCh))
		}
		time.Sleep(5 * time.Millisecond)
	}
}
