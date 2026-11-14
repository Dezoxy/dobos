package vfs

import (
	"bytes"
	"flodni/internal/flog"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"flodni/internal/metadb"
)

// "InodeMap: Loaded N files" is the line operators read the library size from.
func TestInodeMapLoadLine(t *testing.T) {
	db, err := metadb.New(filepath.Join(t.TempDir(), "s.db"), flog.NewText(io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.UpsertFile("/lib/movies/a.mkv", strings.Repeat("a", 40), 1, 7); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	im := NewInodeMap(filepath.Join(t.TempDir(), "inodes.json"), flog.NewText(&buf))
	im.SetDB(db)

	if err := im.LoadFromDisk(); err != nil {
		t.Fatal(err)
	}

	want := "InodeMap: Loaded 1 files, 0 dirs from StateDB"
	if !strings.Contains(buf.String(), want+"\n") {
		t.Fatalf("log = %q, want %q", buf.String(), want)
	}
}
