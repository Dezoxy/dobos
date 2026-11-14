package daemon

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"flodni/internal/gostorm/native"
	"flodni/internal/warmer"
)

// The WakeStored dep maps the engine's refusal to the warmer's sentinel, so the
// catch-up feeder can tell a hash the engine does not store from a busy wake.
func TestWakeStoredDepMapsNotStoredToTheWarmerSentinel(t *testing.T) {
	old := warmWake
	t.Cleanup(func() { warmWake = old })
	deps := addWarmerDeps(nil, nil)

	warmWake = func(context.Context, string) error { return fmt.Errorf("%w: malformed hash", native.ErrNotStored) }
	if err := deps.WakeStored(context.Background(), "aa"); !errors.Is(err, warmer.ErrNotStored) {
		t.Fatalf("err = %v, want warmer.ErrNotStored", err)
	}
	busy := errors.New("too many pending wakes")
	warmWake = func(context.Context, string) error { return busy }
	if err := deps.WakeStored(context.Background(), "aa"); !errors.Is(err, busy) || errors.Is(err, warmer.ErrNotStored) {
		t.Fatalf("err = %v, want the busy error untouched", err)
	}
	warmWake = func(context.Context, string) error { return nil }
	if err := deps.WakeStored(context.Background(), "aa"); err != nil {
		t.Fatal(err)
	}
}
