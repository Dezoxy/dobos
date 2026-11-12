package torr

import (
	"os"
	"path/filepath"
	"testing"

	sets "flodni/internal/gostorm/settings"
	"flodni/internal/warmup"
)

const removeTestHash = "0123456789abcdef0123456789abcdef01234567"

// warmupFixture points the global warmup cache at a temp dir holding one head and one
// tail file for removeTestHash, and wires a GoStorm DB and an empty BTServer so
// RemTorrent runs end to end.
func warmupFixture(t *testing.T) (head, tail string) {
	t.Helper()
	oldPath, oldRO := sets.Path, sets.ReadOnly
	sets.Path = t.TempDir()
	sets.InitSets(false, false)
	InitApiHelper(NewBTS())

	dir := t.TempDir()
	warmup.InitDiskWarmup(warmup.Options{Dir: dir})
	if warmup.DiskWarmup == nil {
		t.Fatal("warmup cache did not start")
	}
	t.Cleanup(func() {
		warmup.DiskWarmup = nil
		InitApiHelper(nil)
		sets.Path, sets.ReadOnly = oldPath, oldRO
	})

	head = filepath.Join(dir, removeTestHash+"-1.warmup")
	tail = filepath.Join(dir, removeTestHash+"-1.warmup-tail")
	for _, p := range []string{head, tail} {
		if err := os.WriteFile(p, make([]byte, 4096), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return head, tail
}

// Dropping a torrent (idle expiry goes through the same BTServer removal) must keep its
// head and tail cache, since the next play of the release depends on it. An explicit
// removal (the library's rem action) must take them with it, or they linger until quota
// eviction.
func TestWarmupFilesFollowExplicitRemovalOnly(t *testing.T) {
	head, tail := warmupFixture(t)

	DropTorrent(removeTestHash)
	for _, p := range []string{head, tail} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s did not survive DropTorrent: %v", filepath.Base(p), err)
		}
	}

	RemTorrent(removeTestHash)
	for _, p := range []string{head, tail} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s survived RemTorrent (stat err = %v)", filepath.Base(p), err)
		}
	}
}
