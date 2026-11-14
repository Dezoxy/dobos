package daemon

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flodni/internal/library"
	"flodni/internal/promtext"
	"flodni/internal/warmer"
	"flodni/internal/warmup"
)

// Next episode (roadmap step 23): the decision, against a fake library, cache and
// warmer.

const (
	hashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func ep(show string, season, episode int, hash string, idx int) library.Item {
	return library.Item{
		Path: filepath.Join("/src/tv", show, "Season."+twoDigits(season), "x.mkv"),
		Size: 1 << 30, Hash: hash, FileIndex: idx, Season: season, Episode: episode,
		ModTime: time.Unix(1000, 0),
	}
}

func twoDigits(n int) string {
	if n < 10 {
		return "0" + string(rune('0'+n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

type nextAdd struct {
	hash string
	f    warmer.File
	done func()
}

type nextEnv struct {
	n       *nextEpisodeWarm
	items   []library.Item
	enabled bool
	same    bool
	roomErr error
	warm    bool
	listed  int
	adds    []nextAdd
	addOK   bool
}

func newNextEnv(items ...library.Item) *nextEnv {
	e := &nextEnv{items: items, addOK: true}
	n := newNextEpisodeWarm()
	n.on = func() (bool, bool) { return e.enabled, e.same }
	n.list = func() ([]library.Item, error) { e.listed++; return e.items, nil }
	n.room = func(warmup.FileKey, int64) (bool, error) { return e.warm, e.roomErr }
	n.add = func(hash string, f warmer.File, done func()) bool {
		if !e.addOK {
			return false
		}
		e.adds = append(e.adds, nextAdd{hash, f, done})
		return true
	}
	n.logf = func(string, ...interface{}) {}
	e.n = n
	return e
}

func (e *nextEnv) count(o string) int64 { return e.n.counts[o].Load() }

func (e *nextEnv) total() (t int64) {
	for _, o := range nextOutcomes {
		t += e.count(o)
	}
	return t
}

func TestWarmNextOffByDefaultDoesNothing(t *testing.T) {
	e := newNextEnv(ep("Show", 1, 1, hashA, 1), ep("Show", 1, 2, hashB, 1))
	e.n.onConfirmedPlay(warmup.FileKey{Hash: hashA, FileIdx: 1})
	if e.listed != 0 || len(e.adds) != 0 || e.total() != 0 {
		t.Fatalf("listed %d times, %d adds, %d counted: the switch is off", e.listed, len(e.adds), e.total())
	}
	// The switch itself defaults to off, whatever warm on add says.
	if on, _ := warmNextEnabled(); on {
		t.Fatal("warmNextEnabled is true without a config")
	}
}

func TestWarmNextQueuesTheNextEpisodeOnce(t *testing.T) {
	e := newNextEnv(ep("Show", 1, 1, hashA, 1), ep("Show", 1, 2, hashB, 3), ep("Other", 1, 2, hashA, 9))
	e.enabled = true
	k := warmup.FileKey{Hash: hashA, FileIdx: 1}
	e.n.onConfirmedPlay(k)
	if len(e.adds) != 1 || e.adds[0].hash != hashB || e.adds[0].f.Index != 3 || e.adds[0].f.Size != 1<<30 {
		t.Fatalf("adds = %+v, want one for %s/3", e.adds, hashB)
	}
	if e.count(nextQueued) != 1 {
		t.Fatalf("queued = %d", e.count(nextQueued))
	}
	// The same play again, or another episode of the show, while the fill runs.
	e.n.onConfirmedPlay(k)
	if len(e.adds) != 1 || e.count(nextInFlight) != 1 {
		t.Fatalf("a second fill started for the show: adds %d, in_flight %d", len(e.adds), e.count(nextInFlight))
	}
	// Once the fill is over the show may queue again.
	e.adds[0].done()
	e.n.onConfirmedPlay(k)
	if len(e.adds) != 2 {
		t.Fatalf("adds = %d after the fill ended, want 2", len(e.adds))
	}
}

func TestWarmNextSkipsASameTorrentPackByDefault(t *testing.T) {
	e := newNextEnv(ep("Show", 1, 1, hashA, 1), ep("Show", 1, 2, hashA, 2))
	e.enabled = true
	k := warmup.FileKey{Hash: hashA, FileIdx: 1}
	e.n.onConfirmedPlay(k)
	if len(e.adds) != 0 || e.count(nextSameTorrent) != 1 {
		t.Fatalf("adds %d, same_torrent %d: a pack is skipped unless allowed", len(e.adds), e.count(nextSameTorrent))
	}
	e.same = true
	e.n.onConfirmedPlay(k)
	if len(e.adds) != 1 || e.adds[0].hash != hashA || e.adds[0].f.Index != 2 {
		t.Fatalf("adds = %+v, want the pack's file 2 with the switch on", e.adds)
	}
}

func TestWarmNextWithNoRoomSkipsAndFreesTheShow(t *testing.T) {
	e := newNextEnv(ep("Show", 1, 1, hashA, 1), ep("Show", 1, 2, hashB, 1))
	e.enabled, e.roomErr = true, warmup.ErrNoRoom
	e.n.onConfirmedPlay(warmup.FileKey{Hash: hashA, FileIdx: 1})
	if len(e.adds) != 0 || e.count(nextNoRoom) != 1 {
		t.Fatalf("adds %d, no_room %d", len(e.adds), e.count(nextNoRoom))
	}
	e.roomErr = nil
	e.n.onConfirmedPlay(warmup.FileKey{Hash: hashA, FileIdx: 1})
	if len(e.adds) != 1 {
		t.Fatal("the show stayed in flight after a skip")
	}
}

func TestWarmNextOtherOutcomes(t *testing.T) {
	t.Run("last episode, next season, and a movie", func(t *testing.T) {
		e := newNextEnv(ep("Show", 1, 1, hashA, 1), ep("Show", 1, 2, hashA, 2), ep("Show", 2, 1, hashB, 1))
		e.enabled = true
		e.n.onConfirmedPlay(warmup.FileKey{Hash: hashA, FileIdx: 2}) // the last of season 1: season 2 episode 1
		if len(e.adds) != 1 || e.adds[0].hash != hashB {
			t.Fatalf("adds = %+v, want season 2 episode 1", e.adds)
		}
		e.adds[0].done()
		e.n.onConfirmedPlay(warmup.FileKey{Hash: hashB, FileIdx: 1}) // nothing after it
		if e.count(nextNone) != 1 {
			t.Fatalf("no_next = %d", e.count(nextNone))
		}
		before := e.total()
		e.n.onConfirmedPlay(warmup.FileKey{Hash: "cc", FileIdx: 1}) // not an episode of the library
		if e.total() != before {
			t.Fatal("a file that is no library episode was counted")
		}
	})
	t.Run("already warm", func(t *testing.T) {
		e := newNextEnv(ep("Show", 1, 1, hashA, 1), ep("Show", 1, 2, hashB, 1))
		e.enabled, e.warm = true, true
		e.n.onConfirmedPlay(warmup.FileKey{Hash: hashA, FileIdx: 1})
		if len(e.adds) != 0 || e.count(nextAlreadyWarm) != 1 {
			t.Fatalf("adds %d, already_warm %d", len(e.adds), e.count(nextAlreadyWarm))
		}
	})
	t.Run("cache cannot be asked, or the warmer refuses", func(t *testing.T) {
		e := newNextEnv(ep("Show", 1, 1, hashA, 1), ep("Show", 1, 2, hashB, 1))
		e.enabled, e.roomErr = true, errors.New("boom")
		e.n.onConfirmedPlay(warmup.FileKey{Hash: hashA, FileIdx: 1})
		e.roomErr, e.addOK = nil, false
		e.n.onConfirmedPlay(warmup.FileKey{Hash: hashA, FileIdx: 1})
		if e.count(nextError) != 2 || e.count(nextQueued) != 0 {
			t.Fatalf("error %d, queued %d", e.count(nextError), e.count(nextQueued))
		}
	})
}

func TestWarmNextMetricHasEveryOutcome(t *testing.T) {
	var w promtext.Writer
	collectWarmNextProm(&w, nil)
	out := string(w.Bytes())
	for _, o := range nextOutcomes {
		want := `flodni_warm_next_episode_total{outcome="` + o + `"} 0`
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in\n%s", want, out)
		}
	}
}
