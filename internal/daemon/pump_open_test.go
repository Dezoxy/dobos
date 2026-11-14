package daemon

import "testing"

// Dezoxy fork: a scanner's Open must not start the read-ahead pump when pump_on_open is
// off; confirmed playback still does, and the upstream default is unchanged.
func TestStartPumpAtOpen(t *testing.T) {
	cases := []struct {
		pumpOnOpen, confirmed, want bool
	}{
		{true, false, true},   // upstream default: always
		{true, true, true},    // upstream default: always
		{false, false, false}, // scanner Open: no pump, demand fetches only
		{false, true, true},   // webhook-confirmed playback: pump at once
	}
	for _, c := range cases {
		if got := startPumpAtOpen(c.pumpOnOpen, c.confirmed); got != c.want {
			t.Errorf("startPumpAtOpen(%v, %v) = %v, want %v", c.pumpOnOpen, c.confirmed, got, c.want)
		}
	}
}

func TestPlaybackConfirmedReadsTheRegistry(t *testing.T) {
	newTestApp(t)
	const path = "/test/pump-open.mkv"
	defer app.playbackRegistry.Delete(path)
	if playbackConfirmed(path) {
		t.Fatal("unknown path reported as confirmed playback")
	}
	app.playbackRegistry.Store(path, &PlaybackState{Path: path})
	if playbackConfirmed(path) {
		t.Fatal("unconfirmed playback reported as confirmed")
	}
	app.playbackRegistry.Store(path, &PlaybackState{Path: path, IsHealthy: true})
	if !playbackConfirmed(path) {
		t.Fatal("confirmed playback not reported")
	}
}
