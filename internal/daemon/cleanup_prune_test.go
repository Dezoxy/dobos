package daemon

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"flodni/internal/cache"
	"flodni/internal/vfs"
)

func cleanupInodeEnv(t *testing.T, root string) (*CleanupManager, *vfs.InodeMap) {
	t.Helper()
	im := vfs.NewInodeMap(filepath.Join(t.TempDir(), "inodes.json"), &vfsLogger{log.New(io.Discard, "", 0)})
	prev := globalInodeMap
	globalInodeMap = im
	t.Cleanup(func() { globalInodeMap = prev })
	im.AddFile(filepath.Join(root, "movies", "a.mkv"), inLibHash, 1)
	im.AddFile(filepath.Join(root, "movies", "ghost.mkv"), orphHash, 1)
	cm := NewCleanupManager(log.New(io.Discard, "", 0), cache.NewLRUCache(1<<20, time.Hour), nil)
	return cm, im
}

func TestPeriodicInodePruneSkippedWhenTheLibraryIsEmptyOrUnmounted(t *testing.T) {
	tests := []struct {
		name  string
		build func(root string)
	}{
		{"root without movies and tv", func(string) {}},
		{"movies and tv present but empty", func(root string) {
			for _, d := range []string{"movies", "tv"} {
				if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
					t.Fatal(err)
				}
			}
		}},
		{"stubs under movies only", func(root string) {
			writeStub(t, filepath.Join(root, "movies", "a.mkv"), inLibHash, 1<<30)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			tt.build(root)
			cm, im := cleanupInodeEnv(t, root)
			if n := cm.pruneInodes(root); n != 0 {
				t.Fatalf("pruned %d, want 0", n)
			}
			if got := inodeFiles(im); got != 2 {
				t.Fatalf("files = %d, want 2: nothing may be pruned", got)
			}
		})
	}
}

func TestPeriodicInodePruneDropsTheGhostAfterAWholeWalk(t *testing.T) {
	root := t.TempDir()
	writeStub(t, filepath.Join(root, "movies", "a.mkv"), inLibHash, 1<<30)
	writeStub(t, filepath.Join(root, "tv", "Show", "S01", "e.mkv"), inLibHash, 1<<30)
	cm, im := cleanupInodeEnv(t, root)
	if n := cm.pruneInodes(root); n != 1 {
		t.Fatalf("pruned %d, want 1", n)
	}
	if got := inodeFiles(im); got != 1 {
		t.Fatalf("files = %d, want 1", got)
	}
}
