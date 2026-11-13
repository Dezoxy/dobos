package library

import (
	"errors"
	"testing"
)

func TestResolveOpenTargetCorrectStubWinsOverSameSizeSibling(t *testing.T) {
	files := []TorrentFile{{Index: 1, Path: "2.mkv", Length: 100}, {Index: 2, Path: "12.mkv", Length: 100}}
	got, err := ResolveOpenTarget("aa", 2, 100, "/lib/Show_S01E12_aaaaaaaa.mkv", files)
	if err != nil || got.FileIndex != 2 {
		t.Fatalf("got %+v, %v; want index 2, verified", got, err)
	}
}

func TestResolveOpenTargetNoFileOfTheSizeIsAMismatch(t *testing.T) {
	files := []TorrentFile{{Index: 1, Path: "a.mkv", Length: 100}}
	if _, err := ResolveOpenTarget("aa", 1, 7, "/lib/x.mkv", files); !errors.Is(err, ErrStubMismatch) {
		t.Fatalf("err = %v, want ErrStubMismatch", err)
	}
	got, err := ResolveOpenTarget("aa", 1, 7, "/lib/x.mkv", nil)
	if err != nil || got.FileIndex != 1 {
		t.Errorf("no list: got %+v, %v; want the stub's index", got, err)
	}
}
