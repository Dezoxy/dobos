package library

import (
	"context"
	"reflect"
	"testing"
)

// clearingRegistry is a memRegistry that also takes the optional method the manager
// asserts on the registry, so the assertion is exercised through Remove.
type clearingRegistry struct {
	*memRegistry
	cleared []string
}

func (r *clearingRegistry) ClearMetadataFailure(hash string) error {
	r.cleared = append(r.cleared, hash)
	return nil
}

func TestRemoveClearsTheReadabilityRecord(t *testing.T) {
	e := newCharEnv(t, movieTorrent...)
	reg := &clearingRegistry{memRegistry: e.reg}
	e.m.cfg.Registry = reg
	if _, err := e.add(AddRequest{Title: "Inception", Hash: charPackHash, Year: 2010}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Remove(context.Background(), RemoveRequest{Hash: charPackHash}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reg.cleared, []string{charPackHash}) {
		t.Fatalf("cleared = %v, want the removed hash", reg.cleared)
	}
}
