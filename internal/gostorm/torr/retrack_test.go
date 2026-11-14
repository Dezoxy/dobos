package torr

import (
	"reflect"
	"testing"

	sets "flodni/internal/gostorm/settings"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
)

func testInfoBytes(t *testing.T, private *bool) []byte {
	t.Helper()
	b, err := bencode.Marshal(metainfo.Info{
		Name:        "release",
		Length:      16384,
		PieceLength: 16384,
		Pieces:      make([]byte, 20),
		Private:     private,
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A private torrent (BEP 27) gets no tracker beyond its own metainfo; a public
// torrent and one whose metadata is not known yet keep the pre-fix behaviour
// where the info is known, and wait for the metadata where it is not.
func TestRetrackSpecKeepsPrivateTorrentsPrivate(t *testing.T) {
	yes, no := true, false
	own := [][]string{{"http://seeder.example/announce"}}
	def := func() []string { return []string{"udp://public.example:1337/announce"} }
	file := func() []string { return []string{"udp://file.example:80/announce"} }
	withDefs := [][]string{
		{"http://seeder.example/announce"},
		{"udp://public.example:1337/announce"},
		{"udp://file.example:80/announce"},
	}
	cases := []struct {
		name string
		info []byte
		mode int
		want [][]string
	}{
		{"private, mode 1", testInfoBytes(t, &yes), 1, own},
		{"private, mode 2", testInfoBytes(t, &yes), 2, own},
		{"private, mode 3", testInfoBytes(t, &yes), 3, own},
		{"private, mode 0", testInfoBytes(t, &yes), 0, own},
		{"magnet without info, mode 1", nil, 1, own},
		{"magnet without info, mode 3", nil, 3, own},
		{"public, mode 1", testInfoBytes(t, &no), 1, withDefs},
		{"no flag, mode 1", testInfoBytes(t, nil), 1, withDefs},
		{"public, mode 2", testInfoBytes(t, &no), 2, [][]string{{"udp://file.example:80/announce"}}},
		{"public, mode 3", testInfoBytes(t, &no), 3, [][]string{
			{"udp://public.example:1337/announce"},
			{"udp://file.example:80/announce"},
		}},
	}
	for _, c := range cases {
		spec := &torrent.TorrentSpec{InfoBytes: c.info, Trackers: append([][]string(nil), own...)}
		retrackSpec(spec, c.mode, def, file)
		if !reflect.DeepEqual(spec.Trackers, c.want) {
			t.Errorf("%s: trackers = %v, want %v", c.name, spec.Trackers, c.want)
		}
	}
}

func TestInfoPublic(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name string
		info *metainfo.Info
		want bool
	}{
		{"not known yet", nil, false},
		{"private", &metainfo.Info{Private: &yes}, false},
		{"public", &metainfo.Info{Private: &no}, true},
		{"no flag", &metainfo.Info{}, true},
	}
	for _, c := range cases {
		if got := infoPublic(c.info); got != c.want {
			t.Errorf("%s: infoPublic = %v, want %v", c.name, got, c.want)
		}
	}
}

// A private torrent woken from its stored spec loses the public trackers it was saved
// with (the library defaults, a downloaded retracker) and keeps its own; a public
// torrent keeps everything.
func TestDropPublicTrackersOnWake(t *testing.T) {
	yes, no := true, false
	public := func() []string {
		return []string{
			"udp://tracker.opentrackr.org:1337/announce",
			"udp://open.stealth.si:80/announce",
			"udp://tracker.torrent.eu.org:451/announce",
			"udp://exodus.desync.com:6969/announce",
			"udp://tracker.openbittorrent.com:6969/announce",
			"udp://retracker.example:80/announce",
		}
	}
	own := "http://seeder.example/announce"
	extra := "https://other.example/announce"
	stored := func() [][]string {
		return [][]string{{own}, public()[:5], {public()[5]}, {extra}}
	}
	cases := []struct {
		name string
		info []byte
		want [][]string
	}{
		{"private", testInfoBytes(t, &yes), [][]string{{own}, {extra}}},
		{"public", testInfoBytes(t, &no), stored()},
		{"info unknown", nil, stored()},
	}
	for _, c := range cases {
		spec := &torrent.TorrentSpec{InfoBytes: c.info, Trackers: stored()}
		dropPublicTrackers(spec, public)
		if !reflect.DeepEqual(spec.Trackers, c.want) {
			t.Errorf("%s: trackers = %v, want %v", c.name, spec.Trackers, c.want)
		}
	}
}

// A wake (LoadStored, reached from native.Wake and WakeStored) adds a closed torrent
// from its stored record alone: the spec NewTorrent gets is the record's own, with no
// magnet parsed and no tracker merged in. For a private torrent that spec must then
// carry its own tracker only, whatever the retracker mode (hard rule 3). The spec
// here is the one the real wake path hands to the engine.
func TestWakeOfAStoredPrivateTorrentKeepsOnlyItsOwnTrackers(t *testing.T) {
	warmupFixture(t)
	yes := true
	own := "http://seeder.example/announce"
	def := func() []string { return []string{"udp://public.example:1337/announce"} }
	file := func() []string { return []string{"udp://file.example:80/announce"} }
	public := func() []string { return append(def(), file()...) }
	h := metainfo.NewHashFromHex(removeTestHash)
	// A record saved before the retracker gate carries the public lists too.
	sets.AddTorrent(&sets.TorrentDB{TorrentSpec: &torrent.TorrentSpec{
		InfoHash:  h,
		InfoBytes: testInfoBytes(t, &yes),
		Trackers:  [][]string{{own}, def(), file()},
	}})

	var got *torrent.TorrentSpec
	prev := newTorrent
	newTorrent = func(spec *torrent.TorrentSpec, _ *BTServer) (*Torrent, error) {
		got = spec
		return &Torrent{TorrentSpec: spec}, nil
	}
	defer func() { newTorrent = prev }()

	for mode := 0; mode <= 3; mode++ {
		got = nil
		if _, err := LoadStored(h.HexString()); err != nil {
			t.Fatalf("mode %d: LoadStored: %v", mode, err)
		}
		if got == nil || len(got.InfoBytes) == 0 {
			t.Fatalf("mode %d: the wake did not hand the record's metainfo to the engine", mode)
		}
		// What NewTorrent applies before the client sees the spec.
		dropPublicTrackers(got, public)
		retrackSpec(got, mode, def, file)
		if want := [][]string{{own}}; !reflect.DeepEqual(got.Trackers, want) {
			t.Errorf("mode %d: trackers = %v, want %v", mode, got.Trackers, want)
		}
	}
}

// Hard rule 3 for the tracker scrape: a private torrent's scrape targets only
// its own announce hosts, whatever the retracker mode added at the add and
// whatever an older record carries. A torrent whose metadata is not known yet
// is treated the same way; a public torrent keeps its stored trackers.
func TestScrapeTargetsKeepPrivateTorrentsOnTheirOwnTrackers(t *testing.T) {
	yes, no := true, false
	own := "http://seeder.example/KEY123456/announce?auth=SECRET9999"
	public := []string{"udp://public.example:1337/announce", "udp://file.example:80/announce"}
	def := func() []string { return public[:1] }
	file := func() []string { return public[1:] }

	record := func(info []byte, mode int, stored [][]string) *sets.TorrentDB {
		spec := &torrent.TorrentSpec{InfoBytes: info, Trackers: append([][]string(nil), stored...)}
		spec.InfoHash[0] = 0xab
		retrackSpec(spec, mode, def, file)
		return &sets.TorrentDB{TorrentSpec: spec}
	}
	ownTier := [][]string{{own}}
	// an older record that was saved with the public lists in it
	legacy := [][]string{{own}, {public[0], public[1]}}

	cases := []struct {
		name string
		db   *sets.TorrentDB
		want []string
	}{
		{"private, retracker mode 1", record(testInfoBytes(t, &yes), 1, ownTier), []string{own}},
		{"private, retracker mode 3", record(testInfoBytes(t, &yes), 3, ownTier), []string{own}},
		{"private, older record holding public lists", record(testInfoBytes(t, &yes), 0, legacy), []string{own}},
		{"metadata not known, older record", record(nil, 0, legacy), []string{own}},
		{"public, mode 1", record(testInfoBytes(t, &no), 1, ownTier), []string{own, public[0], public[1]}},
	}
	for _, c := range cases {
		got := scrapeTargetsOf([]*sets.TorrentDB{c.db}, func() []string { return public })
		if len(got) != 1 || !reflect.DeepEqual(got[0].Trackers, c.want) {
			t.Errorf("%s: targets = %+v, want trackers %v", c.name, got, c.want)
		}
	}
	if got := scrapeTargetsOf([]*sets.TorrentDB{nil, {}}, func() []string { return nil }); len(got) != 0 {
		t.Errorf("empty records give targets: %+v", got)
	}
}
