package main

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"flodni/internal/cache"
	"flodni/internal/metadb"
	"flodni/internal/vfs"
)

const (
	inLibHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	orphHash  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func pruneEnv(t *testing.T) (*StartupCacheBuilder, *metadb.DB) {
	t.Helper()
	db, err := metadb.New(filepath.Join(t.TempDir(), "state.db"), discardLogger{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	prev := stateDB
	stateDB = db
	t.Cleanup(func() { stateDB = prev })

	old := time.Now().Add(-time.Hour)
	for _, h := range []string{inLibHash, orphHash} {
		if err := db.RecordMetadataFailure(h, failReasonTimeout, failSourceMount, old); err != nil {
			t.Fatal(err)
		}
	}
	b := NewStartupCacheBuilder(t.TempDir(), cache.NewLRUCache(1<<20, time.Hour), log.New(io.Discard, "", 0))
	b.foundHashes[inLibHash] = true
	return b, db
}

func rowCount(t *testing.T, db *metadb.DB) int {
	t.Helper()
	rows, err := db.ListReadability(0)
	if err != nil {
		t.Fatal(err)
	}
	return len(rows)
}

func TestPruneReadabilityGuards(t *testing.T) {
	tests := []struct {
		name     string
		complete bool
		errors   int
		empty    bool
	}{
		{"incomplete walk", false, 0, false},
		{"walk errors", true, 2, false},
		{"empty stub set", true, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, db := pruneEnv(t)
			b.errors = tt.errors
			if tt.empty {
				b.foundHashes = map[string]bool{}
			}
			b.pruneReadability(tt.complete)
			if got := rowCount(t, db); got != 2 {
				t.Fatalf("rows = %d, want 2: nothing may be deleted", got)
			}
		})
	}
}

func TestPruneReadabilityDeletesOnlyTheOrphanAfterACompleteWalk(t *testing.T) {
	b, db := pruneEnv(t)
	b.pruneReadability(true)
	rows, err := db.ListReadability(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Hash != inLibHash {
		t.Fatalf("rows = %+v, want only the in-library hash", rows)
	}
}

func TestPruneReadabilityNeedsTheStateDatabase(t *testing.T) {
	b, _ := pruneEnv(t)
	stateDB = nil
	b.pruneReadability(true) // must not panic
}

func TestSkippedStubStillLandsInFoundHashes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Movie_2000_1080p_cafe.mkv")
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	mc := cache.NewLRUCache(1<<20, time.Hour)
	mc.Put(path, &vfs.Metadata{URL: "http://engine/stream?link=" + inLibHash + "&index=1&play"}, 100)
	b := NewStartupCacheBuilder(dir, mc, log.New(io.Discard, "", 0))

	b.processDirectory(dir, false)

	if !b.foundHashes[inLibHash] {
		t.Fatalf("foundHashes = %v, want the cached stub's hash", b.foundHashes)
	}
	if b.filesSkipped != 1 || b.filesProcessed != 0 {
		t.Fatalf("skipped %d, processed %d: the stub should have taken the skip path", b.filesSkipped, b.filesProcessed)
	}
}
