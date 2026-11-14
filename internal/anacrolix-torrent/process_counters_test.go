package torrent

import "testing"

func TestProcessCountersChurnSplitByTransport(t *testing.T) {
	before := ProcessCounters()
	tor := &Torrent{}
	tor.countChurnDrop(&PeerConn{Peer: Peer{Network: "tcp"}})
	tor.countChurnDrop(&PeerConn{Peer: Peer{Network: "tcp"}})
	tor.countChurnDrop(&PeerConn{Peer: Peer{Network: "udp"}})
	after := ProcessCounters()
	if d := after.PeerChurnsTCP - before.PeerChurnsTCP; d != 2 {
		t.Fatalf("tcp churn delta = %d, want 2", d)
	}
	if d := after.PeerChurnsUTP - before.PeerChurnsUTP; d != 1 {
		t.Fatalf("utp churn delta = %d, want 1", d)
	}
}

func TestProcessCountersGradient2Backoff(t *testing.T) {
	g := newGradient2(16, 2, 128)
	for i := 0; i < gradient2Warmup; i++ {
		g.onSample(1, 16)
	}
	before := ProcessCounters().Gradient2Backoffs
	g.onSample(1, 16) // baseline latency: gradient clamps at 1, no backoff
	if d := ProcessCounters().Gradient2Backoffs - before; d != 0 {
		t.Fatalf("steady sample counted %d backoffs", d)
	}
	g.onSample(10, 16) // latency jumps: gradient falls below 1
	if d := ProcessCounters().Gradient2Backoffs - before; d != 1 {
		t.Fatalf("queueing sample counted %d backoffs, want 1", d)
	}
	g.onSample(10, 1) // app-limited: the loop does not run
	if d := ProcessCounters().Gradient2Backoffs - before; d != 1 {
		t.Fatalf("app-limited sample counted: delta %d", d)
	}
}
