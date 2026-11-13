package main

import (
	"sync"
	"sync/atomic"
	"time"
)

// Keep-alive counters (Reliability step 8, phase a): how often the engine closes an
// idle torrent, how often a read wakes one again and how often a served read stalls,
// each split by whether a file of the torrent had a webhook-confirmed playback. They
// change no behaviour. Index 1 is confirmed, 0 is not.
var keepaliveCounters struct {
	idleCloses [2]atomic.Int64
	reopens    [2]atomic.Int64
	stalls     [2]atomic.Int64
}

func confirmedIdx(confirmed bool) int {
	if confirmed {
		return 1
	}
	return 0
}

// confirmedHashes is the set of torrent hashes with a confirmed playback, rebuilt at
// most once a second like playbackOpenCache: scanners register thousands of paths and
// the idle ticker asks every second, so a walk per call would be quadratic. A webhook
// can therefore take up to a second to show in the label.
var (
	confirmedHashesNow = time.Now
	confirmedHashes    struct {
		mu  sync.Mutex
		at  time.Time
		set map[string]struct{}
	}
)

// hashConfirmed reports whether any file of the torrent has a webhook-confirmed
// playback. It takes no engine lock, so the idle-close hook may call it.
func hashConfirmed(hash string) bool {
	if hash == "" {
		return false
	}
	confirmedHashes.mu.Lock()
	defer confirmedHashes.mu.Unlock()
	now := confirmedHashesNow()
	if confirmedHashes.set == nil || now.Sub(confirmedHashes.at) >= time.Second {
		set := make(map[string]struct{})
		playbackRegistry.Range(func(_, v any) bool {
			if ps, ok := v.(*PlaybackState); ok && ps.Hash != "" && ps.GetStatus() {
				set[ps.Hash] = struct{}{}
			}
			return true
		})
		confirmedHashes.set, confirmedHashes.at = set, now
	}
	_, ok := confirmedHashes.set[hash]
	return ok
}

// countIdleClose is the torr.OnIdleClose hook.
func countIdleClose(hash string) {
	keepaliveCounters.idleCloses[confirmedIdx(hashConfirmed(hash))].Add(1)
}

// countReopen counts a read-path wake of a closed torrent. The wake at Open is not one.
func countReopen(path, hash string) {
	keepaliveCounters.reopens[confirmedIdx(hashConfirmed(hash))].Add(1)
	if v, ok := sessions.Load(path); ok {
		v.(*TTFFSession).reopens.Add(1)
	}
}

// countStall counts a served read over the stall threshold, labelled by the torrent
// of the path: the session's hash, else the registry state's.
func countStall(path string) {
	hash := ""
	if v, ok := sessions.Load(path); ok {
		hash = v.(*TTFFSession).hash
	}
	if hash == "" {
		if v, ok := playbackRegistry.Load(path); ok {
			if ps, ok := v.(*PlaybackState); ok {
				hash = ps.Hash
			}
		}
	}
	keepaliveCounters.stalls[confirmedIdx(hashConfirmed(hash))].Add(1)
}
