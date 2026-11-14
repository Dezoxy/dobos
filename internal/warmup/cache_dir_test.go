package warmup

import (
	"bytes"
	"errors"
	"flodni/internal/flog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// A missing or damaged cache disk means no cache: nothing here may fill the parent
// filesystem, drop good heads or record an eviction that did not happen.

// logBuf is a log sink the test can read while the cache goroutines still write.
type logBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// captureLog collects the cache log for the test.
func captureLog(t *testing.T) *logBuf {
	t.Helper()
	l := &logBuf{}
	old := logf
	logf = flog.New(l, "DiskWarmup", true)
	t.Cleanup(func() { logf = old })
	return l
}

// denseFile writes n bytes from 0.
func denseFile(t *testing.T, path string, n int) {
	t.Helper()
	if err := os.WriteFile(path, bytes.Repeat([]byte{7}, n), 0o644); err != nil {
		t.Fatal(err)
	}
}

// holeyFile has a hole after its first block: size n, data only at [0, 4096).
func holeyFile(t *testing.T, path string, n int64) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(n); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(bytes.Repeat([]byte{7}, 4096), 0); err != nil {
		t.Fatal(err)
	}
}

func TestHeadHasHoles(t *testing.T) {
	dir := t.TempDir()
	const n = 2 << 20
	dense := filepath.Join(dir, "dense")
	denseFile(t, dense, n)
	trailing := filepath.Join(dir, "trailing")
	holeyFile(t, trailing, n)
	leading := filepath.Join(dir, "leading") // data at the end only
	f, err := os.Create(leading)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(bytes.Repeat([]byte{7}, 4096), n-4096); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	allHole := filepath.Join(dir, "hole")
	if err := os.WriteFile(allHole, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(allHole, n); err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		name string
		path string
		want bool
	}{
		{"dense head is kept", dense, false},
		{"hole after the data", trailing, true},
		{"hole before the data", leading, true},
		{"nothing but a hole", allHole, true},
	} {
		got, err := headHasHoles(tt.path)
		if err != nil || got != tt.want {
			t.Errorf("%s: holes=%v err=%v, want %v", tt.name, got, err, tt.want)
		}
	}
}

func TestHeadHasHolesUnsupportedFilesystemCountsAsDense(t *testing.T) {
	old := seekFn
	t.Cleanup(func() { seekFn = old })
	seekFn = func(int, int64, int) (int64, error) { return 0, syscall.EINVAL }
	buf := captureLog(t)
	seekUnsupportedLogged.Store(false)
	t.Cleanup(func() { seekUnsupportedLogged.Store(false) })

	p := filepath.Join(t.TempDir(), "h")
	denseFile(t, p, 4096)
	for range 3 {
		if holes, err := headHasHoles(p); holes || err != nil {
			t.Fatalf("holes=%v err=%v, want a dense head kept", holes, err)
		}
	}
	if n := strings.Count(buf.String(), "SEEK_HOLE"); n != 1 {
		t.Fatalf("logged %d times, want once:\n%s", n, buf.String())
	}
}

// markerEnv runs InitDiskWarmup on dir and restores the package state after.
func markerEnv(t *testing.T, dir string) *logBuf {
	t.Helper()
	buf := captureLog(t)
	oldQ, oldDW := diskQuotaGB, DiskWarmup
	t.Cleanup(func() {
		DiskWarmup = oldDW
		diskQuotaGB = oldQ
		FileSize, TailWarmupSize = 64<<20, 16<<20
		headBoundaryChunk, persistTails = true, false
	})
	DiskWarmup = nil
	InitDiskWarmup(Options{QuotaGB: 1, Dir: dir, HeadMB: 4, TailMB: 4})
	return buf
}

func TestMissingMarkerKeepsDenseHeadsAndDropsHoles(t *testing.T) {
	dir := t.TempDir()
	good1, good2 := filepath.Join(dir, "a-1.warmup"), filepath.Join(dir, "b-1.warmup")
	bad := filepath.Join(dir, "c-1.warmup")
	denseFile(t, good1, 2<<20)
	denseFile(t, good2, 3<<20)
	holeyFile(t, bad, 2<<20)

	buf := markerEnv(t, dir)

	if !exists(good1) || !exists(good2) {
		t.Fatal("a dense head must survive a missing marker")
	}
	if exists(bad) {
		t.Fatal("a head with a hole must be dropped")
	}
	if !exists(filepath.Join(dir, denseMarker)) {
		t.Fatal("the marker must be written")
	}
	out := buf.String()
	if !strings.Contains(out, "WARNING") || !strings.Contains(out, "checked 3 heads") ||
		!strings.Contains(out, "dropped 1") {
		t.Fatalf("want a WARNING with the counts, got:\n%s", out)
	}
}

func TestMarkerPresentSkipsTheHeadCheck(t *testing.T) {
	dir := t.TempDir()
	denseFile(t, filepath.Join(dir, denseMarker), 0)
	bad := filepath.Join(dir, "c-1.warmup")
	holeyFile(t, bad, 2<<20)

	buf := markerEnv(t, dir)

	if !exists(bad) {
		t.Fatal("with the marker present no head is checked at start")
	}
	if strings.Contains(buf.String(), "checked") {
		t.Fatalf("no check expected:\n%s", buf.String())
	}
}

func TestEmptyOrNewDirGetsTheMarkerQuietly(t *testing.T) {
	for name, mk := range map[string]func(string){
		"empty dir": func(p string) { _ = os.Mkdir(p, 0o755) },
		"new dir":   func(string) {},
	} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "cache")
			mk(dir)
			buf := markerEnv(t, dir)
			if !exists(filepath.Join(dir, denseMarker)) {
				t.Fatal("marker missing")
			}
			if strings.Contains(buf.String(), "WARNING: ") && strings.Contains(buf.String(), "heads") {
				t.Fatalf("an empty cache has no heads to warn about:\n%s", buf.String())
			}
		})
	}
}

func TestInitWarnsWhenTheCacheDoesNotInitialise(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	denseFile(t, blocker, 1)
	buf := markerEnv(t, filepath.Join(blocker, "cache")) // ENOTDIR
	if DiskWarmup != nil {
		t.Fatal("the cache must not initialise")
	}
	if out := buf.String(); !strings.Contains(out, "WARNING") || !strings.Contains(out, "no cache") {
		t.Fatalf("want a WARNING naming the missing cache, got:\n%s", out)
	}
}

func TestInitHintsAtAnEmptyMountpoint(t *testing.T) {
	// The temp dir shares its device with its parent, like an unmounted mountpoint.
	buf := markerEnv(t, t.TempDir())
	if !strings.Contains(buf.String(), "warmup_dir is not a separate mount") {
		t.Fatalf("want the mount hint, got:\n%s", buf.String())
	}
	if DiskWarmup == nil {
		t.Fatal("the hint must not refuse the cache")
	}
}

func TestNoRuntimeMkdirAll(t *testing.T) {
	missing := func(t *testing.T) (*DiskWarmupCache, string) {
		dir := filepath.Join(t.TempDir(), "gone")
		return indexCache(t, dir), dir
	}
	t.Run("head write", func(t *testing.T) {
		d, dir := missing(t)
		d.WriteChunk("h", 1, bytes.Repeat([]byte{7}, 2<<20), 0)
		time.Sleep(100 * time.Millisecond)
		if exists(dir) {
			t.Fatal("a head write re-created the cache directory")
		}
	})
	t.Run("tail write", func(t *testing.T) {
		d, dir := missing(t)
		d.WriteTail("h", 1, bytes.Repeat([]byte{7}, 1<<20), 0, 1<<20)
		time.Sleep(100 * time.Millisecond)
		if exists(dir) {
			t.Fatal("a tail write re-created the cache directory")
		}
	})
	t.Run("fill", func(t *testing.T) {
		s := newSwarm(fillFile).allArrive()
		d := fillEnv(t, s)
		d.dir = filepath.Join(t.TempDir(), "gone")
		if _, err := d.FillWindows(fillReq()); err == nil {
			t.Fatal("a fill into a vanished directory must fail")
		}
		if exists(d.dir) {
			t.Fatal("a fill re-created the cache directory")
		}
	})
}

func TestFailedUnlinkIsNotAnEviction(t *testing.T) {
	d := evictEnv(t)
	// A non-empty directory named like a head: os.Remove fails on it even as root.
	stuck := filepath.Join(d.dir, "a-1.warmup")
	if err := os.MkdirAll(filepath.Join(stuck, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(d.dir, "b-1.warmup")
	if f, err := os.Create(old); err != nil { // sparse: it only has to count as nearly full
		t.Fatal(err)
	} else {
		if err := f.Truncate(quota - mb); err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(stuck, past, past); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(stuck)
	if err != nil {
		t.Fatal(err)
	}
	setTotal(d, quota)

	evicted, _ := d.enforceQuotaLocked(2*mb, evictOpts{policy: EvictAny, trigger: TriggerHeadCreate})

	if len(evicted) != 1 || evicted[0].Key.Hash != "b" {
		t.Fatalf("evicted %+v, want only b: a could not be unlinked", evicted)
	}
	if !exists(stuck) || exists(old) {
		t.Fatal("test setup: the stuck entry must remain and b must go")
	}
	if got := atomic.LoadInt64(&d.totalSize); got != fi.Size() {
		t.Fatalf("total = %d, want %d: the stuck entry's bytes stay counted", got, fi.Size())
	}
}

func TestReadDirErrorKeepsTheTotal(t *testing.T) {
	d := evictEnv(t)
	d.dir = filepath.Join(t.TempDir(), "gone")
	setTotal(d, quota-mb)

	evicted, room := d.enforceQuotaLocked(2*mb, evictOpts{policy: EvictAny})

	if len(evicted) != 0 || room {
		t.Fatalf("evicted=%d room=%v, want nothing and no room", len(evicted), room)
	}
	if got := atomic.LoadInt64(&d.totalSize); got != quota-mb {
		t.Fatalf("total = %d, want it kept at %d", got, quota-mb)
	}
}

func TestReadOnlyDirectoryStillServesReads(t *testing.T) {
	d := indexCache(t, t.TempDir())
	p := d.filePath("h", 1)
	denseFile(t, p, 2<<20)
	d.sizeCache.Store(p, sizeEntry{size: 2 << 20, updatedAt: time.Now()})

	old := openFile
	t.Cleanup(func() { openFile = old })
	openFile = func(name string, flag int, perm os.FileMode) (*os.File, error) {
		if flag&os.O_RDWR != 0 {
			return nil, &os.PathError{Op: "open", Path: name, Err: syscall.EROFS}
		}
		return os.OpenFile(name, flag, perm)
	}
	buf := captureLog(t)
	readOnlyLogged.Store(false) // the notice is once per process
	t.Cleanup(func() { readOnlyLogged.Store(false) })

	got := make([]byte, 4096)
	n, err := d.ReadAt("h", 1, got, 0)
	if err != nil || n != 4096 || got[0] != 7 {
		t.Fatalf("read n=%d err=%v: a read-only directory must still serve its heads", n, err)
	}
	// A write onto the read-only handle is skipped, not logged per chunk.
	d.WriteChunk("h", 1, bytes.Repeat([]byte{9}, 4096), 2<<20)
	time.Sleep(50 * time.Millisecond)
	if fi, _ := os.Stat(p); fi.Size() != 2<<20 {
		t.Fatalf("size = %d", fi.Size())
	}
	if strings.Count(buf.String(), "read-only") != 1 {
		t.Fatalf("want one read-only notice:\n%s", buf.String())
	}
}

func TestEvictionStopsAtTheFailureCap(t *testing.T) {
	d := evictEnv(t)
	for i := range 3 * maxEvictFailures {
		stuck := filepath.Join(d.dir, "s"+string(rune('a'+i))+"-1.warmup")
		if err := os.MkdirAll(filepath.Join(stuck, "x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A sparse file keeps the walk total at the quota; its unit fails to unlink too.
	if f, err := os.Create(filepath.Join(d.dir, "zz-1.warmup")); err != nil {
		t.Fatal(err)
	} else {
		_ = f.Truncate(quota)
		_ = f.Close()
	}
	setTotal(d, quota)
	buf := captureLog(t)

	evicted, _ := d.enforceQuotaLocked(2*mb, evictOpts{policy: EvictAny, trigger: TriggerHeadCreate})

	if len(evicted) != 0 {
		t.Fatalf("reported %d evictions, want none", len(evicted))
	}
	if n := strings.Count(buf.String(), "remove s"); n != maxEvictFailures {
		t.Fatalf("%d unlink attempts, want the cap %d:\n%s", n, maxEvictFailures, buf.String())
	}
	if !strings.Contains(buf.String(), "eviction stopped") {
		t.Fatalf("want the stop line:\n%s", buf.String())
	}
}

func TestPartialEvictionReportsTheBytesRemoved(t *testing.T) {
	d := evictEnv(t)
	head := filepath.Join(d.dir, "a-1.warmup")
	if f, err := os.Create(head); err != nil {
		t.Fatal(err)
	} else {
		_ = f.Truncate(quota - mb)
		_ = f.Close()
	}
	if err := os.MkdirAll(filepath.Join(d.dir, "a-1.warmup-tail", "x"), 0o755); err != nil { // stuck
		t.Fatal(err)
	}
	setTotal(d, quota)

	evicted, _ := d.enforceQuotaLocked(2*mb, evictOpts{policy: EvictAny, trigger: TriggerHeadCreate})

	if len(evicted) != 1 || evicted[0].HeadBytes != quota-mb || evicted[0].TailBytes != 0 {
		t.Fatalf("evicted %+v, want the head's bytes and no tail", evicted)
	}
	if exists(head) {
		t.Fatal("the head must be gone")
	}
}

func TestFillChecksTheDirectoryBeforeFetching(t *testing.T) {
	s := newSwarm(fillFile).allArrive()
	d := fillEnv(t, s)
	d.dir = filepath.Join(t.TempDir(), "gone")

	_, err := d.FillWindows(fillReq())

	if !errors.Is(err, ErrCacheDir) || errors.Is(err, ErrEvicted) {
		t.Fatalf("err = %v, want ErrCacheDir", err)
	}
	if s.fetchCount() != 0 || s.requestCount() != 0 {
		t.Fatalf("fetches=%d requests=%d: nothing may be fetched for a cache that cannot store it",
			s.fetchCount(), s.requestCount())
	}
}
