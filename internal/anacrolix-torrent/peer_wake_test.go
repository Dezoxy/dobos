package torrent

import (
	"testing"
	"time"

	"github.com/RoaringBitmap/roaring/v2"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
)

// wakeCounter is a peer with everything and nothing in flight. It counts the requests
// updates the engine asks of it.
type wakeCounter struct {
	peerImpl
	wakes int
}

func (w *wakeCounter) handleUpdateRequests()               { w.wakes++ }
func (w *wakeCounter) peerHasAllPieces() (all, known bool) { return true, true }
func (w *wakeCounter) peerPieces() *roaring.Bitmap         { return roaring.New() }
func (w *wakeCounter) onClose()                            {}

// Pins what Flodni's cache relies on (internal/gostorm/torr/storage/torrstor, wakePeers):
// a priority change that leaves the set of wanted pieces as it was wakes no peer, and
// AllowDataUpload asks every peer to update its requests. If the first assertion starts
// failing, the engine wakes on order-only changes and the cache's call can go.
func TestPeerWakeOnOrderOnlyPriorityChange(t *testing.T) {
	cfg := NewDefaultClientConfig()
	cfg.DataDir = t.TempDir()
	cfg.ListenHost = func(string) string { return "127.0.0.1" }
	cfg.ListenPort = 0
	cfg.NoDefaultPortForwarding = true
	cfg.DisableTrackers = true
	cfg.DisableIPv6 = true
	cfg.NoDHT = true
	cl, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	const pieces = 4
	b, err := bencode.Marshal(metainfo.Info{Name: "wake", Length: 16384 * pieces, PieceLength: 16384, Pieces: make([]byte, 20*pieces)})
	if err != nil {
		t.Fatal(err)
	}
	tor, _, err := cl.AddTorrentSpec(&TorrentSpec{InfoHash: metainfo.HashBytes(b), InfoBytes: b})
	if err != nil {
		t.Fatal(err)
	}
	w := &wakeCounter{}
	fake := &Peer{t: tor, peerImpl: w}
	fake.requestState.Requests = &peerRequests{}
	cl.lock()
	tor.webSeeds = map[string]*Peer{"fake": fake}
	cl.unlock()
	// The stand-in is no real peer: take it out before the client closes its peers.
	defer func() {
		cl.lock()
		tor.webSeeds = nil
		cl.unlock()
	}()

	// The initial hash pass of the piece must be over, or it is not wanted yet.
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		cl.lock()
		busy := tor.pieces[2].hashing || tor.pieces[2].queuedForHash()
		cl.unlock()
		if !busy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the piece never finished hashing")
		}
	}
	tor.SetPiecePriorities(map[int]piecePriority{2: PiecePriorityNormal})
	if w.wakes == 0 {
		t.Fatal("setup: a piece that becomes wanted woke no idle peer")
	}

	w.wakes = 0
	tor.SetPiecePriorities(map[int]piecePriority{2: PiecePriorityHigh}) // same wanted set, new order
	if w.wakes != 0 {
		t.Fatalf("an order-only change woke the peer %d times: the engine no longer needs Flodni's wake", w.wakes)
	}

	tor.AllowDataUpload()
	if w.wakes != 1 {
		t.Fatalf("AllowDataUpload woke the peer %d times, want 1", w.wakes)
	}
}
