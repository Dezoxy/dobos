package vfs

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"flodni/internal/metadb"
)

type captureLogger struct{ lines []string }

func (l *captureLogger) Printf(format string, v ...interface{}) {
	l.lines = append(l.lines, fmt.Sprintf(format, v...))
}

// "InodeMap: Loaded N files" is the line operators read the library size from.
func TestInodeMapLoadLine(t *testing.T) {
	db, err := metadb.New(filepath.Join(t.TempDir(), "s.db"), &captureLogger{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.UpsertFile("/lib/movies/a.mkv", strings.Repeat("a", 40), 1, 7); err != nil {
		t.Fatal(err)
	}
	lg := &captureLogger{}
	im := NewInodeMap(filepath.Join(t.TempDir(), "inodes.json"), lg)
	im.SetDB(db)

	if err := im.LoadFromDisk(); err != nil {
		t.Fatal(err)
	}

	want := "InodeMap: Loaded 1 files, 0 dirs from StateDB"
	if len(lg.lines) == 0 || lg.lines[len(lg.lines)-1] != want {
		t.Fatalf("lines = %q, want last %q", lg.lines, want)
	}
}
