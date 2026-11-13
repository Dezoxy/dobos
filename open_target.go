package main

import (
	"flodni/internal/gostorm/torr/state"
	"flodni/internal/library"
	"flodni/internal/promtext"
	"flodni/internal/warmup"
	"sync"
	"sync/atomic"

	"github.com/anacrolix/torrent/metainfo"

	"flodni/internal/gostorm/web"
)

// Index drift (Dezoxy fork): a stub names its file by the index the engine gave it when
// the stub was written. An older stub, or an engine that numbered the files another way,
// can leave that index naming a different file. These helpers decide which file an open
// serves, and count the stubs found stale.

// engineFileList is the resident torrent's files in the engine's own id order, nil when
// the torrent is not resident. A var so a test can stand in the engine.
var engineFileList = residentFiles

func residentFiles(hash string) []library.TorrentFile {
	t := web.BTS.GetTorrent(metainfo.NewHashFromHex(hash))
	if t == nil {
		return nil
	}
	return filesFromStats(t.Status().FileStats)
}

// filesFromStats takes the ids and the order from the engine's own list (Status()'s
// FileStats, the list the engine reader resolves ids through). A fresh sort need not
// reproduce it: paths that tie in the comparison, such as 1.mkv and 01.mkv, can come out
// the other way round.
func filesFromStats(stats []*state.TorrentFileStat) []library.TorrentFile {
	files := make([]library.TorrentFile, 0, len(stats))
	for _, s := range stats {
		if s != nil {
			files = append(files, library.TorrentFile{Index: s.Id, Path: s.Path, Length: s.Length})
		}
	}
	return files
}

// openTarget decides the (hash, file index) an open serves, and whether the warm head
// under it is ready; err is set when nothing could be resolved (the open then relies on
// the cache and a retry). headReady says a head exists under the stub's own index.
//
// With a head under the stub's index the open used to trust the stub and never resolve,
// so a stale index served whichever file owned that head. Now:
//   - torrent resident: the stub is checked against the engine's list. On disagreement the
//     resolved pair wins and readiness is recomputed for it.
//   - torrent closed: nothing to check against, so the shortcut stays, unless the head is
//     longer than the stub's file, which only another file's head can be.
func openTarget(url string, size int64, path, hashStr string, urlIdx int, headReady bool) (hash string, idx int, ready bool, err error) {
	if headReady && hashStr != "" {
		if len(engineFileList(hashStr)) > 0 {
			h, i, rerr := resolveTargetFile(url, size, path)
			if rerr != nil || (h == hashStr && i == urlIdx) {
				return hashStr, urlIdx, true, nil
			}
			noteStubIndexMismatch(path, hashStr, urlIdx, i)
			return h, i, warmup.DiskWarmup.HeadReady(h, i), nil
		}
		if warmup.DiskWarmup.GetAvailableRange(hashStr, urlIdx) <= size {
			return hashStr, urlIdx, true, nil
		}
	}
	h, i, err := resolveTargetFile(url, size, path)
	if err == nil && hashStr != "" && urlIdx > 0 && i != urlIdx {
		noteStubIndexMismatch(path, hashStr, urlIdx, i)
	}
	return h, i, false, err
}

// stubIndexMismatches counts the paths whose stub index differs from the file the open
// resolved, once per path (the same path opens many times).
var stubIndexMismatches stubIndexTripwire

type stubIndexTripwire struct {
	seen  sync.Map // path -> struct{}
	total atomic.Int64
}

func (s *stubIndexTripwire) reset() {
	s.seen.Clear()
	s.total.Store(0)
}

// noteStubIndexMismatch logs and counts the first open of a path whose stub index is
// not the file the open resolved.
func noteStubIndexMismatch(path, hash string, stubIdx, resolvedIdx int) {
	if _, dup := stubIndexMismatches.seen.LoadOrStore(path, struct{}{}); dup {
		return
	}
	stubIndexMismatches.total.Add(1)
	logger.Printf("WARNING: stub index: %s names file %d of %s but resolves to file %d; the stub is stale (remove it from the library and add it again)",
		path, stubIdx, hash, resolvedIdx)
}

func collectStubIndexProm(w *promtext.Writer) {
	w.Counter("flodni_stub_index_mismatch_total",
		"Library paths whose stub file index differs from the file the open resolved by size and name, counted once per path per process. Non-zero means a stale stub: repair it with library remove and add.",
		promSample(stubIndexMismatches.total.Load()))
}
