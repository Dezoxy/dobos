package daemon

import (
	"path/filepath"
	"testing"

	"flodni/internal/warmer"
	"flodni/internal/warmup"
)

// Characterization (ADR 3, step 1): a torrent the library drops takes the played mark
// and the warm record of its files with it, in memory and in the state database. A
// replaced or removed file is not archived; the lifecycle API answers 404 for it.
func TestDroppedTorrentLosesItsPlayAndWarmRecordInTheDatabase(t *testing.T) {
	db := testStore(t, filepath.Join(t.TempDir(), "s.db"))
	x := warmIndex(t)
	if err := x.attach(db); err != nil {
		t.Fatal(err)
	}
	k := warmup.FileKey{Hash: "aabb", FileIdx: 2}
	x.MarkPlayed(k)
	x.RecordWarm(warmUpdate{key: k, readability: readWarmed, cache: cacheCached, deliveredAt: wt0})

	w := warmer.New(addWarmerDeps(x, make(chan struct{})))
	onHashRemoved(w, x)("AABB")

	if x.EverPlayed(k) || len(x.WarmSnapshot()["aabb"]) != 0 {
		t.Fatal("memory kept the record of a dropped torrent")
	}
	y := newFileStateIndex(nil, 90)
	if err := y.attach(db); err != nil {
		t.Fatal(err)
	}
	if y.EverPlayed(k) || len(y.WarmSnapshot()["aabb"]) != 0 {
		t.Fatal("the database kept the record of a dropped torrent")
	}
}
