package daemon

import (
	"errors"
	"flodni/internal/flog"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flodni/internal/metadb"
)

func resetReadabilityGauge(t *testing.T) {
	t.Helper()
	readabilityGauge.Store(nil)
	t.Cleanup(func() { readabilityGauge.Store(nil) })
}

func TestCountReadabilityBuckets(t *testing.T) {
	rows := []metadb.Readability{
		{Hash: "f1", FailCount: 1},
		{Hash: "f2", FailCount: 3, LastSwarmRead: 5}, // a streak wins over an old read
		{Hash: "o1", LastSwarmRead: 100},
		{Hash: "r1"}, // reset row: no streak, no read
	}
	c, err := countReadability(&fakeLister{rows: rows}, 6)
	if err != nil {
		t.Fatal(err)
	}
	if want := (readabilityCounts{ok: 1, failed: 2, neverRead: 3}); c != want {
		t.Fatalf("counts %+v, want %+v", c, want)
	}

	// Rows of torrents that left the library can outnumber the library: clamp at 0.
	c, _ = countReadability(&fakeLister{rows: rows}, 2)
	if c.neverRead != 0 {
		t.Fatalf("never_read = %d, want clamped to 0", c.neverRead)
	}
}

func TestCountReadabilityResetRowIsNeverRead(t *testing.T) {
	db, err := metadb.New(filepath.Join(t.TempDir(), "state.db"), discardLogger{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Now()
	if err := db.RecordMetadataFailure("aa", "no_peers", "mount", now); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordMetadataFailure("bb", "no_peers", "mount", now); err != nil {
		t.Fatal(err)
	}
	if err := db.ResetMetadataFailures("bb"); err != nil { // metainfo: ends the streak, no read stamped
		t.Fatal(err)
	}
	if err := db.RecordSwarmRead("cc", now); err != nil {
		t.Fatal(err)
	}
	c, err := countReadability(db, 4) // aa failed, bb reset, cc ok, dd no row
	if err != nil {
		t.Fatal(err)
	}
	if want := (readabilityCounts{ok: 1, failed: 1, neverRead: 2}); c != want {
		t.Fatalf("counts %+v, want %+v", c, want)
	}
}

func TestReadabilityGaugeScrape(t *testing.T) {
	resetReadabilityGauge(t)
	if strings.Contains(scrape(t).Body.String(), "flodni_readability_torrents") {
		t.Fatal("series present before any refresh (state database off)")
	}

	l := flog.NewText(io.Discard)
	refreshReadabilityGauge(&fakeLister{rows: []metadb.Readability{{Hash: "o", LastSwarmRead: 1}}}, 3, l)
	body := scrape(t).Body.String()
	for _, want := range []string{
		`flodni_readability_torrents{state="ok"} 1`,
		`flodni_readability_torrents{state="failed"} 0`,
		`flodni_readability_torrents{state="never_read"} 2`,
	} {
		if !strings.Contains(body, want+"\n") {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}

	// A failing refresh keeps the last numbers.
	refreshReadabilityGauge(&fakeLister{err: errors.New("locked")}, 3, l)
	if c := readabilityGauge.Load(); c == nil || c.ok != 1 {
		t.Fatalf("failed refresh replaced the counts: %+v", c)
	}
}
