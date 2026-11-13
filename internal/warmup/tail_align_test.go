package warmup

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

// The kernel reads whole 4 KiB pages, so the tail window starts on a page boundary:
// a read of the last N MiB of a file that is not page-aligned stays inside it.

func TestTailStart(t *testing.T) {
	old := TailWarmupSize
	t.Cleanup(func() { TailWarmupSize = old })
	TailWarmupSize = 1 << 20
	const mib = int64(1 << 20)
	tests := []struct {
		name string
		size int64
		want int64
	}{
		{"aligned size", 10 * mib, 9 * mib},
		{"unaligned, start inside the page", 10*mib + 100, 9 * mib},
		{"unaligned, start on the next page", 10*mib + 5000, 9*mib + 4096},
		{"just below a page", 10*mib + 4095, 9 * mib},
		{"exactly the window", mib, 0},
		{"one byte over the window", mib + 1, 0},
		{"shorter than the window", 1000, 0},
		{"empty", 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TailStart(tt.size)
			if got != tt.want {
				t.Fatalf("TailStart(%d) = %d, want %d", tt.size, got, tt.want)
			}
			if got%4096 != 0 || (tt.size > mib && tt.size-got-mib >= 4096) {
				t.Fatalf("TailStart(%d) = %d is not the page below size-window", tt.size, got)
			}
		})
	}
}

func TestTailWrittenAndReadAtTheAlignedStart(t *testing.T) {
	d := indexCache(t, t.TempDir())
	const fileSize = 10<<20 + 1000
	start := TailStart(fileSize)
	if start != 9<<20 {
		t.Fatalf("TailStart = %d", start)
	}
	window := bytes.Repeat([]byte{3}, int(fileSize-start))
	window[0] = 7 // the leading page starts with its own byte
	d.WriteTail("h", 1, window, start, fileSize)
	if !d.TailReady("h", 1) {
		t.Fatal("the whole aligned window was written but is not ready")
	}
	buf := make([]byte, 4096)
	if n, _ := d.ReadTail("h", 1, buf, start, fileSize); n != 4096 || buf[0] != 7 {
		t.Fatalf("read of the first page of the window: n=%d first=%d", n, buf[0])
	}
	// A read of exactly the last window-size bytes starts a page-fraction inside it.
	last := make([]byte, fileSize-(fileSize-TailWarmupSize))
	if n, _ := d.ReadTail("h", 1, last, fileSize-TailWarmupSize, fileSize); n != len(last) {
		t.Fatalf("read of the last %d bytes: n=%d", len(last), n)
	}
	// The page before the window is not cached.
	if n, _ := d.ReadTail("h", 1, buf, start-4096, fileSize); n != 0 {
		t.Fatalf("a read below the window was served: n=%d", n)
	}

	// The aligned window survives a restart.
	restarted := &DiskWarmupCache{dir: d.dir}
	if _, dropped, _, err := restarted.scanExisting(false); err != nil || dropped != 0 {
		t.Fatalf("scan: dropped=%d err=%v", dropped, err)
	}
	if !restarted.TailReady("h", 1) {
		t.Fatal("aligned tail lost across a restart")
	}
	if n, _ := restarted.ReadTail("h", 1, buf, start, fileSize); n != 4096 || buf[0] != 7 {
		t.Fatalf("restored read of the first page: n=%d first=%d", n, buf[0])
	}
}

// A cover written before the window was aligned: the window is the exact configured
// size from size-TailWarmupSize, and the file's bytes are laid out from that start.
func TestOldFormatCoverStillLoadsAndServes(t *testing.T) {
	dir := t.TempDir()
	d := indexCache(t, dir)
	const fileSize = 10<<20 + 1000
	oldStart := fileSize - TailWarmupSize
	data := bytes.Repeat([]byte{5}, int(TailWarmupSize))
	data[0] = 9
	path := d.tailPath("h", 4)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(coverFile{TailStart: oldStart, TailLen: TailWarmupSize,
		Spans: [][2]int64{{0, TailWarmupSize}}})
	if err := os.WriteFile(path+coverSuffix, raw, 0644); err != nil {
		t.Fatal(err)
	}

	restarted := &DiskWarmupCache{dir: dir}
	_, dropped, _, err := restarted.scanExisting(false)
	if err != nil || dropped != 0 {
		t.Fatalf("an old-format cover was dropped: dropped=%d err=%v", dropped, err)
	}
	// An old cover covers the old exact window completely, so it is ready.
	if !restarted.TailReady("h", 4) {
		t.Fatal("an old-format cover must count as complete")
	}
	buf := make([]byte, 4096)
	if n, _ := restarted.ReadTail("h", 4, buf, oldStart, fileSize); n != 4096 || buf[0] != 9 {
		t.Fatalf("read at the old window start: n=%d first=%d", n, buf[0])
	}
	// The leading page of the aligned window is not in the old file: engine as today.
	if n, _ := restarted.ReadTail("h", 4, buf, TailStart(fileSize), fileSize); n != 0 {
		t.Fatalf("the leading page was served from an old cover: n=%d", n)
	}
	// A write of the leading page must not move the old bytes.
	restarted.writeTail("h", 4, bytes.Repeat([]byte{1}, 4096), TailStart(fileSize), fileSize)
	if n, _ := restarted.ReadTail("h", 4, buf, oldStart, fileSize); n != 4096 || buf[0] != 9 {
		t.Fatalf("old bytes damaged by a leading-page write: n=%d first=%d", n, buf[0])
	}
	if !restarted.TailReady("h", 4) {
		t.Fatal("the cover stopped being ready after a leading-page write")
	}
}

func TestCoverForAnotherWindowIsDropped(t *testing.T) {
	dir := t.TempDir()
	d := indexCache(t, dir)
	// Start and length that fit neither the old window nor the aligned one.
	for i, cf := range []coverFile{
		{TailStart: 9<<20 + 1, TailLen: TailWarmupSize + 5, Spans: [][2]int64{{0, 100}}},
		{TailStart: 9 << 20, TailLen: TailWarmupSize + 4096, Spans: [][2]int64{{0, 100}}},
		{TailStart: 9<<20 + 4096, TailLen: TailWarmupSize + 4096, Spans: [][2]int64{{0, 100}}},
	} {
		path := d.tailPath("x", i)
		if err := os.WriteFile(path, make([]byte, 4096), 0644); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(cf)
		if err := os.WriteFile(path+coverSuffix, raw, 0644); err != nil {
			t.Fatal(err)
		}
	}
	restarted := &DiskWarmupCache{dir: dir}
	if _, dropped, _, _ := restarted.scanExisting(false); dropped != 3 {
		t.Fatalf("dropped=%d, want 3", dropped)
	}
}

// An old cover that is not complete keeps filling its own window: the bytes are laid out
// from the old start, so the writes must land there.
func TestOldFormatPartialCoverIsCompletedAtItsOwnStart(t *testing.T) {
	dir := t.TempDir()
	d := indexCache(t, dir)
	const fileSize = 10<<20 + 1000
	oldStart := fileSize - TailWarmupSize
	half := TailWarmupSize / 2
	path := d.tailPath("h", 6)
	if err := os.WriteFile(path, bytes.Repeat([]byte{5}, int(half)), 0644); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(coverFile{TailStart: oldStart, TailLen: TailWarmupSize,
		Spans: [][2]int64{{0, half}}})
	if err := os.WriteFile(path+coverSuffix, raw, 0644); err != nil {
		t.Fatal(err)
	}
	restarted := &DiskWarmupCache{dir: dir}
	if _, _, _, err := restarted.scanExisting(false); err != nil {
		t.Fatal(err)
	}
	if restarted.TailReady("h", 6) {
		t.Fatal("half a window is not ready")
	}
	restarted.writeTail("h", 6, bytes.Repeat([]byte{6}, int(half)), oldStart+half, fileSize)
	if !restarted.TailReady("h", 6) {
		t.Fatal("the second half did not complete the old window")
	}
	buf := make([]byte, 4096)
	if n, _ := restarted.ReadTail("h", 6, buf, oldStart+half, fileSize); n != 4096 || buf[0] != 6 {
		t.Fatalf("second half: n=%d first=%d", n, buf[0])
	}
	if n, _ := restarted.ReadTail("h", 6, buf, oldStart, fileSize); n != 4096 || buf[0] != 5 {
		t.Fatalf("first half: n=%d first=%d", n, buf[0])
	}
}
