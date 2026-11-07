package main

import "testing"

// Dezoxy fork: a scan read fetches the rest of the head window or only what it asked for.
func TestScanFetchSize(t *testing.T) {
	const head, ra = 4 << 20, 16 << 20
	cases := []struct{ off, target, want int64 }{
		{0, 65536, head},              // first probe fills the head window in one fetch
		{1 << 20, 131072, 3 << 20},    // rest of the window
		{head, 131072, 131072},        // past the head: only the probe
		{500 << 20, 1 << 20, 1 << 20}, // a middle read
		{0, 32 << 20, 32 << 20},       // never less than asked
	}
	for _, c := range cases {
		if got := scanFetchSize(c.off, c.target, head, ra); got != c.want {
			t.Errorf("scanFetchSize(%d, %d) = %d, want %d", c.off, c.target, got, c.want)
		}
	}
	// Upstream's 64 MB head: still capped at the normal read-ahead.
	if got := scanFetchSize(0, 65536, 64<<20, ra); got != ra {
		t.Errorf("64 MB head: got %d, want %d", got, ra)
	}
}
