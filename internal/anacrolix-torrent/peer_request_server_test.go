package torrent

import (
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

func newLoopbackClient(t *testing.T, dir string) *Client {
	t.Helper()
	cfg := NewDefaultClientConfig()
	cfg.DataDir = dir
	cfg.DefaultStorage = storage.NewFile(dir)
	cfg.ListenPort = 0
	cfg.NoDHT = true
	cfg.DisablePEX = true
	cfg.NoDefaultPortForwarding = true
	cfg.DisableIPv6 = true
	cfg.DisableUTP = true
	cfg.Seed = true
	cl, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cl.Close() })
	return cl
}

// A seeder left at the default MaxAllocPeerRequestDataPerConn (1 MiB) must still
// serve a leecher that pipelines far more than that: the request server used to
// block on a request whose allocation was not yet granted while granted requests
// went unread, so nothing was sent until the leecher timed requests out (~35 s).
// Not named *Private*: CI's engine run filter must include TestPeerRequestServer.
func TestPeerRequestServerUnderAllocLimit(t *testing.T) {
	seedDir := t.TempDir()
	content := make([]byte, 24<<20)
	if _, err := rand.Read(content); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seedDir, "f.bin"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	info := metainfo.Info{PieceLength: 1 << 20}
	if err := info.BuildFromFilePath(filepath.Join(seedDir, "f.bin")); err != nil {
		t.Fatal(err)
	}
	b, err := bencode.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	mi := &metainfo.MetaInfo{InfoBytes: b}

	seeder := newLoopbackClient(t, seedDir)
	st, err := seeder.AddTorrent(mi)
	if err != nil {
		t.Fatal(err)
	}
	st.VerifyData()
	deadline := time.Now().Add(20 * time.Second)
	for st.BytesCompleted() != st.Length() {
		if time.Now().After(deadline) {
			t.Fatal("seeder did not verify its data")
		}
		time.Sleep(50 * time.Millisecond)
	}

	leecher := newLoopbackClient(t, t.TempDir())
	lt, err := leecher.AddTorrent(mi)
	if err != nil {
		t.Fatal(err)
	}
	lt.AddClientPeer(seeder)
	lt.DownloadAll()
	done := make(chan struct{})
	go func() {
		for lt.BytesCompleted() != lt.Length() {
			time.Sleep(50 * time.Millisecond)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatalf("leecher stalled at %d of %d bytes", lt.BytesCompleted(), lt.Length())
	}
}
