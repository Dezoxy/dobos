package torr

import "testing"

// Dezoxy fork: the pieces a block depends on, for the verification gate.
func TestPieceSpan(t *testing.T) {
	const pl = 16 << 20
	cases := []struct {
		off, n      int64
		first, last int
		ok          bool
	}{
		{0, 1, 0, 0, true},
		{pl - 1, 2, 0, 1, true},  // straddles a boundary: both pieces
		{3 * pl, pl, 3, 3, true}, // exactly one piece
		{5*pl + 10, 4 << 20, 5, 5, true},
		{0, 0, 0, 0, false},
		{-1, 10, 0, 0, false},
	}
	for _, c := range cases {
		first, last, ok := pieceSpan(c.off, c.n, pl)
		if first != c.first || last != c.last || ok != c.ok {
			t.Errorf("pieceSpan(%d, %d) = %d, %d, %v; want %d, %d, %v", c.off, c.n, first, last, ok, c.first, c.last, c.ok)
		}
	}
	if _, _, ok := pieceSpan(0, 10, 0); ok {
		t.Error("zero piece length accepted")
	}
}

func TestRangeVerifiedNilTorrent(t *testing.T) {
	var tr *Torrent
	if tr.RangeVerified(1, 0, 10) {
		t.Fatal("a missing torrent verified a range")
	}
}

func TestBoundedRangeEnd(t *testing.T) {
	cases := []struct {
		header string
		end    int64
		ok     bool
	}{
		{"bytes=0-511", 512, true},
		{"bytes=100-199", 200, true},
		{"bytes=900-2000", 1000, true}, // clamped to the file
		{"bytes=100-", 0, false},       // the pump: open-ended
		{"bytes=-500", 0, false},
		{"bytes=0-1,5-9", 0, false},
		{"bytes=9-1", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		end, ok := boundedRangeEnd(c.header, 1000)
		if end != c.end || ok != c.ok {
			t.Errorf("boundedRangeEnd(%q) = %d, %v; want %d, %v", c.header, end, ok, c.end, c.ok)
		}
	}
}
