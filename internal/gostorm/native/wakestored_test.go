package native

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Hard rule 3. WakeStored is how warm on add revives a closed torrent. It may only do
// so for a hash the engine still stores: the stored record keeps the trackers and the
// private flag, and a bare magnet for a hash with no record would be added with public
// trackers and the DHT. These tests need no engine: with none started, nothing is stored.

func TestWakeStoredRefusesAHashTheEngineDoesNotStore(t *testing.T) {
	c := NewNativeClient()
	const hash = "0123456789abcdef0123456789abcdef01234567"
	err := c.WakeStored(context.Background(), hash)
	if !errors.Is(err, ErrNotStored) {
		t.Fatalf("WakeStored(unknown) = %v, want ErrNotStored", err)
	}
	if _, ok := c.activeHashes.Load(hash); ok {
		t.Fatal("a refused wake left the hash marked active")
	}
}

func TestWakeStoredRefusesAMalformedHash(t *testing.T) {
	c := NewNativeClient()
	for _, h := range []string{"", "xyz", strings.Repeat("g", 40), "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567"} {
		if err := c.WakeStored(context.Background(), h); !errors.Is(err, ErrNotStored) {
			t.Fatalf("WakeStored(%q) = %v, want ErrNotStored", h, err)
		}
	}
}

func TestStoredMagnetIsBare(t *testing.T) {
	const hash = "0123456789abcdef0123456789abcdef01234567"
	if got, want := storedMagnet(hash), "magnet:?xt=urn:btih:"+hash; got != want {
		t.Fatalf("storedMagnet = %q, want %q: no tracker, no peer, no name", got, want)
	}
}
