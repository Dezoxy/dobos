package torrstor

import "testing"

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
