package torr

import (
	"bytes"
	"crypto/sha1"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

func TestIncompleteRuns(t *testing.T) {
	done := map[int]bool{2: true, 3: true, 7: true}
	got := incompleteRuns(0, 8, func(i int) bool { return done[i] })
	want := [][2]int{{0, 2}, {4, 7}, {8, 9}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("runs = %v, want %v", got, want)
	}
	if got := incompleteRuns(2, 3, func(i int) bool { return done[i] }); len(got) != 0 {
		t.Errorf("all complete: runs = %v, want none", got)
	}
}

// RequestRange must not leave a priority on a piece that is already complete: nothing
// clears it, and the engine spends its storage cap on it. Real engine torrent, no network:
// piece 3 is on disk with the right hash, the others are not.
func TestRequestIncompleteSkipsCompletePieces(t *testing.T) {
	const pieceLen, pieces, done = 16 << 10, 8, 3
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x"), make([]byte, pieces*pieceLen), 0o644); err != nil {
		t.Fatal(err)
	}
	hashes := make([]byte, 20*pieces)
	sum := sha1.Sum(make([]byte, pieceLen))
	copy(hashes[20*done:], sum[:])
	for i := 0; i < pieces; i++ {
		if i != done {
			hashes[20*i] = 0xff // no data hashes to this
		}
	}

	cfg := torrent.NewDefaultClientConfig()
	cfg.DefaultStorage = storage.NewFile(dir)
	cfg.ListenPort = 0
	cfg.NoDHT = true
	cfg.DisableTrackers = true
	cfg.NoDefaultPortForwarding = true
	cfg.DisableUTP = true
	cl, err := torrent.NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cl.Close() })
	info := metainfo.Info{PieceLength: pieceLen, Length: pieces * pieceLen, Name: "x", Pieces: hashes}
	ib, err := bencode.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	tor, err := cl.AddTorrent(&metainfo.MetaInfo{InfoBytes: ib})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-tor.GotInfo():
	case <-time.After(5 * time.Second):
		t.Fatal("no info")
	}
	tor.VerifyData()
	if !tor.PieceState(done).Complete || tor.PieceState(2).Complete || tor.PieceState(4).Complete {
		t.Fatal("setup: want only piece 3 complete")
	}

	requestIncomplete(tor, 2, 4)

	// The engine reports None for a complete piece whatever was raised, so lose the piece
	// (as a RAM eviction does) and look at what is left behind.
	if err := os.WriteFile(filepath.Join(dir, "x"), bytes.Repeat([]byte{1}, pieces*pieceLen), 0o644); err != nil {
		t.Fatal(err)
	}
	tor.Piece(done).VerifyData()
	deadline := time.Now().Add(5 * time.Second)
	for tor.PieceState(done).Complete {
		if time.Now().After(deadline) {
			t.Fatal("setup: piece 3 stayed complete")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := tor.PieceState(done).Priority; got != torrent.PiecePriorityNone {
		t.Errorf("priority left on the piece that was complete = %v, want None", got)
	}
	for _, i := range []int{2, 4} {
		if got := tor.PieceState(i).Priority; got != torrent.PiecePriorityNormal {
			t.Errorf("incomplete piece %d priority = %v, want Normal", i, got)
		}
	}
}
