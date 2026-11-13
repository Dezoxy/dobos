package warmer

import "time"

// The catch-up feeder (Dezoxy fork, Readiness step 3): it hands the library's
// never-warmed files to the warmer a hash at a time, slowly, and ends a pass when the
// cache has no room or its disk misbehaves. It only paces and stops: which files are
// candidates is the caller's list, and every fill, wake and record is the warmer's, so
// a catch-up file is woken only through Deps.WakeStored and never evicts (hard rule 3,
// docs/roadmap.md). The daemon runs it from warm_catchup.go.

// States of the files a pass left, the keys of PassResult.Left.
const (
	LeftNoRoom        = "left_no_room"        // the cache is full: catch-up never evicts
	LeftNotStored     = "not_stored"          // the engine refused to wake the hash
	LeftNoLonger      = "no_longer_candidate" // Still said no just before the feed
	LeftCacheFailures = "left_cache_failures" // the pass ended on its third cache-side failure
	LeftRateOff       = "left_rate_off"       // the pace was set to 0
	LeftStopped       = "left_stopped"        // the feeder was stopped
)

// MaxCacheFailures is how many cache-side failures (lost, write_failed) of its own
// files end a pass: a failing disk would otherwise be tried file after file.
const MaxCacheFailures = 3

// Candidate is the never-warmed files of one hash.
type Candidate struct {
	Hash  string
	Files []File
}

// Target is what the feeder needs of the warmer; *Warmer is one.
type Target interface {
	AddCatchup(hash string, files []File) int
	Idle() bool
	NoRoom() bool
	ResetNoRoom()
	NotStored(hash string) bool
	CatchupCacheFailures() int64
}

// CatchupCacheFailures counts the lost and write_failed outcomes of catch-up files.
func (w *Warmer) CatchupCacheFailures() int64 {
	if w == nil {
		return 0
	}
	c := w.Stats().ByTrigger[TriggerCatchup]
	return c.Fills[OutcomeLost] + c.Fills[OutcomeWriteFailed]
}

// Feeder runs passes. Fields are set before the first Pass and not changed.
type Feeder struct {
	Target Target
	// Still reports that the file is still a candidate. It runs just before the feed,
	// so a file warmed, evicted or removed since the list was made is not fed. Nil
	// means every file is.
	Still func(hash string, f File) bool
	// PerHour is the files started per hour, read on each feed; 0 or less stops the
	// pass. The wait after a feed is the files fed times an hour over this.
	PerHour func() int
	// Stop ends the pass wherever it waits.
	Stop <-chan struct{}
	Poll time.Duration // how often a wait for an idle warmer looks; 0 means 1 s
}

// PassResult is what one pass did and what it left, counted in files.
type PassResult struct {
	Hashes int            // hashes fed
	Fed    int            // files fed
	Left   map[string]int // by Left* state; absent when 0
	NoRoom bool           // the no-room flag stood when the pass ended
}

// Pass feeds the candidates in order. It feeds one hash only once the warmer is idle
// (nothing queued, nothing running, no pause) and the previous feed's k files have had
// k intervals, so adds are never queued behind it. It ends early on the no-room flag
// (catch-up never evicts), on MaxCacheFailures cache-side failures of its own files,
// on a pace of 0 and on Stop. It waits for the last hash to finish, so the flag and the
// refusals of the last feed are in the result.
func (fd *Feeder) Pass(cands []Candidate) PassResult {
	res := PassResult{Left: map[string]int{}}
	t := fd.Target
	t.ResetNoRoom()
	baseFailures := t.CatchupCacheFailures()
	var next time.Time // when the previous feed's files have had their intervals
	var last Candidate // the hash fed last, until its outcome is read
	var lastFed int
	settle := func() {
		if lastFed > 0 && t.NotStored(last.Hash) {
			res.Left[LeftNotStored] += lastFed
		}
		lastFed = 0
	}
	left := func(state string, from int) {
		for _, c := range cands[from:] {
			res.Left[state] += len(c.Files)
		}
	}
	finish := func() PassResult {
		res.NoRoom = t.NoRoom()
		for k, v := range res.Left {
			if v == 0 {
				delete(res.Left, k)
			}
		}
		return res
	}

	for i, c := range cands {
		// The end checks run before the pace sleep, so a pass that must end reports
		// at once, not an interval later.
		if !fd.waitIdle() {
			settle()
			left(LeftStopped, i)
			return finish()
		}
		settle()
		switch {
		case t.NoRoom():
			left(LeftNoRoom, i)
			return finish()
		case t.CatchupCacheFailures()-baseFailures >= MaxCacheFailures:
			left(LeftCacheFailures, i)
			return finish()
		}
		if !fd.sleepUntil(next) || !fd.waitIdle() {
			left(LeftStopped, i)
			return finish()
		}
		perHour := fd.PerHour()
		if perHour <= 0 {
			left(LeftRateOff, i)
			return finish()
		}
		if t.NotStored(c.Hash) {
			res.Left[LeftNotStored] += len(c.Files)
			continue
		}
		var files []File
		for _, f := range c.Files {
			if fd.Still == nil || fd.Still(c.Hash, f) {
				files = append(files, f)
			}
		}
		res.Left[LeftNoLonger] += len(c.Files) - len(files)
		if len(files) == 0 {
			continue
		}
		k := t.AddCatchup(c.Hash, files)
		if k == 0 {
			res.Left[LeftNoLonger] += len(files) // switched off or removed: nothing queued
			continue
		}
		res.Hashes++
		res.Fed += k
		last, lastFed = c, k
		next = time.Now().Add(time.Duration(k) * (time.Hour / time.Duration(perHour)))
	}
	if !fd.waitIdle() {
		settle()
		return finish()
	}
	settle()
	return finish()
}

// sleepUntil waits for the time, false if Stop closed first.
func (fd *Feeder) sleepUntil(at time.Time) bool {
	d := time.Until(at)
	if d <= 0 {
		return true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-fd.Stop:
		return false
	}
}

// waitIdle waits for an idle warmer, false if Stop closed first.
func (fd *Feeder) waitIdle() bool {
	poll := fd.Poll
	if poll <= 0 {
		poll = time.Second
	}
	for !fd.Target.Idle() {
		select {
		case <-time.After(poll):
		case <-fd.Stop:
			return false
		}
	}
	select {
	case <-fd.Stop:
		return false
	default:
		return true
	}
}
