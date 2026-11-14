package daemon

import "sync"

// App holds the process-wide read-path state that the FUSE handle, the pump, the
// webhook handler and the cleanup manager share. One instance is created in Run()
// and published as app before any goroutine starts; tests install a fresh one with
// newTestApp so they stop sharing state.
type App struct {
	activeHandles      sync.Map // key: *MkvHandle, value: bool
	inFlightPrefetches sync.Map // key: "path:offset", value: bool
	// inFlightFetches dedups the sync FetchBlock fallback: concurrent misses in the same chunk
	// share one fetch instead of each running FetchBlock independently.
	inFlightFetches  sync.Map // key: "path:chunkAlignedOffset", value: *fetchFlight
	activePumps      sync.Map // Map[string]*NativePumpState: one pump per file path
	pumpTimers       sync.Map // key: path, value: *time.Timer
	priorityTimers   sync.Map // key: path, value: *time.Timer
	playbackRegistry sync.Map // path -> *PlaybackState
}

func newApp() *App { return &App{} }

// app is set once in Run() before any goroutine starts and never reassigned there.
var app *App
