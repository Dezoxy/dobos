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
