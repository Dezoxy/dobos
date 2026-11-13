package main

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// Keep-alive (Reliability step 8, phase b): while a file has a webhook-confirmed
// playback, its torrent is not closed for idleness. The engine's wrapper asks
// keepaliveHold (torr.HoldExpiry, wired in main) for every torrent that would otherwise
// expire; the answer derives from the playback state each time, so a reopened torrent
// needs nothing reapplied and nothing is left to clear.
//
// keepaliveHold runs under the engine's muTorrent on the idle ticker. It must never call
// into the engine (Status, Stats and the like take the same non-reentrant lock): this
// file and confirmed_counters.go import no engine package, and a test pins that.
//
// A torrent is held while a file of it is confirmed and not stopped and the last
// activity (the later of the last read and the last play/resume webhook; a pause adds
// nothing) is under playback_keepalive_minutes old. At most
// playback_keepalive_max_torrents are held: the most recently active win, the rest fall
// back to the plain idle close. playback_keepalive_minutes 0 turns it all off.

// keepaliveSettings returns the live minutes and cap. A var so tests set it.
var keepaliveSettings = func() (minutes, maxTorrents int) {
	if c := gc(); c != nil {
		return c.PlaybackKeepaliveMinutes, c.PlaybackKeepaliveMaxTorrents
	}
	return 15, 4
}

type holdVerdict int

const (
	verdictStop    holdVerdict = iota // confirmation gone: stop webhook or the registry dropped it
	verdictCeiling                    // confirmed but idle past the ceiling
	verdictEvicted                    // confirmed and recent but over the cap
	verdictHold
)

// heldStaleAfter is how long after its last ask a held torrent still counts in the gauge.
// The ticker asks every second; a torrent that stopped being asked has a reader again or
// is closed.
const heldStaleAfter = 3 * time.Second

// keepalive is the held set, the edges and the cached verdicts. Lock order:
// keepalive.mu, then confirmedHashes.mu, then a PlaybackState's mu.
var keepalive struct {
	mu          sync.Mutex
	held        map[string]time.Time // hash -> last ask that returned true
	evictedSeen map[string]struct{}  // over-cap hashes already counted this episode
	verdicts    map[string]holdVerdict
	gen         uint64 // snapshot generation the verdicts were computed from
	minutes     int
	max         int
}

// keepaliveHold is torr.HoldExpiry: true keeps the torrent open for this tick.
func keepaliveHold(hash string) bool {
	if hash == "" {
		return false
	}
	minutes, max := keepaliveSettings()
	keepalive.mu.Lock()
	defer keepalive.mu.Unlock()
	if minutes <= 0 {
		keepalive.held = nil
		keepalive.evictedSeen = nil
		keepalive.verdicts = nil
		return false
	}
	now := ensureVerdicts(minutes, max, hash)
	_, wasHeld := keepalive.held[hash]
	v, ok := keepalive.verdicts[hash]
	if !ok {
		v = verdictStop
	}
	if v == verdictHold {
		if keepalive.held == nil {
			keepalive.held = make(map[string]time.Time)
		}
		keepalive.held[hash] = now
		return true
	}
	if wasHeld {
		delete(keepalive.held, hash)
		switch v {
		case verdictStop:
			keepaliveCounters.releases[0].Add(1)
		case verdictCeiling:
			keepaliveCounters.releases[1].Add(1)
		}
	}
	if v == verdictEvicted {
		if _, seen := keepalive.evictedSeen[hash]; !seen {
			if keepalive.evictedSeen == nil {
				keepalive.evictedSeen = make(map[string]struct{})
			}
			keepalive.evictedSeen[hash] = struct{}{}
			keepaliveCounters.evicted.Add(1)
		}
	}
	return false
}

// rankHolds decides every confirmed torrent in the snapshot: held (recent and within
// the cap, most recently active first, hash as the tie-break), evicted (recent but over
// the cap) or past the ceiling. A hash not in the snapshot is a stop.
func rankHolds(snap map[string]time.Time, now time.Time, ceiling time.Duration, max int) map[string]holdVerdict {
	out := make(map[string]holdVerdict, len(snap))
	var recent []string
	for h, last := range snap {
		if now.Sub(last) < ceiling {
			recent = append(recent, h)
		} else {
			out[h] = verdictCeiling
		}
	}
	sort.Slice(recent, func(i, j int) bool {
		a, b := snap[recent[i]], snap[recent[j]]
		if !a.Equal(b) {
			return a.After(b)
		}
		return recent[i] < recent[j]
	})
	for i, h := range recent {
		if i < max {
			out[h] = verdictHold
		} else {
			out[h] = verdictEvicted
		}
	}
	return out
}

// ensureVerdicts brings the verdicts up to date with the snapshot and the settings and
// returns the snapshot's clock. keep is the hash being asked about, if any. Called with
// keepalive.mu held.
func ensureVerdicts(minutes, max int, keep string) time.Time {
	snap, gen, now := confirmedSnapshot()
	if keepalive.verdicts == nil || gen != keepalive.gen || minutes != keepalive.minutes || max != keepalive.max {
		ceiling := time.Duration(minutes) * time.Minute
		keepalive.verdicts = rankHolds(snap, now, ceiling, max)
		keepalive.gen, keepalive.minutes, keepalive.max = gen, minutes, max
		pruneHeld(now, keep, ceiling)
		for h := range keepalive.evictedSeen {
			if keepalive.verdicts[h] != verdictEvicted {
				delete(keepalive.evictedSeen, h)
			}
		}
	}
	return now
}

// pruneHeld forgets held torrents not asked about for a ceiling and a minute: no hold
// lasts longer, so the torrent is gone or was busy the whole time. It counts nothing.
// A torrent that is merely not asked for a while (a stop's 30 s grace, a reader) keeps
// its entry, so its release is still counted when the engine next asks. keep is the
// torrent being asked about. Called with keepalive.mu held.
func pruneHeld(now time.Time, keep string, ceiling time.Duration) {
	for h, at := range keepalive.held {
		if h != keep && now.Sub(at) > ceiling+time.Minute {
			delete(keepalive.held, h)
		}
	}
}

// keepaliveHeld is the number of idle torrents the hold is keeping open right now: asked
// about and held within the last heldStaleAfter, and still held by the playback state.
func keepaliveHeld() int {
	minutes, max := keepaliveSettings()
	keepalive.mu.Lock()
	defer keepalive.mu.Unlock()
	if minutes <= 0 {
		return 0
	}
	now := ensureVerdicts(minutes, max, "")
	n := 0
	for h, at := range keepalive.held {
		if now.Sub(at) <= heldStaleAfter && keepalive.verdicts[h] == verdictHold {
			n++
		}
	}
	return n
}

// keepaliveStartupField is the keep-alive part of the [Webhook] startup line: the
// effective settings, and "inactive" until a play webhook has matched a file, because
// without one the hold never applies (README > Play webhook).
func keepaliveStartupField(minutes, maxTorrents int, webhookMatched bool) string {
	if minutes <= 0 {
		return "keep-alive: off"
	}
	s := fmt.Sprintf("keep-alive: on (%d min after the last read or play webhook, max %d torrents)", minutes, maxTorrents)
	if !webhookMatched {
		s += ", inactive until a play webhook matches"
	}
	return s
}
