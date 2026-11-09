package library

import "testing"

// Dezoxy fork: a pack whose files write the episode with a separator (s04.e01) filed
// nothing for that season.
func TestParseSeasonEpisode(t *testing.T) {
	for name, want := range map[string][2]int{
		"Game.of.Thrones.S04E01.720p.mkv":        {4, 1},
		"gameofthrones.s04.e01.720p-trinity.mkv": {4, 1},
		"Show S02 E10 1080p.mkv":                 {2, 10},
		"Show.S1-E3.mkv":                         {1, 3},
		"show.3x07.hdtv.mkv":                     {3, 7},
		"Movie.2019.1080p.DDP5.1.x264.mkv":       {0, 0},
		"Some.Film.Atmos.e01.mkv":                {0, 0},
	} {
		s, e := ParseSeasonEpisode(name)
		if s != want[0] || e != want[1] {
			t.Errorf("%s: got %d,%d want %d,%d", name, s, e, want[0], want[1])
		}
	}
}

// Dezoxy fork: scene-numbered packs (sln-720p.ahs.806.mkv) and packs naming the episode
// only in its folder filed nothing.
func TestPackEpisode(t *testing.T) {
	for _, c := range []struct {
		path   string
		season int
		want   [2]int
	}{
		{"pack/sln-720p.ahs.806.mkv", 8, [2]int{8, 6}},
		{"pack/sln-ahs.502.mkv", 5, [2]int{5, 2}},
		{"pack/show.1005.mkv", 10, [2]int{10, 5}},
		{"pack/Supernatural.S05E01.720p.BluRay.x264.HuN-R4Z3R/r4z3r-supnat.501.mkv", 5, [2]int{5, 1}},
		{"pack/Show.S02E03.720p.mkv", 1, [2]int{2, 3}},
		{"pack/sln-720p.ahs.806.mkv", 7, [2]int{0, 0}},
		{"pack/show.720p.x264.mkv", 7, [2]int{0, 0}},
		{"pack/show.2019.1080p.mkv", 20, [2]int{20, 19}},
		{"pack/show.805.806.mkv", 8, [2]int{0, 0}},
		{"pack/sln-ahs.502.mkv", 0, [2]int{0, 0}},
		{"pack/Season 5/extras.mkv", 5, [2]int{0, 0}},
	} {
		s, e := PackEpisode(c.path, c.season)
		if s != c.want[0] || e != c.want[1] {
			t.Errorf("%s (season %d): got %d,%d want %d,%d", c.path, c.season, s, e, c.want[0], c.want[1])
		}
	}
}
