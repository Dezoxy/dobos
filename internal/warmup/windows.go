package warmup

import (
	"path/filepath"
	"strings"
)

// WindowState is what the cache holds of one media file's two windows right now.
type WindowState struct {
	// HeadBytes is the contiguous bytes cached from offset 0 (head files are dense).
	// The cache does not know the media file's size, so whether the head is complete
	// is the caller's call: compare it with min(file size, FileSize).
	HeadBytes int64
	// TailBytes is the covered bytes inside the tail window.
	TailBytes int64
	// TailComplete reports that the whole tail window is covered, which is what
	// TailReady answers.
	TailComplete bool
}

// Windows lists the media files that have bytes in either window. It reads two
// in-memory indexes, takes no cache lock and makes no syscall, so it is cheap at tens
// of thousands of files; it is a best-effort snapshot, never a read-path decision.
// Per-file detail stays internal: the exported metrics are aggregates.
func (d *DiskWarmupCache) Windows() map[FileKey]WindowState {
	if d == nil {
		return nil
	}
	out := map[FileKey]WindowState{}
	// The head index ignores its 10 s TTL on purpose: an entry is replaced on every
	// write and deleted when its file is evicted, removed or found missing.
	d.sizeCache.Range(func(k, v any) bool {
		path := k.(string)
		if !strings.HasSuffix(path, warmupSuffix) {
			return true // tail paths keep the end of the last write here, not a size
		}
		size := v.(sizeEntry).size
		if size <= 0 {
			return true
		}
		key := splitCacheName(strings.TrimSuffix(filepath.Base(path), warmupSuffix))
		if key.FileIdx < 0 {
			return true
		}
		ws := out[key]
		ws.HeadBytes = size
		out[key] = ws
		return true
	})
	d.tailCoverage.Range(func(k, v any) bool {
		path := k.(string)
		if !strings.HasSuffix(path, tailSuffix) {
			return true
		}
		tr := v.(*tailRange)
		covered, _ := tr.snapshot()
		if covered <= 0 {
			return true // tailRangeFor makes empty trackers
		}
		key := splitCacheName(strings.TrimSuffix(filepath.Base(path), tailSuffix))
		if key.FileIdx < 0 {
			return true
		}
		ws := out[key]
		ws.TailBytes = covered
		ws.TailComplete = tr.complete()
		out[key] = ws
		return true
	})
	return out
}

// WarmCounts counts the files whose head reached the ready floor (the same bar
// HeadReady uses, so an aborted attempt's residue is not warm) and the files whose tail
// window is fully covered (TailReady). It reads the Windows snapshot, so it costs no
// syscall; nil-safe.
func (d *DiskWarmupCache) WarmCounts() (heads, tails int64) {
	for _, ws := range d.Windows() {
		if ws.HeadBytes >= headReadyFloor {
			heads++
		}
		if ws.TailComplete {
			tails++
		}
	}
	return heads, tails
}
