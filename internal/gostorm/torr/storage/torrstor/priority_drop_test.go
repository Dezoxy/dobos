package torrstor

import (
	"testing"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	torrenttypes "github.com/anacrolix/torrent/types"
)

// A piece that completed must lose the priority this cache gave it: with a storage cap the
// engine spends the cap on pieces in priority order, complete or not, so a stale priority on a
// complete piece keeps the pieces a reader waits for from being requested.
func TestMarkCompleteDropsCachePriority(t *testing.T) {
	const pieceLen = 16 << 10
	const pieces = 8
	stor := NewStorage(int64(pieces) * pieceLen)

	cfg := torrent.NewDefaultClientConfig()
	cfg.DefaultStorage = stor
	cfg.ListenPort = 0
	cfg.NoDHT = true
	cfg.DisableTrackers = true
	cfg.NoDefaultPortForwarding = true
	cfg.DisableUTP = true
	cl, err := torrent.NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()

	info := metainfo.Info{PieceLength: pieceLen, Length: pieces * pieceLen, Name: "x", Pieces: make([]byte, 20*pieces)}
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

	cache := stor.GetCache(tor.InfoHash())
	if cache == nil {
		t.Fatal("no cache")
	}
	cache.SetTorrent(tor)

	// What setLoadPriority does for a piece under a reader.
	const id = 3
	cache.muPriority.Lock()
	cache.localPriority[id] = torrent.PiecePriorityNow
	tor.SetPiecePriorities(map[int]torrenttypes.PiecePriority{id: torrent.PiecePriorityNow})
	cache.muPriority.Unlock()
	if got := tor.PieceState(id).Priority; got != torrent.PiecePriorityNow {
		t.Fatalf("setup: priority %v, want Now", got)
	}

	if err := cache.pieces[id].MarkComplete(); err != nil {
		t.Fatal(err)
	}

	if got := tor.PieceState(id).Priority; got != torrent.PiecePriorityNone {
		t.Errorf("engine priority after MarkComplete = %v, want None", got)
	}
	cache.muPriority.Lock()
	_, kept := cache.localPriority[id]
	cache.muPriority.Unlock()
	if kept {
		t.Error("localPriority entry kept after MarkComplete")
	}
}
