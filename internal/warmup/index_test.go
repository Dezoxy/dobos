package warmup

import (
	"bytes"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// Dezoxy fork: the warmup as an index cache - fixed small windows, tails that survive a
// restart, and nothing on SSD before its pieces verify.

// indexCache sets the package to index mode with 1 MB windows and returns a cache in dir.
func indexCache(t *testing.T, dir string) *DiskWarmupCache {
	t.Helper()
	oldHead, oldTail, oldBoundary, oldPersist, oldVerified, oldFetch := FileSize, TailWarmupSize, headBoundaryChunk, persistTails, RangeVerified, TailFetch
	t.Cleanup(func() {
		FileSize, TailWarmupSize, headBoundaryChunk, persistTails, RangeVerified, TailFetch = oldHead, oldTail, oldBoundary, oldPersist, oldVerified, oldFetch
	})
	FileSize, TailWarmupSize = 1<<20, 1<<20
	headBoundaryChunk, persistTails, RangeVerified, TailFetch = false, true, nil, nil
	d := &DiskWarmupCache{dir: dir, writeCh: make(chan warmupWrite, 32)}
	stopped := make(chan struct{})
	go func() { d.writeWorker(); close(stopped) }()
	// Registered after the restore above, so it runs first: the worker reads the globals.
	t.Cleanup(func() { close(d.writeCh); <-stopped })
	return d
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestInitDiskWarmupIndexModeIgnoresUseDisk(t *testing.T) {
	defer func() {
		DiskWarmup = nil
		FileSize, TailWarmupSize = 64<<20, 16<<20
		headBoundaryChunk, persistTails = true, false
	}()
	InitDiskWarmup(Options{QuotaGB: 1, Dir: t.TempDir(), HeadMB: 4, TailMB: 6})
	if DiskWarmup == nil {
		t.Fatal("index mode must not depend on UseDisk")
	}
	if FileSize != 4<<20 || TailWarmupSize != 6<<20 || headBoundaryChunk || !persistTails {
		t.Fatalf("head=%d tail=%d boundary=%v persist=%v", FileSize, TailWarmupSize, headBoundaryChunk, persistTails)
	}
}

func TestIndexModeHeadStopsAtTheWindow(t *testing.T) {
	d := indexCache(t, t.TempDir())
	d.WriteChunk("h", 1, bytes.Repeat([]byte{7}, 3<<20), 0)
	d.WriteChunk("h", 1, bytes.Repeat([]byte{8}, 1<<20), 1<<20) // starts at the window: dropped
	waitFor(t, "head write", func() bool { return d.GetAvailableRange("h", 1) > 0 })
	time.Sleep(50 * time.Millisecond)
	fi, err := os.Stat(d.filePath("h", 1))
	if err != nil || fi.Size() != 1<<20 {
		t.Fatalf("head file must hold exactly the window, got %v %v", fi, err)
	}
}

func TestTailCoverageSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	const fileSize = 10 << 20
	d := indexCache(t, dir)
	tail := bytes.Repeat([]byte{9}, 1<<20)
	d.WriteTail("h", 2, tail, fileSize-(1<<20), fileSize)
	if !d.TailReady("h", 2) {
		t.Fatal("full tail window written but not ready")
	}

	restarted := &DiskWarmupCache{dir: dir}
	if _, _, _, err := restarted.scanExisting(false); err != nil {
		t.Fatal(err)
	}
	if !restarted.TailReady("h", 2) {
		t.Fatal("tail coverage lost across a restart")
	}
	buf := make([]byte, 4096)
	if n, _ := restarted.ReadTail("h", 2, buf, fileSize-4096, fileSize); n != 4096 || buf[0] != 9 {
		t.Fatalf("restored tail read n=%d first=%d", n, buf[0])
	}

	// A different window size makes the stored coverage meaningless: drop it.
	TailWarmupSize = 2 << 20
	again := &DiskWarmupCache{dir: dir}
	if _, dropped, _, _ := again.scanExisting(false); dropped != 1 || again.TailReady("h", 2) {
		t.Fatalf("tail for another window size kept (dropped=%d)", dropped)
	}
	if _, err := os.Stat(d.tailPath("h", 2) + coverSuffix); !os.IsNotExist(err) {
		t.Fatal("coverage file left behind")
	}
}

func TestPartialTailIsNotReadyAfterRestart(t *testing.T) {
	dir := t.TempDir()
	const fileSize = 10 << 20
	d := indexCache(t, dir)
	d.WriteTail("h", 3, make([]byte, 4096), fileSize-4096, fileSize)

	restarted := &DiskWarmupCache{dir: dir}
	restarted.scanExisting(false)
	if restarted.TailReady("h", 3) {
		t.Fatal("a probe-sized tail must not count as the whole window")
	}
	buf := make([]byte, 4096)
	if n, _ := restarted.ReadTail("h", 3, buf, fileSize-4096, fileSize); n != 4096 {
		t.Fatalf("the covered probe range should still be served, n=%d", n)
	}
	if n, _ := restarted.ReadTail("h", 3, buf, fileSize-8192, fileSize); n != 0 {
		t.Fatalf("an unwritten range was served from a sparse file, n=%d", n)
	}
}

func TestUnverifiedBlocksWaitForTheirPieces(t *testing.T) {
	d := indexCache(t, t.TempDir())
	oldPoll, oldMax := verifyPollInterval, verifyWaitMax
	defer func() { verifyPollInterval, verifyWaitMax = oldPoll, oldMax }()
	verifyPollInterval, verifyWaitMax = 10*time.Millisecond, time.Second

	var ok, requested atomic.Bool
	RangeVerified = func(string, int, int64, int64) bool { return ok.Load() }
	RequestPieces = func(string, int, int64, int64) { requested.Store(true) }
	defer func() { RequestPieces = nil }()
	// The piece is re-downloaded after a failed hash: the verified bytes are 2s, the
	// copy the read handed out was 1s. The cache must hold the verified bytes.
	TailFetch = func(_ string, _ int, _ int64, buf []byte) (int, error) {
		for i := range buf {
			buf[i] = 2
		}
		return len(buf), nil
	}
	d.WriteChunk("h", 4, bytes.Repeat([]byte{1}, 1<<20), 0)
	time.Sleep(50 * time.Millisecond)
	if d.GetAvailableRange("h", 4) != 0 {
		t.Fatal("unverified head written to SSD")
	}
	waitFor(t, "whole pieces requested", requested.Load)
	ok.Store(true)
	waitFor(t, "verified head write", func() bool {
		d.sizeCache.Delete(d.filePath("h", 4))
		d.missing.Delete(d.filePath("h", 4))
		return d.GetAvailableRange("h", 4) == 1<<20
	})
	buf := make([]byte, 16)
	if n, _ := d.ReadAt("h", 4, buf, 0); n != 16 || buf[0] != 2 {
		t.Fatalf("cache holds the pre-verification copy (n=%d first=%d)", n, buf[0])
	}
}

func TestWaiterDoesNotResurrectARemovedFile(t *testing.T) {
	d := indexCache(t, t.TempDir())
	oldPoll, oldMax := verifyPollInterval, verifyWaitMax
	defer func() { verifyPollInterval, verifyWaitMax = oldPoll, oldMax }()
	verifyPollInterval, verifyWaitMax = 10*time.Millisecond, time.Second

	var ok atomic.Bool
	RangeVerified = func(string, int, int64, int64) bool { return ok.Load() }
	TailFetch = func(_ string, _ int, _ int64, buf []byte) (int, error) { return len(buf), nil }
	const fileSize = 10 << 20
	d.WriteTail("h", 6, make([]byte, 4096), fileSize-4096, fileSize)
	d.RemoveHash("h")
	ok.Store(true)
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(d.tailPath("h", 6)); !os.IsNotExist(err) {
		t.Fatal("a waiter recreated a removed tail file")
	}
}

func TestBlocksThatNeverVerifyAreDropped(t *testing.T) {
	d := indexCache(t, t.TempDir())
	oldPoll, oldMax := verifyPollInterval, verifyWaitMax
	defer func() { verifyPollInterval, verifyWaitMax = oldPoll, oldMax }()
	verifyPollInterval, verifyWaitMax = 5*time.Millisecond, 30*time.Millisecond

	RangeVerified = func(string, int, int64, int64) bool { return false }
	before := UnverifiedDrops()
	const fileSize = 10 << 20
	d.WriteTail("h", 5, make([]byte, 4096), fileSize-4096, fileSize)
	waitFor(t, "drop", func() bool { return UnverifiedDrops() == before+1 })
	if _, err := os.Stat(d.tailPath("h", 5)); !os.IsNotExist(err) {
		t.Fatal("unverified tail reached the SSD")
	}
}

func TestIndexTailFillWaitsAndWritesTheWholeWindow(t *testing.T) {
	d := indexCache(t, t.TempDir())
	oldPoll := verifyPollInterval
	defer func() { verifyPollInterval = oldPoll; RequestPieces = nil }()
	verifyPollInterval = 5 * time.Millisecond

	const fileSize = 10 << 20
	var ok atomic.Bool
	var reqOff, reqLen atomic.Int64
	RangeVerified = func(string, int, int64, int64) bool { return ok.Load() }
	RequestPieces = func(_ string, _ int, off, n int64) { reqOff.Store(off); reqLen.Store(n) }
	TailFetch = func(_ string, _ int, _ int64, buf []byte) (int, error) {
		for i := range buf {
			buf[i] = 3
		}
		return len(buf), nil
	}
	d.EnsureTail("h", 7, fileSize)
	waitFor(t, "piece request", func() bool { return reqLen.Load() > 0 })
	if reqOff.Load() != fileSize-(1<<20) || reqLen.Load() != 1<<20 {
		t.Fatalf("requested %d+%d, want the whole tail window", reqOff.Load(), reqLen.Load())
	}
	time.Sleep(30 * time.Millisecond)
	if d.TailReady("h", 7) {
		t.Fatal("tail written before its pieces verified")
	}
	ok.Store(true)
	waitFor(t, "tail ready", func() bool { return d.TailReady("h", 7) })
	waitFor(t, "fill finished", func() bool { return !d.tailFillRunning("h", 7) })
	buf := make([]byte, 8)
	if n, _ := d.ReadTail("h", 7, buf, fileSize-8, fileSize); n != 8 || buf[0] != 3 {
		t.Fatalf("tail read n=%d first=%d", n, buf[0])
	}
}

func TestSeedSpansWritesVerifiedBytes(t *testing.T) {
	d := indexCache(t, t.TempDir())
	const size = 5 << 20
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i % 251)
	}
	// Verified: the whole head window, and the tail except its first 64 KB (a piece the
	// caller could not hash).
	spans := []Seed{{0, data[:1<<20]}, {size - (1 << 20) + 65536, data[size-(1<<20)+65536:]}}
	head, tail, err := d.SeedSpans("h", 8, size, spans)
	if err != nil || head != 1<<20 || tail != (1<<20)-65536 {
		t.Fatalf("head=%d tail=%d err=%v", head, tail, err)
	}
	if !d.HeadReady("h", 8) || d.TailReady("h", 8) {
		t.Fatal("head should be ready, tail should keep its unverified gap")
	}
	buf := make([]byte, 4096)
	if n, _ := d.ReadAt("h", 8, buf, 1000); n != 4096 || !bytes.Equal(buf, data[1000:5096]) {
		t.Fatal("head bytes differ from the source")
	}
	if n, _ := d.ReadTail("h", 8, buf, size-4096, size); n != 4096 || !bytes.Equal(buf, data[size-4096:]) {
		t.Fatal("tail bytes differ from the source")
	}
	if n, _ := d.ReadTail("h", 8, buf, size-(1<<20), size); n != 0 {
		t.Fatal("the unverified gap was served")
	}
}
