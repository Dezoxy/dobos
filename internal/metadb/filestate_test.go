package metadb

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func states(t *testing.T, d *DB) map[FileKey]FileState {
	t.Helper()
	all, err := d.LoadFileStates()
	if err != nil {
		t.Fatal(err)
	}
	m := map[FileKey]FileState{}
	for _, s := range all {
		m[FileKey{s.Hash, s.FileIdx}] = s
	}
	return m
}

func TestFilePlayedKeepsTheLatestTimeAndLowercasesTheHash(t *testing.T) {
	d := openTest(t)
	if err := d.MarkFilePlayed("ABCD", 2, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := d.MarkFilePlayed("abcd", 2, t0); err != nil { // older: ignored
		t.Fatal(err)
	}
	if got := states(t, d)[FileKey{"abcd", 2}].LastPlayedAt; got != t0.Add(time.Hour).Unix() {
		t.Fatalf("last_played_at = %d", got)
	}
	if err := d.MarkFilePlayed("abcd", 2, t0.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := states(t, d)[FileKey{"abcd", 2}].LastPlayedAt; got != t0.Add(2*time.Hour).Unix() {
		t.Fatalf("a later play did not move the time: %d", got)
	}
}

func TestFileEvictionKeepsThePlayedTimeAndClears(t *testing.T) {
	d := openTest(t)
	if err := d.MarkFilePlayed("aa", 1, t0); err != nil {
		t.Fatal(err)
	}
	if err := d.MarkFilesEvicted([]FileKey{{"AA", 1}, {"bb", 0}}, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	s := states(t, d)
	if s[FileKey{"aa", 1}].LastPlayedAt != t0.Unix() || s[FileKey{"aa", 1}].EvictedAt != t0.Add(time.Minute).Unix() {
		t.Fatalf("aa/1 = %+v", s[FileKey{"aa", 1}])
	}
	if s[FileKey{"bb", 0}].EvictedAt == 0 || s[FileKey{"bb", 0}].LastPlayedAt != 0 {
		t.Fatalf("bb/0 = %+v", s[FileKey{"bb", 0}])
	}
	if err := d.ClearFileEvicted("AA", 1); err != nil {
		t.Fatal(err)
	}
	if s := states(t, d)[FileKey{"aa", 1}]; s.EvictedAt != 0 || s.LastPlayedAt != t0.Unix() {
		t.Fatalf("after clear: %+v", s)
	}
	if err := d.MarkFilesEvicted(nil, t0); err != nil {
		t.Fatalf("empty batch: %v", err)
	}
}

func TestFileStateDeleteAndPrune(t *testing.T) {
	d := openTest(t)
	old, recent := t0, t0.Add(10*time.Hour)
	for _, h := range []string{"gone", "kept", "fresh", "del"} {
		at := old
		if h == "fresh" {
			at = recent
		}
		if err := d.MarkFilePlayed(h, 0, at); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.DeleteFileStates("DEL"); err != nil {
		t.Fatal(err)
	}
	// "fresh" is outside keep but touched after the cutoff: spared by the time guard.
	dropped, err := d.PruneFileStates(map[string]bool{"kept": true}, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(dropped) != 1 || dropped[0] != "gone" {
		t.Fatalf("dropped %v", dropped)
	}
	s := states(t, d)
	if _, ok := s[FileKey{"gone", 0}]; ok {
		t.Fatal("gone survived")
	}
	for _, h := range []string{"kept", "fresh"} {
		if _, ok := s[FileKey{h, 0}]; !ok {
			t.Fatalf("%s was pruned", h)
		}
	}
	if _, ok := s[FileKey{"del", 0}]; ok {
		t.Fatal("del survived DeleteFileStates")
	}
}

// A database from before schema 13 gains the table and keeps its other rows.
func TestMigrationAddsFileState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE schema_version (version INTEGER PRIMARY KEY, description TEXT, applied_at INTEGER DEFAULT 0)`,
		`INSERT INTO schema_version (version, description) VALUES (12, 'x')`,
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
		if err := d.MarkFilePlayed("h", 1, t0); err != nil {
			t.Fatal(err)
		}
		var v int
		if err := d.db.QueryRow(`SELECT COUNT(*) FROM schema_version WHERE version = 13`).Scan(&v); err != nil || v != 1 {
			t.Fatalf("open %d: version 13 rows = %d, err %v", i, v, err)
		}
		_ = d.Close()
	}
}
