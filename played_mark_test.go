package main

import (
	"encoding/json"
	"testing"

	"flodni/internal/warmup"
)

func cand(path, imdb string, idx int) playedCandidate {
	return playedCandidate{path: path, imdbID: imdb, key: warmup.FileKey{Hash: "h", FileIdx: idx}}
}

func si(v int) optInt { return optInt{v: v, ok: true} }

func TestChoosePlayedFile(t *testing.T) {
	const show = "/mnt/tv/Some Show/"
	tests := []struct {
		name  string
		keys  playedKeys
		cands []playedCandidate
		want  string // path, "" = none
	}{
		{
			name:  "exact file name",
			keys:  playedKeys{basenames: []string{"Movie.2020.1080p.mkv"}, section: "movie", title: "movie"},
			cands: []playedCandidate{cand("/m/Movie.2020.1080p.mkv", "", 0), cand("/m/Movie.2020.720p.mkv", "", 1)},
			want:  "/m/Movie.2020.1080p.mkv",
		},
		{
			name:  "same file name twice is ambiguous",
			keys:  playedKeys{basenames: []string{"a.mkv"}},
			cands: []playedCandidate{cand("/x/a.mkv", "", 0), cand("/y/a.mkv", "", 1)},
			want:  "",
		},
		{
			name:  "unique IMDB id",
			keys:  playedKeys{imdbID: "tt1", section: "movie"},
			cands: []playedCandidate{cand("/m/A.mkv", "tt1", 0), cand("/m/B.mkv", "tt2", 1)},
			want:  "/m/A.mkv",
		},
		{
			name:  "IMDB id shared by a season marks none",
			keys:  playedKeys{imdbID: "tt9", section: "show", season: si(1), episode: si(2)},
			cands: []playedCandidate{cand(show+"S01E01.mkv", "tt9", 0), cand(show+"S01E02.mkv", "tt9", 1)},
			want:  "",
		},
		{
			name:  "unique IMDB id on the wrong episode marks none",
			keys:  playedKeys{imdbID: "tt9", section: "show", season: si(1), episode: si(2)},
			cands: []playedCandidate{cand(show+"Some.Show.S01E05.mkv", "tt9", 0)},
			want:  "",
		},
		{
			name:  "IMDB id cached on the previous episode falls through to the name",
			keys:  playedKeys{imdbID: "tt9", section: "show", seriesTitle: "Some Show", season: si(1), episode: si(2)},
			cands: []playedCandidate{cand(show+"Some.Show.S01E01.mkv", "tt9", 0), cand(show+"Some.Show.S01E02.mkv", "", 1)},
			want:  show + "Some.Show.S01E02.mkv",
		},
		{
			name:  "series-wide IMDB id on both falls through to the name",
			keys:  playedKeys{imdbID: "tt9", section: "show", seriesTitle: "Some Show", season: si(1), episode: si(2)},
			cands: []playedCandidate{cand(show+"Some.Show.S01E01.mkv", "tt9", 0), cand(show+"Some.Show.S01E02.mkv", "tt9", 1)},
			want:  show + "Some.Show.S01E02.mkv",
		},
		{
			name:  "a show webhook with no season and episode is not marked on the id alone",
			keys:  playedKeys{imdbID: "tt9", section: "show", seriesTitle: "Some Show"},
			cands: []playedCandidate{cand(show+"Some.Show.S01E01.mkv", "tt9", 0)},
			want:  "",
		},
		{
			name:  "a movie title that is also a series name does not mark an episode",
			keys:  playedKeys{section: "movie", title: "Some Show"},
			cands: []playedCandidate{cand(show+"Some.Show.S01E01.mkv", "", 0)},
			want:  "",
		},
		{
			name:  "movie by title, unique",
			keys:  playedKeys{section: "movie", title: "Blade Runner", year: 1982},
			cands: []playedCandidate{cand("/m/Blade.Runner.1982.mkv", "", 0), cand("/m/Heat.1995.mkv", "", 1)},
			want:  "/m/Blade.Runner.1982.mkv",
		},
		{
			name:  "a title is not a prefix of a longer one",
			keys:  playedKeys{section: "movie", title: "Alien"},
			cands: []playedCandidate{cand("/m/Aliens.1986.mkv", "", 0)},
			want:  "",
		},
		{
			name:  "two movies, the year tells them apart",
			keys:  playedKeys{section: "movie", title: "Dune", year: 2021},
			cands: []playedCandidate{cand("/m/Dune.1984.mkv", "", 0), cand("/m/Dune.2021.mkv", "", 1)},
			want:  "/m/Dune.2021.mkv",
		},
		{
			name:  "two equally good movies mark none",
			keys:  playedKeys{section: "movie", title: "Dune"},
			cands: []playedCandidate{cand("/m/Dune.1984.mkv", "", 0), cand("/m/Dune.2021.mkv", "", 1)},
			want:  "",
		},
		{
			name:  "episode with matching series, season and episode",
			keys:  playedKeys{section: "show", seriesTitle: "Some Show", season: si(2), episode: si(3)},
			cands: []playedCandidate{cand(show+"Some.Show.S02E02.mkv", "", 0), cand(show+"Some.Show.S02E03.mkv", "", 1)},
			want:  show + "Some.Show.S02E03.mkv",
		},
		{
			name:  "two releases of the same episode mark none",
			keys:  playedKeys{section: "show", seriesTitle: "Some Show", season: si(2), episode: si(3)},
			cands: []playedCandidate{cand(show+"Some.Show.S02E03.1080p.mkv", "", 0), cand(show+"Some.Show.S02E03.720p.mkv", "", 1)},
			want:  "",
		},
		{
			name:  "series title alone across a season marks none",
			keys:  playedKeys{section: "show", seriesTitle: "Some Show"},
			cands: []playedCandidate{cand(show+"Some.Show.S02E02.mkv", "", 0), cand(show+"Some.Show.S02E03.mkv", "", 1)},
			want:  "",
		},
		{
			name:  "right episode of the wrong series marks none",
			keys:  playedKeys{section: "show", seriesTitle: "Some Show", season: si(1), episode: si(1)},
			cands: []playedCandidate{cand("/tv/Other.Show.S01E01.mkv", "", 0)},
			want:  "",
		},
		{
			name:  "right series, right episode number, wrong season marks none",
			keys:  playedKeys{section: "show", seriesTitle: "Some Show", season: si(3), episode: si(1)},
			cands: []playedCandidate{cand(show+"Some.Show.S02E01.mkv", "", 0)},
			want:  "",
		},
		{
			name:  "nothing opened",
			keys:  playedKeys{section: "movie", title: "Heat"},
			cands: nil,
			want:  "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := choosePlayedFile(tt.keys, tt.cands)
			if tt.want == "" {
				if ok {
					t.Fatalf("marked %s, want none", got.path)
				}
				return
			}
			if !ok || got.path != tt.want {
				t.Fatalf("got (%q, %v), want %q", got.path, ok, tt.want)
			}
		})
	}
}

// A template that sends the season as a string, or something odd, must not break the
// payload: the webhook would stop confirming playback.
func TestOptIntNeverFailsThePayload(t *testing.T) {
	var p struct {
		A optInt `json:"a"`
		B optInt `json:"b"`
		C optInt `json:"c"`
		D optInt `json:"d"`
	}
	if err := json.Unmarshal([]byte(`{"a":2,"b":"3","c":"","d":{"x":1}}`), &p); err != nil {
		t.Fatal(err)
	}
	if !p.A.ok || p.A.v != 2 || !p.B.ok || p.B.v != 3 || p.C.ok || p.D.ok {
		t.Fatalf("%+v", p)
	}
}
