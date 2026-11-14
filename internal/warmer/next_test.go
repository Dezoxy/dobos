package warmer

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"flodni/internal/warmup"
)

// The next-episode fill (roadmap step 23, opt-in): it runs while a playback is open,
// never evicts, and leaves the catch-up flag alone.

func nextFills(e *env, outcome string) int64 {
	return e.w.Stats().ByTrigger[TriggerNext].Fills[outcome]
}

func TestAddNextFillsDuringAPlaybackAndNeverEvicts(t *testing.T) {
	e := newEnv(t, nil)
	e.set(func() { e.openFn = func() bool { return true } }) // a playback is open throughout
	var done atomic.Int32
	if !e.w.AddNext("AA", f(2), func() { done.Add(1) }) {
		t.Fatal("AddNext refused with the switch on")
	}
	e.until("the fill", func() bool { return done.Load() == 1 })
	e.mu.Lock()
	defer e.mu.Unlock()
	var real []warmup.FillRequest
	for _, r := range e.reqs {
		if !r.RoomOnly {
			real = append(real, r)
		}
	}
	if len(real) != 1 || real[0].Key != (warmup.FileKey{Hash: "aa", FileIdx: 2}) {
		t.Fatalf("fills = %+v, want one for aa/2", e.reqs)
	}
	if real[0].Policy != warmup.EvictNone || real[0].Trigger != TriggerNext {
		t.Fatalf("policy = %v, trigger = %q, want EvictNone and next", real[0].Policy, real[0].Trigger)
	}
	if got := nextFills(e, OutcomeWarmed); got != 1 {
		t.Fatalf("next warmed = %d, want 1", got)
	}
	if got := e.w.Stats().ByTrigger[Trigger].Fills[OutcomeWarmed]; got != 0 {
		t.Fatalf("an add counter moved: %d", got)
	}
}

func TestAddNextWithNoRoomFillsNothingAndLeavesTheCatchupFlag(t *testing.T) {
	e := newEnv(t, nil)
	e.set(func() {
		e.fill = func(_ int, req warmup.FillRequest) (warmup.FillResult, error) {
			return warmup.FillResult{}, warmup.ErrNoRoom
		}
	})
	var done atomic.Int32
	e.w.AddNext("aa", f(2), func() { done.Add(1) })
	e.until("the attempt", func() bool { return done.Load() == 1 })
	if got := nextFills(e, OutcomeNoRoom); got != 1 {
		t.Fatalf("next no_room = %d, want 1", got)
	}
	if e.w.NoRoom() {
		t.Fatal("a next-episode fill raised the catch-up no-room flag")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, r := range e.reqs {
		if !r.RoomOnly {
			t.Fatalf("a fill ran with no room: %+v", r)
		}
	}
	if len(e.wakes) != 0 {
		t.Fatal("a torrent was woken for a fill with no room")
	}
}

func TestAddNextIsRefusedWithTheSwitchOff(t *testing.T) {
	e := newEnv(t, nil)
	e.set(func() { e.enabled = false })
	called := false
	if e.w.AddNext("aa", f(2), func() { called = true }) {
		t.Fatal("AddNext took a file with the switch off")
	}
	e.quiet(0, 0)
	if called {
		t.Fatal("done ran for a refused file")
	}
	var nilW *Warmer
	if nilW.AddNext("aa", f(2), nil) {
		t.Fatal("a nil warmer took a file")
	}
}

func TestAddNextIsNotRetriedAfterACacheFailure(t *testing.T) {
	e := newEnv(t, func(d *Deps) { d.RetryDelays = []time.Duration{time.Millisecond} })
	e.set(func() {
		e.fill = func(_ int, req warmup.FillRequest) (warmup.FillResult, error) {
			if req.RoomOnly {
				return warmup.FillResult{}, nil
			}
			return warmup.FillResult{}, errors.New("disk full")
		}
	})
	var done atomic.Int32
	e.w.AddNext("aa", f(2), func() { done.Add(1) })
	e.until("the attempt", func() bool { return done.Load() == 1 })
	if got := nextFills(e, OutcomeWriteFailed); got != 1 {
		t.Fatalf("write_failed = %d, want 1", got)
	}
	time.Sleep(40 * time.Millisecond)
	if got := e.w.Stats().QueueFiles; got != 0 {
		t.Fatalf("a failed next-episode fill was queued again: %d files", got)
	}
}
