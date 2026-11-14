package daemon

import (
	"sync"
	"testing"
)

var testApps sync.Map // *testing.T -> *App

// newTestApp installs a fresh App for the test and restores the previous one when it
// ends. Tests that seed or reset the shared read-path maps call it first, so no test
// sees another's state. A second call for the same t returns the App already installed,
// so setup helpers can each call it without swapping state mid-test.
func newTestApp(t *testing.T) *App {
	t.Helper()
	if a, ok := testApps.Load(t); ok {
		return a.(*App)
	}
	prev := app
	a := newApp()
	app = a
	testApps.Store(t, a)
	t.Cleanup(func() {
		app = prev
		testApps.Delete(t)
	})
	return a
}

func TestTwoAppsDoNotShareState(t *testing.T) {
	a, b := newApp(), newApp()
	a.activePumps.Store("/m/a.mkv", &NativePumpState{})
	a.playbackRegistry.Store("/m/a.mkv", &PlaybackState{Path: "/m/a.mkv"})
	a.activeHandles.Store(&MkvHandle{}, true)
	a.pumpTimers.Store("/m/a.mkv", true)
	a.priorityTimers.Store("/m/a.mkv", true)
	a.inFlightFetches.Store("k", true)
	a.inFlightPrefetches.Store("k", true)

	for name, m := range map[string]interface{ Range(func(k, v any) bool) }{
		"activePumps": &b.activePumps, "playbackRegistry": &b.playbackRegistry,
		"activeHandles": &b.activeHandles, "pumpTimers": &b.pumpTimers,
		"priorityTimers": &b.priorityTimers, "inFlightFetches": &b.inFlightFetches,
		"inFlightPrefetches": &b.inFlightPrefetches,
	} {
		m.Range(func(_, _ any) bool { t.Errorf("%s leaked into a second App", name); return false })
	}
}
