package main

import (
	"bytes"
	"errors"
	"log"
	"strings"
	"testing"

	"flodni/internal/gostorm/torr/state"
	"flodni/internal/library"
	"flodni/internal/promtext"
	"flodni/internal/warmer"
	"flodni/internal/warmup"
)

// Index drift (Readiness, "Never serve another file's bytes"): a stub whose index
// is stale must not be served another file's head.

// useEngineFiles makes the engine report files for hash (nil: not resident).
func useEngineFiles(t *testing.T, files []library.TorrentFile) {
	t.Helper()
	prev := engineFileList
	engineFileList = func(string) []library.TorrentFile { return files }
	t.Cleanup(func() { engineFileList = prev })
}

func useMismatchSeen(t *testing.T) {
	t.Helper()
	stubIndexMismatches.reset()
	t.Cleanup(stubIndexMismatches.reset)
}

// writeHead puts n bytes of head under (hash, idx) and waits until they are readable.
func writeHead(t *testing.T, hash string, idx int, n int) {
	t.Helper()
	warmup.DiskWarmup.WriteChunk(hash, idx, wantPattern(n, 0), 0)
	waitUntil(t, "the head write", func() bool { return warmup.DiskWarmup.GetAvailableRange(hash, idx) >= int64(n) })
}

// captureLogs swaps the logger for a buffer and returns a reader of it.
func captureLogs(t *testing.T) func() string {
	t.Helper()
	var buf bytes.Buffer
	prev := logger
	logger = log.New(&buf, "", 0)
	t.Cleanup(func() { logger = prev })
	return buf.String
}

func staleStubFiles() []library.TorrentFile {
	return []library.TorrentFile{
		{Index: 1, Path: "a.mkv", Length: 300 * mb},
		{Index: 2, Path: "b.mkv", Length: 500 * mb},
	}
}

func TestOpenTargetResidentTorrentOverridesAStaleWarmHead(t *testing.T) {
	useEngineReadPath(t)
	useMismatchSeen(t)
	hash := useDiskWarmup(t)
	useEngineFiles(t, staleStubFiles())
	writeHead(t, hash, 2, int(mb)) // the sibling's head, under the stale stub's index
	url := "http://x/stream?link=" + hash + "&index=2"

	h, idx, ready, err := openTarget(url, 300*mb, "/lib/a.mkv", hash, 2, true)
	if err != nil || h != hash || idx != 1 {
		t.Fatalf("served (%s, %d, err=%v), want file 1: the stub says 2 but the size is file 1's", h, idx, err)
	}
	if ready {
		t.Error("the sibling's head was taken for the resolved file's head")
	}
	if got := stubIndexMismatches.total.Load(); got != 1 {
		t.Errorf("mismatch count = %d, want 1", got)
	}
}

func TestOpenTargetResidentTorrentKeepsAnAgreeingWarmHead(t *testing.T) {
	useEngineReadPath(t)
	useMismatchSeen(t)
	hash := useDiskWarmup(t)
	useEngineFiles(t, staleStubFiles())
	writeHead(t, hash, 1, int(mb))
	url := "http://x/stream?link=" + hash + "&index=1"

	_, idx, ready, err := openTarget(url, 300*mb, "/lib/a.mkv", hash, 1, true)
	if err != nil || idx != 1 || !ready {
		t.Fatalf("got idx=%d err=%v ready=%v, want 1 true true", idx, err, ready)
	}
	if got := stubIndexMismatches.total.Load(); got != 0 {
		t.Errorf("mismatch count = %d on a correct stub", got)
	}
}

func TestOpenTargetClosedTorrentRefusesAHeadLongerThanTheFile(t *testing.T) {
	useEngineReadPath(t)
	useMismatchSeen(t)
	hash := useDiskWarmup(t)
	useEngineFiles(t, nil) // closed
	writeHead(t, hash, 2, 3*int(mb))
	url := "http://x/stream?link=" + hash + "&index=2"

	// The stub's file is 2 MiB: a 3 MiB head under its index is another file's.
	_, idx, ready, err := openTarget(url, 2*mb, "/lib/a.mkv", hash, 2, true)
	if ready {
		t.Error("a head longer than the stub's file was trusted")
	}
	if err != nil || idx != 2 {
		t.Errorf("got idx=%d err=%v, want the stub's pair as the fallback", idx, err)
	}
}

func TestOpenTargetClosedTorrentKeepsTheShortcutForAPlausibleHead(t *testing.T) {
	useEngineReadPath(t)
	useMismatchSeen(t)
	hash := useDiskWarmup(t)
	useEngineFiles(t, nil)
	writeHead(t, hash, 2, int(mb))
	url := "http://x/stream?link=" + hash + "&index=2"

	_, idx, ready, err := openTarget(url, 500*mb, "/lib/b.mkv", hash, 2, true)
	if err != nil || idx != 2 || !ready {
		t.Fatalf("got idx=%d err=%v ready=%v, want 2 true true", idx, err, ready)
	}
}

func TestStubIndexMismatchCountsOncePerPath(t *testing.T) {
	useMismatchSeen(t)
	logs := captureLogs(t)
	noteStubIndexMismatch("/lib/a.mkv", "aa", 2, 1)
	noteStubIndexMismatch("/lib/a.mkv", "aa", 2, 1)
	if got := stubIndexMismatches.total.Load(); got != 1 {
		t.Fatalf("count = %d after two opens of one path, want 1", got)
	}
	noteStubIndexMismatch("/lib/b.mkv", "aa", 3, 1)
	if got := stubIndexMismatches.total.Load(); got != 2 {
		t.Fatalf("count = %d after a second path, want 2", got)
	}
	if n := strings.Count(logs(), "stub index"); n != 2 {
		t.Errorf("logged %d lines, want one per path (2)", n)
	}
}

func TestStubIndexMismatchSeriesIsPresentAtZero(t *testing.T) {
	useMismatchSeen(t)
	var w promtext.Writer
	collectStubIndexProm(&w)
	if err := promtext.Lint(w.Bytes()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(w.Bytes()), "flodni_stub_index_mismatch_total 0\n") {
		t.Fatalf("series missing or not 0:\n%s", w.Bytes())
	}
}

func TestFilesFromStatsKeepsTheEnginesIds(t *testing.T) {
	// 01.mkv and 1.mkv tie in the engine's comparison: whichever order the
	// engine cached, its ids are the ones an open must use.
	for _, order := range [][]string{{"01.mkv", "1.mkv"}, {"1.mkv", "01.mkv"}} {
		stats := []*state.TorrentFileStat{
			{Id: 1, Path: order[0], Length: 10},
			{Id: 2, Path: order[1], Length: 20},
			{Id: 3, Path: "z.mkv", Length: 30},
		}
		got := filesFromStats(stats)
		want := []library.TorrentFile{
			{Index: 1, Path: order[0], Length: 10},
			{Index: 2, Path: order[1], Length: 20},
			{Index: 3, Path: "z.mkv", Length: 30},
		}
		if len(got) != len(want) {
			t.Fatalf("order %v: got %v", order, got)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("order %v: file %d = %+v, want %+v", order, i, got[i], want[i])
			}
		}
	}
}

func TestResolveTargetFileLowerCasesTheHash(t *testing.T) {
	useEngineReadPath(t)
	useEngineFiles(t, staleStubFiles())
	upper := strings.ToUpper(testHash)
	h, idx, err := resolveTargetFile("http://x/stream?link="+upper+"&index=2", 300*mb, "/lib/a.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if h != testHash || idx != 1 {
		t.Errorf("got (%s, %d), want (%s, 1)", h, idx, testHash)
	}
}

func TestWarmFillRefusesAStubSizeThatIsNotTheEnginesFile(t *testing.T) {
	x := warmIndex(t)
	deps := addWarmerDeps(x, make(chan struct{}))
	prev := engineFileLength
	t.Cleanup(func() { engineFileLength = prev })

	engineFileLength = func(string, int) (int64, bool) { return 999, true }
	_, err := deps.Fill(warmup.FillRequest{Key: warmup.FileKey{Hash: "aa", FileIdx: 2}, FileSize: 1000})
	if !errors.Is(err, warmer.ErrIndexMismatch) {
		t.Fatalf("a size mismatch gave %v, want ErrIndexMismatch", err)
	}

	// Same length, and not resident: the guard lets the fill through (the cache is not
	// wired in this test, which is what answers).
	for name, fn := range map[string]func(string, int) (int64, bool){
		"agrees":       func(string, int) (int64, bool) { return 1000, true },
		"not resident": func(string, int) (int64, bool) { return 0, false },
	} {
		engineFileLength = fn
		_, err := deps.Fill(warmup.FillRequest{Key: warmup.FileKey{Hash: "aa", FileIdx: 2}, FileSize: 1000})
		if errors.Is(err, warmer.ErrIndexMismatch) {
			t.Errorf("%s: refused: %v", name, err)
		}
	}
}
