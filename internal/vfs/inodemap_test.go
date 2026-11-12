package vfs

import (
	"io"
	"log"
	"path/filepath"
	"testing"
)

func TestLibraryHashCountIsDistinctAndCaseFolded(t *testing.T) {
	im := NewInodeMap(filepath.Join(t.TempDir(), "inode.json"), log.New(io.Discard, "", 0))
	if got := im.LibraryHashCount(); got != 0 {
		t.Fatalf("empty map counts %d", got)
	}
	im.AddFile("/m/a/e1.mkv", "AAAA", 0)
	im.AddFile("/m/a/e2.mkv", "aaaa", 1) // same torrent, second file
	im.AddFile("/m/b/f.mkv", "bbbb", 0)
	if got := im.LibraryHashCount(); got != 2 {
		t.Fatalf("count = %d, want 2", got)
	}
	im.RemoveFile("/m/b/f.mkv")
	if got := im.LibraryHashCount(); got != 1 {
		t.Fatalf("after remove count = %d, want 1", got)
	}
}
