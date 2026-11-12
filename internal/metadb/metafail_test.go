package metadb

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// The library manager asserts this signature on its registry at run time.
var _ interface{ ClearMetadataFailure(string) error } = (*DB)(nil)

type nopLogger struct{}

func (nopLogger) Printf(string, ...interface{}) {}

func openTest(t *testing.T) *DB {
	t.Helper()
	d, err := New(filepath.Join(t.TempDir(), "state.db"), nopLogger{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func one(t *testing.T, d *DB, hash string) Readability {
	t.Helper()
	all, err := d.ListReadability(0)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range all {
		if r.Hash == hash {
			return r
		}
	}
	t.Fatalf("no record for %s in %+v", hash, all)
	return Readability{}
}

func has(t *testing.T, d *DB, hash string) bool {
	t.Helper()
	all, err := d.ListReadability(0)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range all {
		if r.Hash == hash {
			return true
		}
	}
	return false
}

var t0 = time.Unix(1_700_000_000, 0)

func TestStreakTransitions(t *testing.T) {
	d := openTest(t)
	const h = "aaaa"
	if err := d.RecordMetadataFailure(h, "no_peers", "mount", t0); err != nil {
		t.Fatal(err)
	}
	if err := d.RecordMetadataFailure(h, "timeout", "mount", t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	r := one(t, d, h)
	if r.FailCount != 2 || r.FirstFail != t0.Unix() || r.LastFail != t0.Add(time.Hour).Unix() ||
		r.LastReason != "timeout" || r.LastSource != "mount" || r.LastSwarmRead != 0 {
		t.Fatalf("after two failures: %+v", r)
	}

	if err := d.RecordSwarmRead(h, t0.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	r = one(t, d, h)
	if r.FailCount != 0 || r.FirstFail != 0 || r.LastSwarmRead != t0.Add(2*time.Hour).Unix() ||
		r.LastFail != t0.Add(time.Hour).Unix() || r.LastReason != "timeout" {
		t.Fatalf("after a swarm read: %+v", r)
	}

	// An older stamp never moves the time back.
	if err := d.RecordSwarmRead(h, t0); err != nil {
		t.Fatal(err)
	}
	if got := one(t, d, h).LastSwarmRead; got != t0.Add(2*time.Hour).Unix() {
		t.Fatalf("stamp moved back to %d", got)
	}

	// A new streak starts with a new first_fail.
	if err := d.RecordMetadataFailure(h, "no_peers", "mount", t0.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	r = one(t, d, h)
	if r.FailCount != 1 || r.FirstFail != t0.Add(3*time.Hour).Unix() || r.LastReason != "no_peers" {
		t.Fatalf("new streak: %+v", r)
	}
}

func TestMetainfoResetKeepsStampAndCreatesNoRow(t *testing.T) {
	d := openTest(t)
	if err := d.ResetMetadataFailures("bbbb"); err != nil {
		t.Fatal(err)
	}
	if has(t, d, "bbbb") {
		t.Fatal("a reset created a row")
	}
	if err := d.RecordSwarmRead("cccc", t0); err != nil {
		t.Fatal(err)
	}
	if err := d.RecordMetadataFailure("cccc", "timeout", "mount", t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := d.ResetMetadataFailures("cccc"); err != nil {
		t.Fatal(err)
	}
	r := one(t, d, "cccc")
	if r.FailCount != 0 || r.FirstFail != 0 || r.LastSwarmRead != t0.Unix() {
		t.Fatalf("after reset: %+v", r)
	}
}

func TestSwarmReadWithoutRowInsertsOne(t *testing.T) {
	d := openTest(t)
	if err := d.RecordSwarmRead("dddd", t0); err != nil {
		t.Fatal(err)
	}
	r := one(t, d, "dddd")
	if r.FailCount != 0 || r.FirstFail != 0 || r.LastFail != 0 || r.LastSwarmRead != t0.Unix() {
		t.Fatalf("fresh row: %+v", r)
	}
}

func TestListReadabilityFilter(t *testing.T) {
	d := openTest(t)
	for i := 0; i < 3; i++ {
		_ = d.RecordMetadataFailure("3333", "timeout", "mount", t0)
	}
	_ = d.RecordMetadataFailure("1111", "timeout", "mount", t0)
	_ = d.RecordSwarmRead("0000", t0)
	hashes := func(min int) []string {
		rows, err := d.ListReadability(min)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range rows {
			out = append(out, r.Hash)
		}
		return out
	}
	if got := hashes(0); len(got) != 3 || got[0] != "0000" || got[1] != "1111" || got[2] != "3333" {
		t.Fatalf("all = %v", got)
	}
	if got := hashes(1); len(got) != 2 || got[0] != "1111" {
		t.Fatalf("min 1 = %v", got)
	}
	if got := hashes(2); len(got) != 1 || got[0] != "3333" {
		t.Fatalf("min 2 = %v", got)
	}
}

func TestPruneReadability(t *testing.T) {
	d := openTest(t)
	old := t0
	_ = d.RecordMetadataFailure("keepme", "timeout", "mount", old)
	_ = d.RecordMetadataFailure("gone", "timeout", "mount", old)
	_ = d.RecordSwarmRead("goneread", old)
	_ = d.RecordMetadataFailure("recent", "timeout", "mount", old.Add(2*time.Hour))
	_ = d.RecordSwarmRead("recentread", old.Add(2*time.Hour))

	// A mixed-case row still matches its lower-case keep entry.
	if _, err := d.db.Exec(`INSERT INTO metadata_failures (hash, fail_count, first_fail, last_fail) VALUES ('KEEPME2', 1, ?, ?)`, old.Unix(), old.Unix()); err != nil {
		t.Fatal(err)
	}
	n, err := d.PruneReadability(map[string]bool{"keepme": true, "keepme2": true}, old.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("pruned %d, want 2", n)
	}
	for _, h := range []string{"gone", "goneread"} {
		if has(t, d, h) {
			t.Errorf("%s survived", h)
		}
	}
	for _, h := range []string{"keepme", "KEEPME2", "recent", "recentread"} {
		if !has(t, d, h) {
			t.Errorf("%s was pruned", h)
		}
	}
}

func TestClearMetadataFailureDeletes(t *testing.T) {
	d := openTest(t)
	_ = d.RecordSwarmRead("eeee", t0)
	if err := d.ClearMetadataFailure("eeee"); err != nil {
		t.Fatal(err)
	}
	if has(t, d, "eeee") {
		t.Fatal("row survived")
	}
}

// A database from before schema 12 keeps its rows and gains the columns.
func TestMigrationFromTheFourColumnTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE schema_version (version INTEGER PRIMARY KEY, description TEXT, applied_at INTEGER DEFAULT 0)`,
		`CREATE TABLE metadata_failures (hash TEXT PRIMARY KEY, fail_count INTEGER NOT NULL DEFAULT 0, first_fail INTEGER NOT NULL, last_fail INTEGER NOT NULL)`,
		`INSERT INTO metadata_failures VALUES ('oldrow', 4, 100, 200)`,
		`INSERT INTO schema_version (version, description) VALUES (11, 'x')`,
	} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	_ = raw.Close()

	for i := 0; i < 2; i++ {
		d, err := New(path, nopLogger{})
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		r := one(t, d, "oldrow")
		if r.FailCount != 4 || r.FirstFail != 100 || r.LastFail != 200 ||
			r.LastReason != "" || r.LastSource != "" || r.LastSwarmRead != 0 {
			t.Fatalf("open %d: %+v", i, r)
		}
		var v int
		if err := d.db.QueryRow(`SELECT COUNT(*) FROM schema_version WHERE version = 12`).Scan(&v); err != nil || v != 1 {
			t.Fatalf("open %d: version 12 rows = %d, err %v", i, v, err)
		}
		_ = d.Close()
	}
}
