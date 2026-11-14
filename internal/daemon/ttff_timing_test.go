package daemon

import (
	"strings"
	"testing"
	"time"
)

func TestTTFFTiming(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	tests := []struct {
		name      string
		env       map[string]string
		idle      time.Duration
		tick      time.Duration
		wantNotes int
		ignored   bool
	}{
		{"unset keeps the defaults", nil, ttffIdleClose, ttffDefaultTick, 0, false},
		{"both set", map[string]string{envTestSessionIdleClose: "5s", envTestSessionTick: "2s"}, 5 * time.Second, 2 * time.Second, 2, false},
		{"bounds are inclusive", map[string]string{envTestSessionIdleClose: "1s", envTestSessionTick: "10m"}, time.Second, 10 * time.Minute, 2, false},
		{"below the minimum", map[string]string{envTestSessionIdleClose: "999ms"}, ttffIdleClose, ttffDefaultTick, 1, true},
		{"above the maximum", map[string]string{envTestSessionTick: "11m"}, ttffIdleClose, ttffDefaultTick, 1, true},
		{"not a duration", map[string]string{envTestSessionIdleClose: "soon"}, ttffIdleClose, ttffDefaultTick, 1, true},
		{"bare number", map[string]string{envTestSessionTick: "5"}, ttffIdleClose, ttffDefaultTick, 1, true},
		{"negative", map[string]string{envTestSessionIdleClose: "-5s"}, ttffIdleClose, ttffDefaultTick, 1, true},
		{"one good one bad", map[string]string{envTestSessionIdleClose: "5s", envTestSessionTick: "0s"}, 5 * time.Second, ttffDefaultTick, 2, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			idle, tick, notes := ttffTiming(env(tc.env))
			if idle != tc.idle || tick != tc.tick {
				t.Fatalf("idle %s tick %s, want %s %s", idle, tick, tc.idle, tc.tick)
			}
			if len(notes) != tc.wantNotes {
				t.Fatalf("notes %q, want %d", notes, tc.wantNotes)
			}
			if tc.ignored && !strings.Contains(notes[0], "ignored") {
				t.Fatalf("note %q should say the value was ignored", notes[0])
			}
		})
	}
}
