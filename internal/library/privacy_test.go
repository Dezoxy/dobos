package library

import (
	"context"
	"strings"
	"testing"

	"tiramisu/internal/gostorm/torr/utils"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
)

const privacyTracker = "http://seeder.example:6969/announce"

// torrentFile builds a one-file .torrent announcing on privacyTracker.
func torrentFile(t *testing.T, private bool) []byte {
	t.Helper()
	info := metainfo.Info{
		Name:        "release",
		Length:      16384,
		PieceLength: 16384,
		Pieces:      make([]byte, 20),
	}
	if private {
		info.Private = &private
	}
	ib, err := bencode.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	b, err := bencode.Marshal(metainfo.MetaInfo{InfoBytes: ib, Announce: privacyTracker})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func hasPublicTracker(magnet string) bool {
	for _, tr := range DefaultTrackers() {
		if strings.Contains(magnet, escapeTracker(tr)) {
			return true
		}
	}
	return false
}

func escapeTracker(tr string) string {
	return BuildMagnet("", "", []string{tr})[len("magnet:?xt=urn:btih:&tr="):]
}

// A private torrent keeps only its own trackers; a public one, or one named by hash
// alone, is not given the public fallback list for a hash that may turn out private.
func TestReleaseMagnetKeepsPrivateTorrentsPrivate(t *testing.T) {
	cases := []struct {
		name       string
		file       []byte
		wantPublic bool
		wantOwn    bool
	}{
		{"private torrent file", torrentFile(t, true), false, true},
		{"public torrent file", torrentFile(t, false), true, true},
		{"hash only, privacy unknown", nil, false, false},
	}
	for _, c := range cases {
		hash := strings.Repeat("ab", 20)
		file, err := releaseFile(c.file, hash)
		if len(c.file) > 0 {
			src, perr := ParseTorrentFile(c.file)
			if perr != nil {
				t.Fatal(perr)
			}
			hash = src.Hash
			file, err = releaseFile(c.file, hash)
		}
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		magnet := releaseMagnet(hash, "Title", file)
		if got := hasPublicTracker(magnet); got != c.wantPublic {
			t.Errorf("%s: public trackers in magnet = %v, want %v: %s", c.name, got, c.wantPublic, magnet)
		}
		if got := strings.Contains(magnet, "seeder.example"); got != c.wantOwn {
			t.Errorf("%s: own tracker in magnet = %v, want %v", c.name, got, c.wantOwn)
		}
	}
}

// fakeEngine records the magnets the library hands the engine.
type fakeEngine struct{ magnets []string }

func (f *fakeEngine) AddTorrent(_ context.Context, magnet, _ string) (string, error) {
	f.magnets = append(f.magnets, magnet)
	return HashFromMagnet(magnet), nil
}
func (f *fakeEngine) GetTorrentInfo(context.Context, string, int) (*TorrentStats, error) {
	return nil, context.DeadlineExceeded
}
func (f *fakeEngine) RemoveTorrent(context.Context, string) error { return nil }
func (f *fakeEngine) ListTorrents(context.Context) ([]TorrentStats, error) {
	return nil, nil
}

func TestInspectDoesNotGivePrivateTorrentsPublicTrackers(t *testing.T) {
	private := torrentFile(t, true)
	src, err := ParseTorrentFile(private)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name       string
		hash       string
		file       []byte
		wantPublic bool
	}{
		{"private torrent file", src.Hash, private, false},
		{"hash only", strings.Repeat("cd", 20), nil, false},
		{"public torrent file", "", torrentFile(t, false), true},
	}
	for _, c := range cases {
		fe := &fakeEngine{}
		m := New(Config{GoStorm: fe})
		hash := c.hash
		if hash == "" {
			s, _ := ParseTorrentFile(c.file)
			hash = s.Hash
		}
		_, _ = m.Inspect(context.Background(), InspectRequest{Hash: hash, Title: "T", TorrentFile: c.file, MetadataWait: 1})
		if len(fe.magnets) != 1 {
			t.Fatalf("%s: engine got %d adds, want 1", c.name, len(fe.magnets))
		}
		if got := hasPublicTracker(fe.magnets[0]); got != c.wantPublic {
			t.Errorf("%s: public trackers in magnet = %v, want %v: %s", c.name, got, c.wantPublic, fe.magnets[0])
		}
	}
}

// The wake path strips GoStorm's public lists from a private torrent; the library's
// fallback list must be among them or its trackers would survive a wake.
func TestDefaultTrackersAreInGoStormBuiltinList(t *testing.T) {
	builtin := map[string]bool{}
	for _, tr := range utils.BuiltinTrackers() {
		builtin[tr] = true
	}
	for _, tr := range DefaultTrackers() {
		if !builtin[tr] {
			t.Errorf("%s is not in gostorm's built-in tracker list", tr)
		}
	}
}
