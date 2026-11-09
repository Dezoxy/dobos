package main

import (
	"context"
	"errors"
	"testing"
)

// Dezoxy fork: a read on a handle whose torrent the engine closed as idle wakes it
// again; a live torrent, or a handle with nothing to wake it with, is left alone.
func TestRewakeOnlyAClosedTorrent(t *testing.T) {
	type call struct {
		magnet string
		fileID int
	}
	var calls []call
	wake := func(_ context.Context, magnet string, fileID int) error {
		calls = append(calls, call{magnet, fileID})
		return nil
	}
	closed := func(string) bool { return false }
	live := func(string) bool { return true }
	ctx := context.Background()

	woke, err := rewake(ctx, "abc", "magnet:?xt=urn:btih:abc", 3, closed, wake)
	if !woke || err != nil {
		t.Fatalf("closed torrent: woke=%v err=%v, want woken", woke, err)
	}
	if len(calls) != 1 || calls[0] != (call{"magnet:?xt=urn:btih:abc", 3}) {
		t.Fatalf("wake calls = %v", calls)
	}

	calls = nil
	for name, c := range map[string]struct {
		hash, magnet string
		live         func(string) bool
		wake         func(context.Context, string, int) error
	}{
		"live torrent": {"abc", "magnet:?xt=urn:btih:abc", live, wake},
		"no hash":      {"", "magnet:?xt=urn:btih:abc", closed, wake},
		"no magnet":    {"abc", "", closed, wake},
		"no wake":      {"abc", "magnet:?xt=urn:btih:abc", closed, nil},
	} {
		if woke, err := rewake(ctx, c.hash, c.magnet, 0, c.live, c.wake); woke || err != nil {
			t.Errorf("%s: woke=%v err=%v, want neither", name, woke, err)
		}
	}
	if len(calls) != 0 {
		t.Fatalf("woke a torrent it should have left alone: %v", calls)
	}

	failing := func(context.Context, string, int) error { return errors.New("semaphore exhausted") }
	if woke, err := rewake(ctx, "abc", "magnet:?xt=urn:btih:abc", 0, closed, failing); woke || err == nil {
		t.Fatalf("failed wake: woke=%v err=%v, want the error", woke, err)
	}
}

// A hash the engine has never seen is not live.
func TestTorrentLiveForAnUnknownHash(t *testing.T) {
	if torrentLive("0000000000000000000000000000000000000000") {
		t.Fatal("unknown torrent reported live")
	}
}
