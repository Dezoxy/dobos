package torrent

import (
	"testing"

	"github.com/anacrolix/torrent/metainfo"
	pp "github.com/anacrolix/torrent/peer_protocol"
)

// A private torrent (BEP 27) must stay off the DHT and out of PEX even when both
// are on for public torrents: its peers are offered no ut_pex.
func TestPrivateTorrentsGetNoPex(t *testing.T) {
	yes, no := true, false
	cl := &Client{
		defaultLocalLtepProtocolMap: makeBuiltinLtepProtocols(true),
		privateLocalLtepProtocolMap: makeBuiltinLtepProtocols(false),
	}
	cases := []struct {
		name    string
		info    *metainfo.Info
		private bool
	}{
		{"magnet without metadata yet", nil, false},
		{"public", &metainfo.Info{Private: &no}, false},
		{"no flag", &metainfo.Info{}, false},
		{"private", &metainfo.Info{Private: &yes}, true},
	}
	for _, c := range cases {
		tor := &Torrent{cl: cl, info: c.info}
		if got := tor.isPrivate(); got != c.private {
			t.Errorf("%s: isPrivate() = %v, want %v", c.name, got, c.private)
		}
		pc := &PeerConn{}
		pc.t = tor
		pc.addBuiltinLtepProtocols(true)
		offersPex := false
		for _, name := range pc.LocalLtepProtocolMap.Index {
			if name == pp.ExtensionNamePex {
				offersPex = true
			}
		}
		if offersPex == c.private {
			t.Errorf("%s: offers ut_pex = %v", c.name, offersPex)
		}
	}
}
