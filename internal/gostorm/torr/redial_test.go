package torr

import (
	"net"
	"sync"
	"testing"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
)

// Privacy pins for the dropped-peer redial (redial_dropped_peer, hard rule 3): a private
// torrent gets back only an address its own tracker gave it, and nothing in the redial
// touches the DHT, PEX or a public tracker.

type fakeRedial struct {
	hash    metainfo.Hash
	private bool
	active  int
	missing int64
	closed  chan struct{}

	mu     sync.Mutex
	added  []torrent.PeerRemoteAddr
	refuse bool // AddPeer takes nothing, as for an address the torrent already knows
}

func newFakeRedial() *fakeRedial {
	return &fakeRedial{hash: metainfo.HashBytes([]byte("redial")), private: true, missing: 1, closed: make(chan struct{})}
}

func (f *fakeRedial) Hash() metainfo.Hash     { return f.hash }
func (f *fakeRedial) Private() bool           { return f.private }
func (f *fakeRedial) ActivePeers() int        { return f.active }
func (f *fakeRedial) BytesMissing() int64     { return f.missing }
func (f *fakeRedial) Closed() <-chan struct{} { return f.closed }
func (f *fakeRedial) AddPeer(a torrent.PeerRemoteAddr) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.added = append(f.added, a)
	if f.refuse {
		return 0
	}
	return 1
}
func (f *fakeRedial) adds() []torrent.PeerRemoteAddr {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]torrent.PeerRemoteAddr(nil), f.added...)
}

func redialEnv(t *testing.T, on bool) {
	t.Helper()
	prevF, prevD, prevG := RedialDroppedPeer, redialDelay, redialMinGap
	RedialDroppedPeer = func() bool { return on }
	redialDelay, redialMinGap = time.Millisecond, time.Hour
	redialMu.Lock()
	redialLast = map[metainfo.Hash]time.Time{}
	redialMu.Unlock()
	t.Cleanup(func() { RedialDroppedPeer, redialDelay, redialMinGap = prevF, prevD, prevG })
}

var testAddr = &net.TCPAddr{IP: net.IPv4(192, 0, 2, 7), Port: 51413}

// settle waits for the redials in flight to end.
func settle() { redialWG.Wait() }

func TestRedialOnlyFromTheTorrentsOwnTracker(t *testing.T) {
	cases := []struct {
		name string
		src  torrent.PeerSource
		want bool
	}{
		{"own tracker", torrent.PeerSourceTracker, true},
		{"DHT search", torrent.PeerSourceDhtGetPeers, false},
		{"DHT announce", torrent.PeerSourceDhtAnnouncePeer, false},
		{"PEX", torrent.PeerSourcePex, false},
		{"incoming", torrent.PeerSourceIncoming, false},
		{"hole punch", torrent.PeerSourceUtHolepunch, false},
		{"direct add", torrent.PeerSourceDirect, false},
		{"unknown", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			redialEnv(t, true)
			f := newFakeRedial()
			maybeRedial(c.src, f, testAddr)
			settle()
			if got := len(f.adds()) == 1; got != c.want {
				t.Fatalf("redialed = %v, want %v (adds %v)", got, c.want, f.adds())
			}
		})
	}
}

func TestRedialAddsTheDroppedAddressBackOnce(t *testing.T) {
	redialEnv(t, true)
	f := newFakeRedial()
	before := RedialCount()
	maybeRedial(torrent.PeerSourceTracker, f, testAddr)
	settle()
	if a := f.adds(); len(a) != 1 || a[0] != torrent.PeerRemoteAddr(testAddr) {
		t.Fatalf("adds = %v, want exactly the dropped address", a)
	}
	// The same seeder dropping again at once is not redialed in a loop.
	maybeRedial(torrent.PeerSourceTracker, f, testAddr)
	settle()
	if n := len(f.adds()); n != 1 {
		t.Fatalf("adds = %d after a second drop within the gap, want 1", n)
	}
	if RedialCount()-before != 1 {
		t.Fatalf("redial count +%d, want 1", RedialCount()-before)
	}
}

func TestRedialSkipsWhatItShouldNotTouch(t *testing.T) {
	cases := []struct {
		name  string
		on    bool
		setup func(f *fakeRedial)
	}{
		{"switch off", false, func(*fakeRedial) {}},
		{"public torrent", true, func(f *fakeRedial) { f.private = false }},
		{"other peers connected", true, func(f *fakeRedial) { f.active = 2 }},
		{"nothing missing", true, func(f *fakeRedial) { f.missing = 0 }},
		{"torrent closed in the delay", true, func(f *fakeRedial) { close(f.closed) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			redialEnv(t, c.on)
			f := newFakeRedial()
			c.setup(f)
			maybeRedial(torrent.PeerSourceTracker, f, testAddr)
			settle()
			if a := f.adds(); len(a) != 0 {
				t.Fatalf("redialed %v", a)
			}
		})
	}
}

// With the switch unset (the default of a process that never sets the hook) nothing runs.
func TestRedialIsOffWithoutTheHook(t *testing.T) {
	redialEnv(t, true)
	RedialDroppedPeer = nil
	f := newFakeRedial()
	maybeRedial(torrent.PeerSourceTracker, f, testAddr)
	settle()
	if a := f.adds(); len(a) != 0 {
		t.Fatalf("redialed %v with no hook", a)
	}
}

// The adapter against a real engine torrent: private only once the metadata says so, no
// connected peer, and AddPeer queues the address as a tracker peer without dialing here.
func TestRedialAdapterReadsTheEngineTorrent(t *testing.T) {
	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = t.TempDir()
	cfg.ListenPort = 0
	cfg.NoDHT = true
	cfg.DisableTrackers = true
	cfg.DisableTCP, cfg.DisableUTP = true, true
	cfg.NoDefaultPortForwarding = true
	cl, err := torrent.NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cl.Close() })

	yes, no := true, false
	for _, c := range []struct {
		name    string
		private *bool
		want    bool
	}{{"private", &yes, true}, {"public", &no, false}, {"no flag", nil, false}} {
		b := testInfoBytes(t, c.private)
		tor, err := cl.AddTorrent(&metainfo.MetaInfo{InfoBytes: b})
		if err != nil {
			t.Fatal(err)
		}
		<-tor.GotInfo()
		e := engineTorrent{tor}
		if e.Private() != c.want {
			t.Errorf("%s: Private() = %v, want %v", c.name, e.Private(), c.want)
		}
		if e.ActivePeers() != 0 || e.BytesMissing() <= 0 || e.Hash() != tor.InfoHash() {
			t.Errorf("%s: adapter reads wrong: peers %d missing %d", c.name, e.ActivePeers(), e.BytesMissing())
		}
		e.AddPeer(testAddr)
		if got := tor.Stats().PendingPeers; got != 1 {
			t.Errorf("%s: pending peers = %d after AddPeer, want 1", c.name, got)
		}
	}
}

// A redial the torrent did not take is neither counted nor logged, and a closed torrent
// leaves no limit entry behind.
func TestRedialCountsOnlyWhatTheTorrentTook(t *testing.T) {
	redialEnv(t, true)
	f := newFakeRedial()
	f.refuse = true
	before := RedialCount()
	maybeRedial(torrent.PeerSourceTracker, f, testAddr)
	settle()
	if RedialCount() != before {
		t.Fatalf("counted a redial the torrent refused")
	}
}

func TestRedialLimitEntryIsForgottenWhenTheTorrentCloses(t *testing.T) {
	redialEnv(t, true)
	redialDelay = time.Hour
	f := newFakeRedial()
	redialAllowed(f.hash, time.Now())
	maybeRedial(torrent.PeerSourceTracker, f, testAddr)
	close(f.closed)
	settle()
	redialMu.Lock()
	n := len(redialLast)
	redialMu.Unlock()
	if n != 0 {
		t.Fatalf("%d limit entries left after the torrent closed", n)
	}
}
