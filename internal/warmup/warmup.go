package warmup

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"flodni/internal/gostorm/settings"
)

// FileSize is the per-file head cache cap. Set at init from config, default 64 MB.
var FileSize int64 = 64 * 1024 * 1024

// TailWarmupSize is the per-file tail window (V265: Cues/seek index). Set at init.
var TailWarmupSize int64 = 16 * 1024 * 1024

// headBoundaryChunk lets the chunk that starts exactly at FileSize be written in full
// (upstream: instant start serves up to FileSize plus one pump chunk). Index mode turns
// it off so every file costs at most its two windows.
var headBoundaryChunk = true

// persistTails keeps tail files and their coverage across restarts (index mode).
var persistTails = false

// RangeVerified reports whether every piece covering [off, off+n) of a file has passed
// its checksum. Responsive streaming returns bytes before the piece is hashed, so nothing
// is written to SSD until this says so. Wired at startup; nil skips the check.
var RangeVerified func(hash string, fileID int, off, n int64) bool

// RequestPieces asks for the whole pieces under a block that waits for verification, so
// they complete and get hashed. Wired at startup; nil leaves it to the readers.
var RequestPieces func(hash string, fileID int, off, n int64)

// Options configures InitDiskWarmup. An empty Dir keeps upstream's behaviour.
type Options struct {
	QuotaGB int64
	Dir     string // index mode: warmup lives here regardless of UseDisk
	HeadMB  int64
	TailMB  int64
}

const (
	warmupQuota    int64 = 32 * 1024 * 1024 * 1024 // fallback default 32 GB (overridden by config)
	warmupSuffix         = ".warmup"
	tailSuffix           = ".warmup-tail"    // V265: separate file for tail
	coverSuffix          = ".cover"          // persisted tail coverage, beside its tail file
	denseMarker          = ".dense-migrated" // one-shot marker: head cache rebuilt for the density invariant
	warmupWriteBuf       = 16 * 1024 * 1024  // 16 MB — matches pump chunk size
	handleIdleMax        = 30 * time.Second  // close idle file handles after 30s
	missingTTL           = 10 * time.Second  // negative-cache lifetime, matches sizeCache
	// headReadyFloor is the least covered bytes for a head warmup file to count as
	// warm. A file holding a few bytes is the residue of an aborted attempt (38 bytes
	// in production), and treating it as warm sends Open down the warm path and
	// falsifies any "this title has a warmup" reading.
	headReadyFloor = 1 << 20
)

var diskQuotaGB int64

// DiskWarmup is the global instance, nil when disabled.
var DiskWarmup *DiskWarmupCache

// OnWarmupStateChange, if set, is called synchronously from the single writeWorker goroutine
// when a head warmup starts (active=true, right after the file is created and its first chunk
// written) or completes (active=false) - wired up once at startup (main.go) to signal the
// anacrolix-torrent fork's Torrent.SetWarmupActive. It runs after the write's locks are
// released, so it may call back into the cache.
// Deliberately synchronous, not polled: WriteChunk() only enqueues onto writeCh and returns
// immediately, so a caller checking IsWarmingUp() right after WriteChunk() returns can race ahead
// of this worker actually processing the write - this callback fires precisely when the state
// itself transitions, eliminating that race by construction.
var OnWarmupStateChange func(hash string, fileID int, active bool)

var warmupDurationBuckets [8]atomic.Int64 // <2s,<5s,<10s,<15s,<30s,<60s,<120s,>=120s

func recordWarmupDuration(d time.Duration) {
	s := d.Seconds()
	idx := 7
	switch {
	case s < 2:
		idx = 0
	case s < 5:
		idx = 1
	case s < 10:
		idx = 2
	case s < 15:
		idx = 3
	case s < 30:
		idx = 4
	case s < 60:
		idx = 5
	case s < 120:
		idx = 6
	}
	warmupDurationBuckets[idx].Add(1)
}

// WarmupDurationBucketCounts returns a snapshot of the bucket counts for /metrics.
func WarmupDurationBucketCounts() [8]int64 {
	var out [8]int64
	for i := range warmupDurationBuckets {
		out[i] = warmupDurationBuckets[i].Load()
	}
	return out
}

// V261: sync.Pool for write buffers — avoids 16MB heap allocs per chunk.
var warmupWritePool = sync.Pool{
	New: func() interface{} {
		buf := make([]byte, warmupWriteBuf)
		return &buf
	},
}

type warmupWrite struct {
	hash   string
	fileID int
	buf    *[]byte // pooled buffer pointer — returned to warmupWritePool after write
	len    int     // actual data length within buf
	off    int64
}

// cachedHandle holds an open file descriptor with last-access tracking.
type cachedHandle struct {
	f            *os.File
	lastUsedNano atomic.Int64 // atomic to prevent race with handleReaper
	closed       atomic.Bool  // set by closeHandle before f.Close() so writeWorker can detect stale handles
}

// V264: sizeEntry stores the cached size of a warmup file with TTL.
type sizeEntry struct {
	size      int64
	updatedAt time.Time
}

// tailSpan is a half-open [start,end) byte range within a tail warmup file.
type tailSpan struct{ start, end int64 }

// V265: tailRange tracks which ranges of a tail warmup file were actually written.
// Writes land at whatever offsets the client probes, so the file is sparse: a single
// watermark would mark unwritten holes as covered, and reading a hole returns zeros
// with err==nil - served to the player as if it were real MKV data.
type tailRange struct {
	mu     sync.Mutex
	ranges []tailSpan // disjoint, merged, sorted by start
	window int64      // length of the tail window; 0 means TailWarmupSize (a short file has less)
}

// setWindow records the length of the file's tail window, so a file shorter than
// TailWarmupSize can complete.
func (tr *tailRange) setWindow(n int64) {
	tr.mu.Lock()
	tr.window = n
	tr.mu.Unlock()
}

// snapshot returns the covered bytes and the window length, without any syscall.
func (tr *tailRange) snapshot() (covered, window int64) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	for _, s := range tr.ranges {
		covered += s.end - s.start
	}
	window = tr.window
	if window <= 0 {
		window = TailWarmupSize
	}
	return covered, window
}

// add merges [start,end) into the covered set.
func (tr *tailRange) add(start, end int64) {
	if end <= start {
		return
	}
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.ranges = append(tr.ranges, tailSpan{start, end})
	sort.Slice(tr.ranges, func(i, j int) bool { return tr.ranges[i].start < tr.ranges[j].start })
	merged := tr.ranges[:1]
	for _, s := range tr.ranges[1:] {
		last := &merged[len(merged)-1]
		if s.start <= last.end {
			if s.end > last.end {
				last.end = s.end
			}
			continue
		}
		merged = append(merged, s)
	}
	tr.ranges = merged
}

// covers reports whether all of [start,end) was written. Spans are disjoint and merged,
// so a covered request always falls inside a single span.
func (tr *tailRange) covers(start, end int64) bool {
	if end <= start {
		return false
	}
	tr.mu.Lock()
	defer tr.mu.Unlock()
	i := sort.Search(len(tr.ranges), func(i int) bool { return tr.ranges[i].end > start })
	return i < len(tr.ranges) && tr.ranges[i].start <= start && tr.ranges[i].end >= end
}

// complete reports full coverage of the tail window.
func (tr *tailRange) complete() bool {
	_, window := tr.snapshot()
	return tr.covers(0, window)
}

// DiskWarmupCache persists the first 128MB of each streamed file to SSD.
type DiskWarmupCache struct {
	dir           string
	mu            sync.Mutex   // protects quota enforcement
	totalSize     int64        // V288: Tracked total size of all warmup files in bytes
	missing       sync.Map     // path -> time.Time (negative cache for missing files)
	handles       sync.Map     // path -> *cachedHandle (cached file descriptors)
	sizeCache     sync.Map     // V264: path -> sizeEntry (cached file sizes with TTL)
	tailCoverage  sync.Map     // V265: path -> *tailRange (written range tracking)
	tailFills     sync.Map     // path -> true while a sequential tail fill is running
	pendingVerify sync.Map     // block key -> true while it waits for its pieces to verify
	tailMu        sync.Mutex   // serializes tail writes with each other and with removals
	tailQueue     chan tailJob // index-mode tail fills, one worker
	tailQueueOnce sync.Once
	removals      atomic.Int64 // bumped by every removal; a waiter older than one drops its block
	warmupStarts  sync.Map     // path -> time.Time (STARTING timestamp, for duration histogram)
	writeCh       chan warmupWrite
}

// InitDiskWarmup creates the global warmup cache if UseDisk is enabled.
var logf = log.New(os.Stdout, "[DiskWarmup] ", log.LstdFlags)

// removeFile deletes a cache or coverage file. A file that stays behind is no
// longer counted by the size accounting, so a failure is logged.
func removeFile(path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		logf.Printf("[DiskWarmup] remove %s failed: %v", filepath.Base(path), err)
	}
}

func InitDiskWarmup(opts Options) {
	diskQuotaGB = opts.QuotaGB
	FileSize = 64 * 1024 * 1024
	TailWarmupSize = 16 * 1024 * 1024
	headBoundaryChunk, persistTails = true, false

	var dir string
	if opts.Dir != "" {
		// Index mode (Dezoxy fork): independent of GoStorm's piece storage.
		dir = opts.Dir
		if opts.HeadMB > 0 {
			FileSize = opts.HeadMB << 20
		}
		if opts.TailMB > 0 {
			TailWarmupSize = opts.TailMB << 20
		}
		headBoundaryChunk, persistTails = false, true
	} else {
		for i := 0; i < 15; i++ {
			if settings.BTsets != nil {
				break
			}
			time.Sleep(1 * time.Second)
		}
		if settings.BTsets == nil || !settings.BTsets.UseDisk {
			return
		}
		dir = settings.BTsets.TorrentsSavePath
	}
	if dir == "" || dir == "/" {
		return
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return
	}

	DiskWarmup = &DiskWarmupCache{
		dir:     dir,
		writeCh: make(chan warmupWrite, 32),
	}

	// One-shot migration: head files written before the density invariant may hold holes
	// that read back as zeros, and nothing on disk can tell them apart from dense ones.
	// Head warmup is the library-wide instant-start cache, so this must not repeat on
	// every boot - the marker makes it a single cold start per file, once.
	migrated := filepath.Join(dir, denseMarker)
	dropHeads := false
	if _, err := os.Stat(migrated); os.IsNotExist(err) {
		dropHeads = true
	}

	if total, dropped, residues, err := DiskWarmup.scanExisting(dropHeads); err == nil {
		atomic.StoreInt64(&DiskWarmup.totalSize, total)
		logf.Printf("[DiskWarmup] Initial size: %.1fGB (dropped %d stale cache files, %d residues)", float64(total)/(1<<30), dropped, residues)
	}

	if dropHeads {
		if f, err := os.Create(migrated); err == nil {
			_ = f.Close() // an empty marker file
			logf.Printf("[DiskWarmup] Head cache rebuilt for the write-density invariant (one-shot)")
		}
	}

	go DiskWarmup.writeWorker()
	go DiskWarmup.handleReaper()

	logf.Printf("[DiskWarmup] Active — dir=%s quota=%dGB head=%dMB tail=%dMB persistent=%v",
		dir, opts.QuotaGB, FileSize>>20, TailWarmupSize>>20, persistTails)
}

// scanExisting walks the cache at startup. A tail file stays only when persistTails is
// on and its coverage file proves which ranges are real for the current window size;
// otherwise it goes, since the on-disk size of a sparse file proves nothing. Head files
// go when the one-shot density migration runs, and a head below the ready floor goes as
// a residue: the reaper only walks open handles, so the backlog left by a previous
// process would otherwise live until quota pressure and keep passing for warm coverage.
func (d *DiskWarmupCache) scanExisting(dropHeads bool) (total int64, dropped, residues int, err error) {
	dir := d.dir
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0, 0, err
	}
	for _, e := range entries {
		name := e.Name()
		full := filepath.Join(dir, name)
		if strings.HasSuffix(name, coverSuffix) {
			if _, serr := os.Stat(strings.TrimSuffix(full, coverSuffix)); serr != nil {
				removeFile(full) // coverage without its tail file
			}
			continue
		}
		if strings.HasSuffix(name, tailSuffix) {
			if persistTails && d.loadCoverage(full) {
				if info, ierr := e.Info(); ierr == nil {
					total += info.Size()
				}
				continue
			}
			if os.Remove(full) == nil {
				removeFile(full + coverSuffix)
				dropped++
			}
			continue
		}
		if dropHeads && strings.HasSuffix(name, warmupSuffix) {
			if os.Remove(full) == nil {
				dropped++
			}
			continue
		}
		if strings.HasSuffix(name, warmupSuffix) {
			info, ierr := e.Info()
			if ierr != nil {
				continue
			}
			if info.Size() < headReadyFloor {
				if os.Remove(filepath.Join(dir, name)) == nil {
					residues++
				}
				continue
			}
			total += info.Size()
			d.sizeCache.Store(full, sizeEntry{size: info.Size(), updatedAt: time.Now()})
		}
	}
	return total, dropped, residues, nil
}

func (d *DiskWarmupCache) handleReaper() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		d.reapIdle(handleIdleMax)
	}
}

// reapIdle closes handles idle longer than max and drops the head files that never
// grew past the ready floor. Without the drop the residue of an aborted attempt (38
// bytes in production) lives until quota pressure, passes for warm coverage, and
// falsifies every "it has a warmup" reading.
func (d *DiskWarmupCache) reapIdle(max time.Duration) {
	now := time.Now()
	d.handles.Range(func(key, val interface{}) bool {
		path := key.(string)
		ch := val.(*cachedHandle)
		if now.UnixNano()-ch.lastUsedNano.Load() <= max.Nanoseconds() {
			return true
		}
		// V2.0: Use LoadAndDelete + double-check to prevent TOCTOU with getHandle.
		// If getHandle updated lastUsedNano between Range and delete, re-insert.
		if actual, loaded := d.handles.LoadAndDelete(path); loaded {
			ac := actual.(*cachedHandle)
			if now.UnixNano()-ac.lastUsedNano.Load() < max.Nanoseconds() {
				d.handles.Store(path, ac)
				return true
			}
			ac.closed.Store(true)
			_ = ac.f.Close() // never written through this handle
			d.warmupStarts.Delete(path)
			d.dropResidue(path)
		}
		return true
	})
}

// dropResidue removes a head file that stayed below the ready floor: the shape of an
// aborted attempt, not a warmup worth keeping. Tail files are left alone, since their
// readiness already comes from in-memory coverage instead of file size.
func (d *DiskWarmupCache) dropResidue(path string) {
	if !strings.HasSuffix(path, warmupSuffix) {
		return
	}
	// A new attempt may have reopened the file since the reaper decided on the old
	// idle handle: unlinking it now would lose the warmup being written.
	if _, ok := d.handles.Load(path); ok {
		return
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Size() >= headReadyFloor {
		return
	}
	logf.Printf("[DiskWarmup] DROPPED %s (%.1fKB): incomplete head warmup below the ready floor",
		filepath.Base(path), float64(fi.Size())/(1<<10))
	d.sizeCache.Delete(path)
	if os.Remove(path) == nil {
		// Written bytes were counted against the quota, so the drop gives them back
		// instead of leaving the drift to the next full walk in enforceQuotaLocked.
		atomic.AddInt64(&d.totalSize, -fi.Size())
	}
	d.missing.Store(path, time.Now())
}

func (d *DiskWarmupCache) getHandle(path string) (*cachedHandle, error) {
	if val, ok := d.handles.Load(path); ok {
		ch := val.(*cachedHandle)
		ch.lastUsedNano.Store(time.Now().UnixNano())
		return ch, nil
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0644)
	if err != nil {
		return nil, err
	}
	ch := &cachedHandle{f: f}
	ch.lastUsedNano.Store(time.Now().UnixNano())
	if actual, loaded := d.handles.LoadOrStore(path, ch); loaded {
		_ = f.Close() // lost the race; nothing was written through it
		existing := actual.(*cachedHandle)
		existing.lastUsedNano.Store(time.Now().UnixNano())
		return existing, nil
	}
	return ch, nil
}

func (d *DiskWarmupCache) closeHandle(path string) {
	if val, ok := d.handles.LoadAndDelete(path); ok {
		ch := val.(*cachedHandle)
		ch.closed.Store(true) // mark before Close so writeWorker can detect stale handle
		if err := ch.f.Close(); err != nil {
			logf.Printf("[DiskWarmup] close %s failed: %v", filepath.Base(path), err)
		}
	}
	// Aborted warmups (eviction, hash removal, idle close) never reach the
	// completion branch in writeHeadLocked that LoadAndDeletes warmupStarts —
	// clear it here so the entry doesn't leak forever under a rotating cache.
	d.warmupStarts.Delete(path)
}

func (d *DiskWarmupCache) writeWorker() {
	for w := range d.writeCh {
		d.processWrite(w.hash, w.fileID, (*w.buf)[:w.len], w.off)
		warmupWritePool.Put(w.buf)
	}
}

func (d *DiskWarmupCache) WriteChunk(hash string, fileID int, data []byte, off int64) {
	if off > FileSize || d.writeCh == nil {
		return
	}
	if !headBoundaryChunk {
		if off >= FileSize {
			return
		}
		if off+int64(len(data)) > FileSize {
			data = data[:FileSize-off]
		}
	}
	if !d.verified(hash, fileID, off, data, func(buf []byte) { d.writeChunk(hash, fileID, buf, off) }) {
		return
	}
	d.writeChunk(hash, fileID, data, off)
}

// writeChunk queues a head write that already passed the verification gate.
func (d *DiskWarmupCache) writeChunk(hash string, fileID int, data []byte, off int64) {
	bufPtr := warmupWritePool.Get().(*[]byte)
	if len(*bufPtr) < len(data) {
		warmupWritePool.Put(bufPtr)
		buf := make([]byte, len(data))
		copy(buf, data)
		bufPtr = &buf
	} else {
		copy(*bufPtr, data)
	}

	select {
	case d.writeCh <- warmupWrite{hash, fileID, bufPtr, len(data), off}:
	default:
		writeQueueDrops.Add(1)
		warmupWritePool.Put(bufPtr)
	}
}

func (d *DiskWarmupCache) processWrite(hash string, fileID int, data []byte, off int64) {
	if off > FileSize {
		return
	}
	// Only truncate chunks that straddle the boundary from below.
	// Chunks starting AT FileSize are written in full (the boundary chunk).
	if off < FileSize && off+int64(len(data)) > FileSize {
		data = data[:FileSize-off]
	}

	path := d.filePath(hash, fileID)

	// Quota enforcement takes d.mu and then tailMu, so it runs before this write takes
	// tailMu. The check is advisory: a file created or removed meanwhile only changes
	// whether eviction ran, never the accounting.
	// Only a chunk at offset 0 can create the file, so only it may make room for it.
	if _, err := os.Stat(path); off == 0 && os.IsNotExist(err) {
		if err := os.MkdirAll(d.dir, 0755); err != nil {
			logf.Printf("[DiskWarmup] mkdir %s failed: %v", d.dir, err)
		}
		d.mu.Lock()
		evicted, _ := d.enforceQuotaLocked(FileSize, evictOpts{
			exclude: FileKey{hash, fileID}, trigger: TriggerHeadCreate})
		d.mu.Unlock()
		reportEvictions(evicted) // after the locks: it may write to a database
	}

	started, completed := d.writeHeadLocked(path, off, data)
	if started && OnWarmupStateChange != nil {
		OnWarmupStateChange(hash, fileID, true)
	}
	if completed && OnWarmupStateChange != nil {
		OnWarmupStateChange(hash, fileID, false)
	}
}

// writeHeadLocked performs the whole head write - checks, create, WriteAt, size
// bookkeeping - under tailMu, the lock RemoveHash and quota eviction hold while they
// unlink and subtract. A removal therefore lands entirely before or after a write: it
// cannot unlink the file under WriteAt, leave a stale size entry behind, or lose the
// bytes this write added to totalSize. Lock order stays d.mu -> tailMu -> per-file;
// nothing here takes d.mu, and the state callbacks run after the caller releases tailMu.
func (d *DiskWarmupCache) writeHeadLocked(path string, off int64, data []byte) (started, completed bool) {
	d.tailMu.Lock()
	defer d.tailMu.Unlock()

	if val, ok := d.sizeCache.Load(path); ok {
		if entry := val.(sizeEntry); entry.size > FileSize {
			return
		}
	} else if fi, err := os.Stat(path); err == nil && fi.Size() > FileSize {
		d.sizeCache.Store(path, sizeEntry{size: fi.Size(), updatedAt: time.Now()})
		return
	}

	// Head files must stay dense: GetAvailableRange reports their on-disk size as
	// contiguous coverage from 0 (pump start offset, warmup gates, ReadAt), and a hole
	// below that size reads back as zeros, not as a short read. A write that would leave
	// one is dropped - the sequential pump rewrites that range in order anyway. A missing
	// file has size 0, so a chunk past offset 0 never (re)creates it.
	prevSize := d.headSize(path)
	if off > prevSize {
		return
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		// A head file starts at 0. Whatever size the cache still holds for a vanished
		// file is stale; creating the file at off > 0 would hide a hole behind it.
		if off != 0 {
			d.sizeCache.Delete(path)
			return
		}
		d.missing.Delete(path)
		// The new file holds nothing yet: a size entry left by the vanished file would
		// be stored as the new coverage and let a later out-of-order chunk leave a hole.
		prevSize = 0

		f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
		if err != nil {
			logf.Printf("[DiskWarmup] Error creating file: %v", err)
			return
		}
		newCh := &cachedHandle{f: f}
		newCh.lastUsedNano.Store(time.Now().UnixNano())
		d.handles.Store(path, newCh)
		d.warmupStarts.Store(path, time.Now())
		logf.Printf("[DiskWarmup] STARTING %s at offset %d", filepath.Base(path), off)
		started = true
	}

	ch, err := d.getHandle(path)
	if err != nil {
		return
	}
	if ch.closed.Load() {
		logf.Printf("[DiskWarmup] Write skipped: handle closed for %s", filepath.Base(path))
		return
	}

	n, err := ch.f.WriteAt(data, off)
	if err != nil {
		logf.Printf("[DiskWarmup] WriteAt error for %s: %v", filepath.Base(path), err)
		return
	}

	// The file demonstrably exists now, so no stale negative-cache entry may outlive it.
	d.missing.Delete(path)

	// A write overlapping already-covered bytes must not shrink the recorded coverage.
	currentSize := off + int64(n)
	if currentSize > prevSize {
		atomic.AddInt64(&d.totalSize, currentSize-prevSize)
	} else {
		currentSize = prevSize
	}

	d.sizeCache.Store(path, sizeEntry{size: currentSize, updatedAt: time.Now()})

	if off+int64(n) >= FileSize {
		if startVal, ok := d.warmupStarts.LoadAndDelete(path); ok {
			recordWarmupDuration(time.Since(startVal.(time.Time)))
		}
		logf.Printf("[DiskWarmup] COMPLETED %s", filepath.Base(path))
		completed = true
	}
	return
}

func (d *DiskWarmupCache) filePath(hash string, fileID int) string {
	return filepath.Join(d.dir, hash+"-"+strconv.Itoa(fileID)+warmupSuffix)
}

func (d *DiskWarmupCache) tailPath(hash string, fileID int) string {
	return filepath.Join(d.dir, hash+"-"+strconv.Itoa(fileID)+tailSuffix)
}

// GetAvailableRange returns the contiguous bytes cached from offset 0. Callers treat the
// result as coverage (pump start offset, warmup gates, ReadAt clamp), which holds only
// because processWrite refuses writes that would leave a hole.
func (d *DiskWarmupCache) GetAvailableRange(hash string, fileID int) int64 {
	path := d.filePath(hash, fileID)
	if val, ok := d.missing.Load(path); ok {
		// The negative cache must expire: a file absent at one Open is routinely created
		// moments later, and a permanent entry hides a complete warmup file for the rest
		// of the process lifetime (headReady false -> slow resolve path on every Open).
		since := time.Since(val.(time.Time))
		if since < missingTTL {
			return 0
		}
		d.missing.Delete(path)
		if fi, err := os.Stat(path); err == nil && fi.Size() > 0 {
			logf.Printf("[DiskWarmup] Negative cache hid an existing warmup file (%.1fMB, %s) for %s",
				float64(fi.Size())/(1<<20), since.Truncate(time.Second), filepath.Base(path))
		}
	}

	cached, hadEntry := d.sizeCache.Load(path)
	if hadEntry {
		entry := cached.(sizeEntry)
		if time.Since(entry.updatedAt) < 10*time.Second {
			return entry.size
		}
	}

	fi, err := os.Stat(path)
	if err != nil {
		// Only the entry that was seen: a writer may have stored a newer one since.
		if hadEntry {
			d.sizeCache.CompareAndDelete(path, cached)
		}
		d.missing.Store(path, time.Now())
		return 0
	}

	if fi.Size() > FileSize+(16*1024*1024) {
		logf.Printf("[DiskWarmup] CORRUPT CACHE detected (Size: %.1fMB > 128MB) for %s. Removing.", float64(fi.Size())/(1<<20), hash[:8])
		d.closeHandle(path)
		d.sizeCache.Delete(path)
		removeFile(path)
		d.missing.Store(path, time.Now())
		return 0
	}

	// The stat above ran without a lock, so a writer may have stored a newer size
	// meanwhile: replace only the entry that was seen, and otherwise believe the writer.
	fresh := sizeEntry{size: fi.Size(), updatedAt: time.Now()}
	if hadEntry {
		if !d.sizeCache.CompareAndSwap(path, cached, fresh) {
			if cur, ok := d.sizeCache.Load(path); ok {
				return cur.(sizeEntry).size
			}
			return 0 // the entry was deleted meanwhile: the file was removed or evicted
		}
	} else if actual, loaded := d.sizeCache.LoadOrStore(path, fresh); loaded {
		return actual.(sizeEntry).size
	}
	return fi.Size()
}

// HeadReady reports whether the head warmup covers enough to be worth treating the
// file as warm. GetAvailableRange keeps reporting the raw coverage: the pump and the
// read clamps want the number, the Open decision wants the floor.
func (d *DiskWarmupCache) HeadReady(hash string, fileID int) bool {
	if d == nil {
		return false
	}
	return d.GetAvailableRange(hash, fileID) >= headReadyFloor
}

// TailReady reports whether the whole tail window for hash/fileID is on SSD, i.e. Open
// can serve end-of-file probes without the network. Coverage is tracked in memory only:
// the on-disk size of a sparse file says nothing about which ranges hold real data.
func (d *DiskWarmupCache) TailReady(hash string, fileID int) bool {
	if d == nil {
		return false
	}
	val, ok := d.tailCoverage.Load(d.tailPath(hash, fileID))
	return ok && val.(*tailRange).complete()
}

// tailRangeFor returns the coverage tracker for path, creating it if absent.
func (d *DiskWarmupCache) tailRangeFor(path string) *tailRange {
	if val, ok := d.tailCoverage.Load(path); ok {
		return val.(*tailRange)
	}
	val, _ := d.tailCoverage.LoadOrStore(path, &tailRange{})
	return val.(*tailRange)
}

// headSize returns the bytes currently covered in a head warmup file, 0 when absent.
func (d *DiskWarmupCache) headSize(path string) int64 {
	if val, ok := d.sizeCache.Load(path); ok {
		return val.(sizeEntry).size
	}
	if fi, err := os.Stat(path); err == nil {
		return fi.Size()
	}
	return 0
}

func (d *DiskWarmupCache) ReadAt(hash string, fileID int, buf []byte, off int64) (int, error) {
	if off > FileSize {
		return 0, nil
	}
	path := d.filePath(hash, fileID)

	ch, err := d.getHandle(path)
	if err != nil {
		return 0, nil
	}

	availSize := d.GetAvailableRange(hash, fileID)
	if off >= availSize {
		return 0, nil
	}

	if avail := availSize - off; int64(len(buf)) > avail {
		buf = buf[:avail]
	}

	n, err := ch.f.ReadAt(buf, off)
	return n, err
}

func (d *DiskWarmupCache) WriteTail(hash string, fileID int, data []byte, absoluteOffset, fileSize int64) {
	if !d.verified(hash, fileID, absoluteOffset, data, func(buf []byte) { d.writeTail(hash, fileID, buf, absoluteOffset, fileSize) }) {
		return
	}
	d.writeTail(hash, fileID, data, absoluteOffset, fileSize)
}

// writeTail stores a tail write that already passed the verification gate.
func (d *DiskWarmupCache) writeTail(hash string, fileID int, data []byte, absoluteOffset, fileSize int64) {
	path := d.tailPath(hash, fileID)

	tailStart := fileSize - TailWarmupSize
	if tailStart < 0 {
		tailStart = 0
	}
	if absoluteOffset < tailStart {
		return
	}

	tailLen := fileSize - tailStart
	relOffset := absoluteOffset - tailStart
	if relOffset >= tailLen {
		return
	}
	if relOffset+int64(len(data)) > tailLen {
		data = data[:tailLen-relOffset]
	}
	if len(data) == 0 {
		return
	}

	// One tail write at a time, and never beside a removal: two creators would truncate
	// each other's bytes while both record them as covered (Dezoxy fork).
	d.tailMu.Lock()
	defer d.tailMu.Unlock()

	tr := d.tailRangeFor(path)
	tr.setWindow(tailLen)
	if tr.complete() {
		return
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.MkdirAll(d.dir, 0755); err != nil {
			logf.Printf("[DiskWarmup] mkdir %s failed: %v", d.dir, err)
		}
		d.missing.Delete(path)

		f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
		if err != nil {
			return
		}
		// A new file holds nothing yet: coverage from a removed predecessor must not
		// carry over, in memory or on disk.
		d.tailCoverage.Delete(path)
		removeFile(path + coverSuffix)
		tr = d.tailRangeFor(path)
		tr.setWindow(tailLen)
		tailCh := &cachedHandle{f: f}
		tailCh.lastUsedNano.Store(time.Now().UnixNano())
		if old, loaded := d.handles.Swap(path, tailCh); loaded {
			oc := old.(*cachedHandle)
			oc.closed.Store(true)
			if err := oc.f.Close(); err != nil {
				logf.Printf("[DiskWarmup] close replaced handle %s failed: %v", filepath.Base(path), err)
			}
		}
		logf.Printf("[DiskWarmup] TAIL STARTING %s at relOffset %d", filepath.Base(path), relOffset)
	}

	ch, err := d.getHandle(path)
	if err != nil {
		return
	}
	if ch.closed.Load() {
		logf.Printf("[DiskWarmup] Write skipped (tail): handle closed for %s", filepath.Base(path))
		return
	}

	// The quota counts the file's apparent size, as the startup walk does, so the growth
	// is the size change across the write: a rewrite of covered bytes adds nothing.
	// Tail growth is counted here but not enforced: the quota is enforced on the next
	// head creation, so a background tail write never evicts anything itself.
	var prevSize int64
	if fi, serr := ch.f.Stat(); serr == nil {
		prevSize = fi.Size()
	}
	n, _ := ch.f.WriteAt(data, relOffset)
	if n <= 0 {
		return
	}
	if grown := relOffset + int64(n) - prevSize; grown > 0 {
		atomic.AddInt64(&d.totalSize, grown)
	}
	d.sizeCache.Store(path, sizeEntry{size: relOffset + int64(n), updatedAt: time.Now()})
	tr.add(relOffset, relOffset+int64(n))
	if persistTails {
		d.saveCoverage(ch.f, path, tailStart, tailLen, tr)
	}
}

func (d *DiskWarmupCache) ReadTail(hash string, fileID int, buf []byte, absoluteOffset, fileSize int64) (int, error) {
	tailStart := fileSize - TailWarmupSize
	if tailStart < 0 {
		tailStart = 0
	}
	if absoluteOffset < tailStart {
		return 0, nil
	}

	tailLen := fileSize - tailStart
	relOffset := absoluteOffset - tailStart
	path := d.tailPath(hash, fileID)

	// Clamp to the tail window: a buffer overhanging EOF still hits if the bytes it can
	// actually return were written.
	readEnd := relOffset + int64(len(buf))
	if readEnd > tailLen {
		readEnd = tailLen
	}

	// Miss unless this exact range was written. No fallback on the file's on-disk size:
	// the file is sparse, and an unwritten hole reads back as zeros, not as a short read.
	val, ok := d.tailCoverage.Load(path)
	if !ok || !val.(*tailRange).covers(relOffset, readEnd) {
		return 0, nil
	}

	ch, err := d.getHandle(path)
	if err != nil {
		return 0, nil
	}

	n, err := ch.f.ReadAt(buf, relOffset)
	return n, err
}

func (d *DiskWarmupCache) RemoveHash(hash string) {
	d.removeHashFiles(hash)
	if OnHashRemoved != nil {
		OnHashRemoved(hash) // after tailMu: it may write to a database
	}
}

// OnHashRemoved, if set, is called after RemoveHash deleted a torrent's cache files, so
// the state kept per file (played, evicted) follows the torrent out of the library.
var OnHashRemoved func(hash string)

func (d *DiskWarmupCache) removeHashFiles(hash string) {
	d.tailMu.Lock()
	defer d.tailMu.Unlock()
	d.removals.Add(1)
	entries, _ := os.ReadDir(d.dir)
	prefix := hash + "-"
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, prefix) && (strings.HasSuffix(name, warmupSuffix) || strings.HasSuffix(name, tailSuffix)) {
			fullPath := filepath.Join(d.dir, name)

			if fi, err := e.Info(); err == nil {
				atomic.AddInt64(&d.totalSize, -fi.Size())
			}

			d.closeHandle(fullPath)
			d.sizeCache.Delete(fullPath)
			d.tailCoverage.Delete(fullPath)
			removeFile(fullPath)
			removeFile(fullPath + coverSuffix)
		}
	}
}

// quotaBytes is the size limit eviction enforces: the configured quota, or the
// built-in default before one is set.
func quotaBytes() int64 {
	if diskQuotaGB > 0 {
		return diskQuotaGB * 1024 * 1024 * 1024
	}
	return warmupQuota
}

// Counters for /metrics/prometheus. They count since the process started, and only
// quota eviction moves the eviction ones: RemoveHash, dropResidue and the corrupt
// file removal are not evictions.
var (
	evictedHeads, evictedHeadBytes atomic.Int64
	evictedTails, evictedTailBytes atomic.Int64
	// writeQueueDrops counts head chunks dropped because writeCh was full.
	writeQueueDrops atomic.Int64
)

// Stats is a snapshot of the process-wide warmup counters.
type Stats struct {
	QuotaBytes                 int64
	EvictedHeads, EvictedTails int64
	EvictedHeadBytes           int64
	EvictedTailBytes           int64
	WriteQueueDrops            int64
}

// CounterStats returns the process-wide counters; they exist whether or not the
// cache is enabled.
func CounterStats() Stats {
	return Stats{
		QuotaBytes:       quotaBytes(),
		EvictedHeads:     evictedHeads.Load(),
		EvictedTails:     evictedTails.Load(),
		EvictedHeadBytes: evictedHeadBytes.Load(),
		EvictedTailBytes: evictedTailBytes.Load(),
		WriteQueueDrops:  writeQueueDrops.Load(),
	}
}

// TrackedBytes is the running total the quota is checked against. It is
// approximate: the remaining drift sources are listed in docs/ideas.md, and the
// eviction walk resyncs it from the disk. Safe on a nil receiver.
func (d *DiskWarmupCache) TrackedBytes() int64 {
	if d == nil {
		return 0
	}
	return atomic.LoadInt64(&d.totalSize)
}

// TailFetch fetches bytes from the torrent. Wired at startup to the native bridge: the
// warmup package cannot import it, so the dependency is injected like OnWarmupStateChange.
var TailFetch func(hash string, fileID int, off int64, buf []byte) (int, error)

const tailFillChunk = 1024 * 1024 // per-request size of the sequential fill

// tailFillPacing spaces out the fill so it stays a background trickle; a var so tests
// don't have to wait it out.
var tailFillPacing = 250 * time.Millisecond

// EnsureTail fills the whole tail window sequentially in the background so TailReady can
// actually become true. Opportunistic WriteTail only records the ranges a client happened
// to probe, which leaves the MKV Cues index one network round trip away on every open.
//
// Call this only while the file is idle (playback stopped): piece requests at the end of a
// partially downloaded file compete with the sequential download front and stall the
// playhead (observed 2026-08-12).
func (d *DiskWarmupCache) EnsureTail(hash string, fileID int, fileSize int64) {
	if d == nil || TailFetch == nil || hash == "" || fileSize <= 0 {
		return
	}
	if d.TailReady(hash, fileID) {
		return
	}
	path := d.tailPath(hash, fileID)
	if _, busy := d.tailFills.LoadOrStore(path, true); busy {
		return
	}
	if persistTails {
		d.queueIndexTail(path, hash, fileID, fileSize)
		return
	}

	go func() {
		defer d.tailFills.Delete(path)

		tailStart := fileSize - TailWarmupSize
		if tailStart < 0 {
			tailStart = 0
		}
		tailLen := fileSize - tailStart
		tr := d.tailRangeFor(path)
		tr.setWindow(tailLen)

		started := time.Now()
		var fetched int64
		buf := make([]byte, tailFillChunk)
		for rel := int64(0); rel < tailLen; {
			end := rel + tailFillChunk
			if end > tailLen {
				end = tailLen
			}
			if tr.covers(rel, end) {
				rel = end
				continue
			}
			n, err := TailFetch(hash, fileID, tailStart+rel, buf[:end-rel])
			if err != nil || n <= 0 {
				logf.Printf("[DiskWarmup] Tail fill stopped at %.1f/%.1fMB for %s: %v",
					float64(rel)/(1<<20), float64(tailLen)/(1<<20), filepath.Base(path), err)
				return
			}
			d.WriteTail(hash, fileID, buf[:n], tailStart+rel, fileSize)
			fetched += int64(n)
			rel += int64(n)
			time.Sleep(tailFillPacing)
		}
		if d.TailReady(hash, fileID) {
			logf.Printf("[DiskWarmup] TAIL COMPLETE %s (%.1fMB fetched in %s)",
				filepath.Base(path), float64(fetched)/(1<<20), time.Since(started).Truncate(time.Second))
		}
	}()
}

// tailFillRunning reports whether a sequential fill is in flight (test helper).
func (d *DiskWarmupCache) tailFillRunning(hash string, fileID int) bool {
	_, busy := d.tailFills.Load(d.tailPath(hash, fileID))
	return busy
}

// Verification gate (Dezoxy fork). Responsive reads hand out bytes before their piece is
// hashed; a block written to SSD before that could be a corrupt copy served on every
// later open. A block whose pieces are not verified yet waits in the background and is
// written once they are, or dropped.
// A waiter holds no data (it re-reads the range once verified), so a scan burst of a
// few hundred files fits.
const verifyWaiters = 1024

var verifySlots = make(chan struct{}, verifyWaiters)

// How often and how long an unverified block waits for its pieces. Vars so tests don't
// wait them out.
var (
	verifyPollInterval = 500 * time.Millisecond
	verifyWaitMax      = 60 * time.Second
)

// verified reports whether data may be written now. When it may not, a copy waits for
// the pieces in the background and write runs once they verify.
func (d *DiskWarmupCache) verified(hash string, fileID int, off int64, data []byte, write func([]byte)) bool {
	check := RangeVerified
	if check == nil || len(data) == 0 {
		return true
	}
	n := int64(len(data))
	if check(hash, fileID, off, n) {
		return true
	}
	key := hash + "-" + strconv.Itoa(fileID) + "@" + strconv.FormatInt(off, 10) + "+" + strconv.FormatInt(n, 10)
	if _, busy := d.pendingVerify.LoadOrStore(key, true); busy {
		return false
	}
	select {
	case verifySlots <- struct{}{}:
	default:
		d.pendingVerify.Delete(key)
		return false // a scan burst: the next open fetches it again
	}
	gen := d.removals.Load()
	fetch, poll, wait, request := TailFetch, verifyPollInterval, verifyWaitMax, RequestPieces
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logf.Printf("[DiskWarmup] verify waiter panicked: %v", r)
			}
			<-verifySlots
			d.pendingVerify.Delete(key)
		}()
		if request != nil { // off the read path: it takes the client lock
			request(hash, fileID, off, n)
		}
		deadline := time.Now().Add(wait)
		for time.Now().Before(deadline) {
			time.Sleep(poll)
			if !check(hash, fileID, off, n) {
				continue
			}
			if d.removals.Load() != gen {
				return // the file was removed or evicted meanwhile; don't resurrect it
			}
			// The bytes held from the read may predate a failed hash and a re-download:
			// write a fresh copy of the now verified pieces instead.
			if fetch == nil {
				break
			}
			buf := make([]byte, n)
			if got, err := fetch(hash, fileID, off, buf); err != nil || int64(got) != n || !check(hash, fileID, off, n) {
				break
			}
			write(buf)
			return
		}
		atomic.AddInt64(&unverifiedDrops, 1)
	}()
	return false
}

// unverifiedDrops counts blocks never written because their pieces did not verify in time.
var unverifiedDrops int64

// UnverifiedDrops returns that count for /metrics and logs.
func UnverifiedDrops() int64 { return atomic.LoadInt64(&unverifiedDrops) }

// coverFile is the persisted coverage of one tail file. TailStart and TailLen pin the
// window it was written for: a different tail size or file size makes it meaningless.
type coverFile struct {
	TailStart int64      `json:"tail_start"`
	TailLen   int64      `json:"tail_len"`
	Spans     [][2]int64 `json:"spans"`
}

// saveCoverage makes the tail's written ranges durable: the data first, then the
// coverage that vouches for it, replaced atomically.
func (d *DiskWarmupCache) saveCoverage(f *os.File, path string, tailStart, tailLen int64, tr *tailRange) {
	// Snapshot first, then sync: every range in the snapshot was written before the sync,
	// so the cover never vouches for bytes still in the page cache.
	tr.mu.Lock()
	cf := coverFile{TailStart: tailStart, TailLen: tailLen}
	for _, sp := range tr.ranges {
		cf.Spans = append(cf.Spans, [2]int64{sp.start, sp.end})
	}
	tr.mu.Unlock()
	if err := f.Sync(); err != nil {
		return
	}
	raw, err := json.Marshal(cf)
	if err != nil {
		return
	}
	tmp := path + coverSuffix + ".tmp"
	if os.WriteFile(tmp, raw, 0644) != nil {
		return
	}
	if os.Rename(tmp, path+coverSuffix) != nil {
		_ = os.Remove(tmp) // the rename failure leaves the old coverage in place
	}
}

// loadCoverage restores a tail file's coverage from its .cover file. It refuses one
// written for another window size, or one claiming bytes the file does not hold.
func (d *DiskWarmupCache) loadCoverage(path string) bool {
	raw, err := os.ReadFile(path + coverSuffix)
	if err != nil {
		return false
	}
	var cf coverFile
	if json.Unmarshal(raw, &cf) != nil || cf.TailLen <= 0 || cf.TailStart < 0 {
		return false
	}
	if cf.TailLen != TailWarmupSize && !(cf.TailStart == 0 && cf.TailLen < TailWarmupSize) {
		return false
	}
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	tr := &tailRange{window: cf.TailLen}
	for _, sp := range cf.Spans {
		if sp[0] < 0 || sp[1] <= sp[0] || sp[1] > cf.TailLen || sp[1] > fi.Size() {
			return false
		}
		tr.add(sp[0], sp[1])
	}
	if len(tr.ranges) == 0 {
		return false
	}
	d.tailCoverage.Store(path, tr)
	return true
}

// Index-mode tail fills (Dezoxy fork). A library scan opens hundreds of files in
// seconds; filling every tail at once pulls more pieces than the engine's RAM cache
// holds, and a piece evicted before it verifies leaves a hole. One worker fills one
// window at a time: request its whole pieces, wait until they verify, write it whole.

type tailJob struct {
	path, hash string
	fileID     int
	fileSize   int64
}

const tailQueueLen = 8192

func (d *DiskWarmupCache) queueIndexTail(path, hash string, fileID int, fileSize int64) {
	d.tailQueueOnce.Do(func() {
		d.tailQueue = make(chan tailJob, tailQueueLen)
		go d.indexTailWorker()
	})
	select {
	case d.tailQueue <- tailJob{path, hash, fileID, fileSize}:
	default:
		d.tailFills.Delete(path) // full: the next scan of the file queues it again
	}
}

func (d *DiskWarmupCache) indexTailWorker() {
	for job := range d.tailQueue {
		d.fillIndexTail(job)
		d.tailFills.Delete(job.path)
	}
}

// fillIndexTail writes one whole tail window once every piece under it has verified.
func (d *DiskWarmupCache) fillIndexTail(job tailJob) {
	defer func() {
		if r := recover(); r != nil {
			logf.Printf("[DiskWarmup] index tail fill panicked for %s: %v", filepath.Base(job.path), r)
		}
	}()
	if d.TailReady(job.hash, job.fileID) || TailFetch == nil {
		return
	}
	tailStart := job.fileSize - TailWarmupSize
	if tailStart < 0 {
		tailStart = 0
	}
	tailLen := job.fileSize - tailStart
	if RangeVerified != nil {
		if RequestPieces != nil {
			RequestPieces(job.hash, job.fileID, tailStart, tailLen)
		}
		deadline := time.Now().Add(verifyWaitMax)
		for !RangeVerified(job.hash, job.fileID, tailStart, tailLen) {
			if time.Now().After(deadline) {
				atomic.AddInt64(&unverifiedDrops, 1)
				logf.Printf("[DiskWarmup] Tail of %s did not verify in %s", filepath.Base(job.path), verifyWaitMax)
				return
			}
			time.Sleep(verifyPollInterval)
		}
	}
	buf := make([]byte, tailLen)
	n, err := TailFetch(job.hash, job.fileID, tailStart, buf)
	if err != nil || int64(n) != tailLen {
		logf.Printf("[DiskWarmup] Tail fill of %s failed: %d/%d bytes, %v", filepath.Base(job.path), n, tailLen, err)
		return
	}
	// Verified before and after the read: the bytes come from hashed pieces.
	if RangeVerified != nil && !RangeVerified(job.hash, job.fileID, tailStart, tailLen) {
		return
	}
	d.writeTail(job.hash, job.fileID, buf, tailStart, job.fileSize)
	if d.TailReady(job.hash, job.fileID) {
		logf.Printf("[DiskWarmup] TAIL COMPLETE %s (index, %.1fMB)", filepath.Base(job.path), float64(tailLen)/(1<<20))
	}
}
