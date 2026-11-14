package torr

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"

	"flodni/internal/gostorm/log"
)

// Redial of a dropped only peer (opt-in, redial_dropped_peer, off by default).
//
// The engine does not keep a dropped peer to redial, and floors a tracker's announce
// interval at one minute, so a private torrent whose one seeder drops waits up to a
// minute for the next announce to hear of it again. With the switch on, the address of
// a connection that this torrent dialed from its own tracker is added back to the same
// torrent once, a few seconds after the drop, when no other peer is connected. Nothing
// else is involved: no DHT lookup, no PEX, no public tracker, no address from anywhere
// but the connection that dropped. A torrent that is not private is left to its own
// discovery, and so is a connection that came from any other source.
//
// To keep a seeder that drops again at once from being redialed in a loop, a torrent
// is redialed at most once per redialMinGap, the announce floor.

// RedialDroppedPeer, when set, says whether the switch is on. Asked when a peer drops.
// The daemon sets it once at start from redial_dropped_peer. A nil func is off.
var RedialDroppedPeer func() bool

var (
	// redialDelay is how long after the drop the address is added back; redialMinGap
	// is the least time between two redials of one torrent. Vars so tests can shorten
	// them; not config keys.
	redialDelay  = 3 * time.Second
	redialMinGap = time.Minute

	redials    atomic.Int64
	redialWG   sync.WaitGroup // the redials in flight; tests wait on it
	redialMu   sync.Mutex
	redialLast = map[metainfo.Hash]time.Time{}
)

// RedialCount is how many peers the redial has added back since start, for
// /metrics/prometheus.
func RedialCount() int64 { return redials.Load() }

// redialSource reports whether a connection discovered by src may be redialed: only
// one that the torrent's own tracker gave us. A peer found through the DHT, PEX, a
// hole-punch, a direct add or an incoming connection (whose remote port is not a
// listening one) never is.
func redialSource(src torrent.PeerSource) bool {
	return src == torrent.PeerSourceTracker
}

// redialTarget is the part of an engine torrent the redial asks about, so a test can
// stand in for it.
type redialTarget interface {
	Hash() metainfo.Hash
	Private() bool
	ActivePeers() int
	BytesMissing() int64
	Closed() <-chan struct{}
	AddPeer(addr torrent.PeerRemoteAddr)
}

// engineTorrent adapts an engine torrent. Every method but Hash and Closed takes the
// engine's client lock, so none may run inside the engine's callbacks.
type engineTorrent struct{ t *torrent.Torrent }

func (e engineTorrent) Hash() metainfo.Hash { return e.t.InfoHash() }

// Private is false until the metadata is known: an unknown torrent is never redialed.
func (e engineTorrent) Private() bool {
	info := e.t.Info()
	return info != nil && info.Private != nil && *info.Private
}
func (e engineTorrent) ActivePeers() int        { return e.t.Stats().ActivePeers }
func (e engineTorrent) BytesMissing() int64     { return e.t.BytesMissing() }
func (e engineTorrent) Closed() <-chan struct{} { return e.t.Closed() }

// AddPeer hands the address back to the torrent as what it was: a tracker's peer.
func (e engineTorrent) AddPeer(addr torrent.PeerRemoteAddr) {
	e.t.AddPeers([]torrent.PeerInfo{{Addr: addr, Source: torrent.PeerSourceTracker}})
}

// onPeerConnClosed is the engine's PeerConnClosed callback. It runs inside the engine
// with its locks possibly held: it reads fields and returns, and the work is a
// goroutine.
func onPeerConnClosed(pc *torrent.PeerConn) {
	t := pc.Torrent()
	if t == nil || pc.RemoteAddr == nil {
		return
	}
	maybeRedial(pc.Discovery, engineTorrent{t}, pc.RemoteAddr)
}

// maybeRedial schedules the redial of a dropped connection when the switch is on and the
// connection came from the torrent's own tracker. Whether it happens is decided again
// when the delay is over.
func maybeRedial(src torrent.PeerSource, t redialTarget, addr torrent.PeerRemoteAddr) {
	if f := RedialDroppedPeer; f == nil || !f() || !redialSource(src) {
		return
	}
	redialWG.Add(1)
	go func() {
		defer redialWG.Done()
		redialAfter(t, addr, redialDelay)
	}()
}

// redialAfter waits, then adds addr back to t once if t is a private torrent that still
// wants data, has no connected peer and was not redialed within redialMinGap.
func redialAfter(t redialTarget, addr torrent.PeerRemoteAddr, delay time.Duration) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-t.Closed():
		return
	}
	if !t.Private() || t.ActivePeers() != 0 || t.BytesMissing() == 0 {
		return
	}
	if !redialAllowed(t.Hash(), time.Now()) {
		return
	}
	t.AddPeer(addr)
	redials.Add(1)
	log.TLogln("redial: re-added the only peer of a private torrent", t.Hash().HexString())
}

// redialAllowed records a redial of hash at now and reports whether the last one is at
// least redialMinGap old.
func redialAllowed(hash metainfo.Hash, now time.Time) bool {
	redialMu.Lock()
	defer redialMu.Unlock()
	if last, ok := redialLast[hash]; ok && now.Sub(last) < redialMinGap {
		return false
	}
	redialLast[hash] = now
	return true
}
