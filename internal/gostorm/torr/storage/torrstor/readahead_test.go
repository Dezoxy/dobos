package torrstor

import (
	"testing"

	"flodni/internal/gostorm/settings"
)

// Dezoxy fork: a bounded range never reads ahead past its end.
func TestClampReadahead(t *testing.T) {
	cases := []struct{ ra, end, pos, want int64 }{
		{256 << 20, 65536, 0, 65536},
		{256 << 20, 65536, 60000, 5536},
		{1 << 20, 64 << 20, 0, 1 << 20}, // smaller read-ahead untouched
		{8 << 20, 100, 200, 0},          // past the end
	}
	for _, c := range cases {
		if got := clampReadahead(c.ra, c.end, c.pos); got != c.want {
			t.Errorf("clampReadahead(%d, %d, %d) = %d, want %d", c.ra, c.end, c.pos, got, c.want)
		}
	}
}

// The engine must never be asked to fetch further ahead than the reader's protected range
// keeps: pieces that arrive beyond it are evictable on arrival and requested again.
func TestReadaheadStaysInsideProtectedRange(t *testing.T) {
	oldSets := settings.BTsets
	defer func() { settings.BTsets = oldSets }()

	const mib = 1 << 20
	for _, cacheMiB := range []int64{64, 128, 256, 512, 2048} {
		for _, pct := range []int{50, 75, 95} {
			for _, readers := range []int{1, 2, 4} {
				settings.BTsets = &settings.BTSets{CacheSize: cacheMiB * mib, ReaderReadAHead: pct}
				ra := perReaderReadahead(CacheReadahead(), readers)
				// The forward half of getOffsetRange, away from the file ends.
				ahead := int64(float64(cacheMiB*mib/int64(readers)) *
					(float64(protectedSafetyPct) / 100) * float64(pct) / 100.0)
				if ahead < minPerReaderReadahead {
					// The 8 MiB floor wins on small caches; it is the only allowed excess.
					ahead = minPerReaderReadahead
				}
				if ra > ahead {
					t.Errorf("cache %d MiB, %d%%, %d readers: engine read-ahead %d exceeds protected range %d",
						cacheMiB, pct, readers, ra, ahead)
				}
			}
		}
	}
}
