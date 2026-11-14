package daemon

import (
	"errors"
	"flodni/internal/flog"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"flodni/internal/library"
	"flodni/internal/promtext"
	"flodni/internal/warmer"
	"flodni/internal/warmup"
)

// Next episode (roadmap step 23, built, opt-in, off by default): while an episode
// plays, confirmed by the media server, the windows of the episode after it are filled
// so a binge starts at once. It goes through warm on add's warmer (so it needs
// warm_on_add and warmup_dir), looks the next episode up in the library by season and
// episode within the same show, and never evicts. At most one fill is in flight per
// show. An episode of the torrent being played (a season pack) is skipped unless
// warm_next_episode_same_torrent is set: a second reader on the playing torrent
// splits the engine's read-ahead.

// Outcomes of flodni_warm_next_episode_total, one per confirmed play of a library
// episode while warm_next_episode is on.
const (
	nextQueued      = "queued"       // the fill of the next episode was handed to the warmer
	nextNone        = "no_next"      // the library has no next episode of the show
	nextSameTorrent = "same_torrent" // the next episode is in the playing torrent and that is not allowed
	nextNoRoom      = "no_room"      // the cache has no room without evicting
	nextAlreadyWarm = "already_warm" // both windows are on disk already
	nextInFlight    = "in_flight"    // a fill for this show is still running
	nextError       = "error"        // the library could not be listed, or the cache cannot be asked
)

var nextOutcomes = []string{nextQueued, nextNone, nextSameTorrent, nextNoRoom, nextAlreadyWarm, nextInFlight, nextError}

// nextEpisodeWarm is the decision. Everything it touches is a field, so a test needs no
// engine, library or cache.
type nextEpisodeWarm struct {
	// on reports warm_next_episode (with warm on add active) and the same-torrent switch.
	on   func() (enabled, sameTorrent bool)
	list func() ([]library.Item, error)
	// room asks the cache, fetching and evicting nothing: whether both windows are warm
	// already, or ErrNoRoom when the windows would not fit.
	room func(k warmup.FileKey, size int64) (alreadyWarm bool, err error)
	add  func(hash string, f warmer.File, done func()) bool
	logf func(format string, v ...interface{})

	mu       sync.Mutex
	inflight map[string]bool // by show directory
	counts   map[string]*atomic.Int64
}

func newNextEpisodeWarm() *nextEpisodeWarm {
	n := &nextEpisodeWarm{inflight: map[string]bool{}, counts: map[string]*atomic.Int64{}}
	for _, o := range nextOutcomes {
		n.counts[o] = new(atomic.Int64)
	}
	return n
}

// warmNext is the process-wide instance, nil until the library is up (a build or test
// that never created it ignores confirmed plays).
var warmNext *nextEpisodeWarm

var warnWarmNextNoWarmOnAdd sync.Once

// warmNextEnabled is the switch: warm_next_episode on, and the machinery it needs,
// which is warm on add's (warm_on_add and warmup_dir).
func warmNextEnabled() (enabled, sameTorrent bool) {
	c := gc()
	if c == nil || !c.WarmNextEpisode {
		return false, false
	}
	if !warmOnAddActive() {
		warnWarmNextNoWarmOnAdd.Do(func() {
			logger.Warn("WARNING: warm_next_episode is set but warm_on_add and warmup_dir are not both set: the next episode is not warmed")
		})
		return false, false
	}
	return true, c.WarmNextEpisodeSameTorrent
}

// startWarmNext wires the process-wide instance to the library, the cache and the warmer.
func startWarmNext(m *library.Manager) {
	n := newNextEpisodeWarm()
	n.on = warmNextEnabled
	n.list = func() ([]library.Item, error) { return m.List("tv") }
	n.room = func(k warmup.FileKey, size int64) (bool, error) {
		if err := checkStubSize(k, size); err != nil {
			return false, err
		}
		res, err := warmup.DiskWarmup.FillWindows(warmup.FillRequest{
			Key: k, FileSize: size, Policy: warmup.EvictNone, Trigger: warmer.TriggerNext,
			RoomOnly: true})
		return res.AlreadyWarm, err
	}
	n.add = func(hash string, f warmer.File, done func()) bool { return addWarmer.AddNext(hash, f, done) }
	n.logf = flog.Logf(logger)
	warmNext = n
}

// showDir is the directory of a show: the stubs are TVDir/<show>/Season.NN/<file>.
func showDir(it library.Item) string { return filepath.Dir(filepath.Dir(it.Path)) }

// nextOf finds the stub after (season, episode) in the show: the next episode of the
// season, else the first of the next season. The newest stub wins when a release was
// replaced. A stub with no hash, index or size cannot be filled.
func nextOf(items []library.Item, show string, season, episode int) (library.Item, bool) {
	var best library.Item
	found := false
	pick := func(s, e int) {
		for _, it := range items {
			if showDir(it) != show || it.Season != s || it.Episode != e ||
				it.Hash == "" || it.FileIndex <= 0 || it.Size <= 0 {
				continue
			}
			if !found || it.ModTime.After(best.ModTime) {
				best, found = it, true
			}
		}
	}
	pick(season, episode+1)
	if !found {
		pick(season+1, 1)
	}
	return best, found
}

// onConfirmedPlay is called, off the webhook's goroutine, for the single file a
// webhook-confirmed play started. It does nothing with the switch off. A file that is
// not an episode of the library (a movie) is no show and counts nothing.
func (n *nextEpisodeWarm) onConfirmedPlay(k warmup.FileKey) {
	enabled, sameTorrent := n.on()
	if !enabled {
		return
	}
	items, err := n.list()
	if err != nil {
		n.count(nextError)
		n.logf("WARNING: [WarmNext] cannot list the library: %v", err)
		return
	}
	var cur library.Item
	found := false
	for _, it := range items {
		if strings.EqualFold(it.Hash, k.Hash) && it.FileIndex == k.FileIdx && it.Season > 0 && it.Episode > 0 {
			cur, found = it, true
			break
		}
	}
	if !found {
		return
	}
	show := showDir(cur)
	next, ok := nextOf(items, show, cur.Season, cur.Episode)
	if !ok {
		n.count(nextNone)
		return
	}
	if strings.EqualFold(next.Hash, k.Hash) && !sameTorrent {
		n.count(nextSameTorrent)
		return
	}
	n.mu.Lock()
	if n.inflight[show] {
		n.mu.Unlock()
		n.count(nextInFlight)
		return
	}
	n.inflight[show] = true
	n.mu.Unlock()
	release := func() {
		n.mu.Lock()
		delete(n.inflight, show)
		n.mu.Unlock()
	}

	nk := warmup.FileKey{Hash: strings.ToLower(next.Hash), FileIdx: next.FileIndex}
	warm, err := n.room(nk, next.Size)
	switch {
	case errors.Is(err, warmup.ErrNoRoom):
		release()
		n.count(nextNoRoom)
		return
	case err != nil:
		release()
		n.count(nextError)
		n.logf("WARNING: [WarmNext] cannot check the cache for the next episode: %v", err)
		return
	case warm:
		release()
		n.count(nextAlreadyWarm)
		return
	}
	if !n.add(nk.Hash, warmer.File{Index: nk.FileIdx, Size: next.Size}, release) {
		release()
		n.count(nextError)
		return
	}
	n.count(nextQueued)
}

func (n *nextEpisodeWarm) count(outcome string) { n.counts[outcome].Add(1) }

// collectWarmNextProm writes flodni_warm_next_episode_total with every outcome present.
// A nil instance reports zeros.
func collectWarmNextProm(w *promtext.Writer, n *nextEpisodeWarm) {
	s := make([]promtext.Sample, 0, len(nextOutcomes))
	for _, o := range nextOutcomes {
		var v int64
		if n != nil {
			v = n.counts[o].Load()
		}
		s = append(s, promSample(v, lbl("outcome", o)))
	}
	w.Counter("flodni_warm_next_episode_total",
		"Confirmed plays of a library episode seen with warm_next_episode on, by outcome: queued (the next episode's windows were handed to the warmer), no_next (no next episode in the library), same_torrent (the next episode is a file of the playing torrent and warm_next_episode_same_torrent is off), no_room (the cache has no room without evicting), already_warm, in_flight (a fill for the show is still running) and error. Zero while warm_next_episode is off.",
		s...)
}
