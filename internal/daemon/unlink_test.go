package daemon

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"syscall"
	"testing"

	"flodni/internal/library"
)

// Unlink hands the path to the library's reference-checked removal and turns
// its answer into an errno. The library tests pin what the removal itself does.
func TestUnlinkFollowsTheLibraryRemoval(t *testing.T) {
	useTestGlobals(t)
	prev := unlinkRemove
	t.Cleanup(func() { unlinkRemove = prev })

	d := &VirtualDirNode{physicalPath: "/state/tv/Show"}
	want := filepath.Join(d.physicalPath, "Show.S01E01_abcd1234.mkv")

	for _, c := range []struct {
		name string
		fn   func(context.Context, string) error
		errn syscall.Errno
	}{
		{"removed", func(context.Context, string) error { return nil }, 0},
		{"outside the media directories",
			func(context.Context, string) error {
				return &library.Error{Status: http.StatusBadRequest, Message: "outside"}
			}, syscall.EPERM},
		{"stub already gone",
			func(context.Context, string) error {
				return &library.Error{Status: http.StatusNotFound, Message: "no stub"}
			}, syscall.ENOENT},
		{"stub delete failed",
			func(context.Context, string) error {
				return &library.Error{Status: http.StatusInternalServerError, Message: "cannot delete"}
			}, syscall.EIO},
		{"a plain error", func(context.Context, string) error { return errors.New("boom") }, syscall.EIO},
		{"not wired", nil, syscall.EIO},
	} {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			unlinkRemove = nil
			if c.fn != nil {
				unlinkRemove = func(ctx context.Context, p string) error {
					got = append(got, p)
					return c.fn(ctx, p)
				}
			}
			if errno := d.Unlink(context.Background(), filepath.Base(want)); errno != c.errn {
				t.Fatalf("errno = %v, want %v", errno, c.errn)
			}
			if c.fn != nil && (len(got) != 1 || got[0] != want) {
				t.Fatalf("unlinkRemove called with %v, want [%s]", got, want)
			}
		})
	}
}

func TestUnlinkRefusesNonMkv(t *testing.T) {
	useTestGlobals(t)
	prev := unlinkRemove
	t.Cleanup(func() { unlinkRemove = prev })
	unlinkRemove = func(context.Context, string) error { t.Error("called"); return nil }
	d := &VirtualDirNode{physicalPath: "/state/tv"}
	if errno := d.Unlink(context.Background(), "x.nfo"); errno != syscall.EPERM {
		t.Fatalf("errno = %v", errno)
	}
}
