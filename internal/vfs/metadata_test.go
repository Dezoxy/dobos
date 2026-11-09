package vfs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Dezoxy fork: a 96 MB SD episode (South Park S22) was filed but could not be opened,
// because video stubs under 100 MiB were rejected as samples.
func TestVideoStubSizeFloor(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []struct {
		size int64
		ok   bool
	}{
		{96122017, true},
		{103131486, true},
		{25 * 1024 * 1024, true},
		{10 * 1024 * 1024, false},
	} {
		path := filepath.Join(dir, fmt.Sprintf("Show_S01E01_%d.mkv", c.size))
		stub := fmt.Sprintf(`{"url":"http://127.0.0.1:8090/stream?link=abc","size":%d,"magnet":"","imdb":""}`, c.size)
		if err := os.WriteFile(path, []byte(stub), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := ReadMetadataFromFile(path)
		if c.ok && err != nil {
			t.Errorf("size %d: %v", c.size, err)
		}
		if !c.ok && !errors.Is(err, ErrInvalidSize) {
			t.Errorf("size %d: want ErrInvalidSize, got %v", c.size, err)
		}
	}
}
