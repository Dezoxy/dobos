package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func TestWriteFileCreatesAndReplaces(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.json")

	if err := WriteFile(path, []byte("one"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("two"), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "two" {
		t.Fatalf("content = %q, %v; want two", got, err)
	}
	if n := names(t, dir); len(n) != 1 || n[0] != "a.json" {
		t.Fatalf("directory holds %v, want only a.json", n)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0644 {
		t.Fatalf("mode = %v, want 0644", fi.Mode().Perm())
	}
}

func TestWriteFileFailureLeavesTargetAndNoTemp(t *testing.T) {
	dir := t.TempDir()
	// A directory at the target makes the rename fail after the data is written.
	target := filepath.Join(dir, "stub.mkv")
	if err := os.Mkdir(target, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "keep"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(target, []byte("new"), 0644); err == nil {
		t.Fatal("expected an error renaming over a directory")
	}
	if n := names(t, dir); len(n) != 1 || n[0] != "stub.mkv" {
		t.Fatalf("directory holds %v, want only stub.mkv", n)
	}
	if _, err := os.Stat(filepath.Join(target, "keep")); err != nil {
		t.Fatalf("original content lost: %v", err)
	}
}

func TestWriteFileMissingDirectory(t *testing.T) {
	if err := WriteFile(filepath.Join(t.TempDir(), "nope", "f"), []byte("x"), 0644); err == nil {
		t.Fatal("expected an error for a missing directory")
	}
}
