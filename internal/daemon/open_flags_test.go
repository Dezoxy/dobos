package daemon

import (
	"testing"

	"github.com/hanwen/go-fuse/v2/fuse"
)

// cleanActiveHandles removes the test's handles from the process-wide map on cleanup.
func cleanActiveHandles(t *testing.T, hs ...*MkvHandle) {
	t.Helper()
	t.Cleanup(func() {
		for _, h := range hs {
			activeHandles.Delete(h)
		}
	})
}

func TestOpenFlags(t *testing.T) {
	tests := []struct {
		name string
		live bool
		want uint32
	}{
		{"first open of a path invalidates", false, 0},
		{"another handle live keeps the cache", true, fuse.FOPEN_KEEP_CACHE},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := openFlags(tc.live); got != tc.want {
				t.Fatalf("openFlags(%v) = %#x, want %#x", tc.live, got, tc.want)
			}
		})
	}
}

func TestAnyLiveHandleForFlipsWhenAHandleIsStored(t *testing.T) {
	const path = "/test/live-flip.mkv"
	h := &MkvHandle{path: path}
	cleanActiveHandles(t, h)
	if anyLiveHandleFor(path) {
		t.Fatal("live before any handle is stored")
	}
	activeHandles.Store(h, true)
	if !anyLiveHandleFor(path) {
		t.Fatal("not live with a handle stored")
	}
	if anyLiveHandleFor("/test/other.mkv") {
		t.Fatal("a handle on another path counted as live")
	}
	activeHandles.Delete(h)
	if anyLiveHandleFor(path) {
		t.Fatal("still live after the handle was deleted")
	}
}

// The flag is computed before the handle is stored: stored first, every open would see
// itself and the flag would be unconditional, which serves a replaced release's pages.
func TestStoreOpenHandleComputesFlagsBeforeStoring(t *testing.T) {
	const path = "/test/register.mkv"
	first, second, third := &MkvHandle{path: path}, &MkvHandle{path: path}, &MkvHandle{path: path}
	cleanActiveHandles(t, first, second, third)

	if got := storeOpenHandle(first); got != 0 {
		t.Fatalf("first open flags = %#x, want 0", got)
	}
	if got := storeOpenHandle(second); got != fuse.FOPEN_KEEP_CACHE {
		t.Fatalf("second concurrent open flags = %#x, want FOPEN_KEEP_CACHE", got)
	}
	activeHandles.Delete(first)
	activeHandles.Delete(second)
	if got := storeOpenHandle(third); got != 0 {
		t.Fatalf("open after every handle released flags = %#x, want 0", got)
	}
}
