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

// A database at schema 13 gains the warm columns once, keeps its played row and
// accepts a warm record.
func TestMigrationAddsFileWarmColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v13.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE schema_version (version INTEGER PRIMARY KEY, description TEXT, applied_at INTEGER DEFAULT 0)`,
		`INSERT INTO schema_version (version, description) VALUES (13, 'x')`,
		`CREATE TABLE file_state (hash TEXT NOT NULL, file_idx INTEGER NOT NULL,
			last_played_at INTEGER NOT NULL DEFAULT 0, evicted_at INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (hash, file_idx))`,
		`INSERT INTO file_state (hash, file_idx, last_played_at) VALUES ('old', 3, 77)`,
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
		var v int
		if err := d.db.QueryRow(`SELECT COUNT(*) FROM schema_version WHERE version = 14`).Scan(&v); err != nil || v != 1 {
			t.Fatalf("open %d: version 14 rows = %d, err %v", i, v, err)
		}
		old := states(t, d)[FileKey{"old", 3}]
		if old.LastPlayedAt != 77 || old.Readability != "" || old.Cache != "" || old.FailedAttempts != 0 {
			t.Fatalf("open %d: old row = %+v", i, old)
		}
		if err := d.SaveFileWarm(FileWarm{Hash: "NEW", FileIdx: 1, Readability: "warmed", Cache: "cached",
			LastSwarmDeliveryAt: 5, UpdatedAt: 9}); err != nil {
			t.Fatal(err)
		}
		if n := states(t, d)[FileKey{"new", 1}]; n.Readability != "warmed" || n.LastSwarmDeliveryAt != 5 {
			t.Fatalf("open %d: new row = %+v", i, n)
		}
		_ = d.Close()
	}
}

// SaveFileWarm replaces the seven warm columns and never touches the played or
// evicted marks.
func TestSaveFileWarmKeepsThePlayedAndEvictedMarks(t *testing.T) {
	d := openTest(t)
	if err := d.MarkFilePlayed("aa", 1, t0); err != nil {
		t.Fatal(err)
	}
	if err := d.MarkFilesEvicted([]FileKey{{"aa", 1}}, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	w := FileWarm{Hash: "AA", FileIdx: 1, Readability: "failed", Cache: "cached", FailedAttempts: 2,
		FirstFailedAt: 10, LastAttemptAt: 20, LastSwarmDeliveryAt: 15, UpdatedAt: 30}
	if err := d.SaveFileWarm(w); err != nil {
		t.Fatal(err)
	}
	s := states(t, d)[FileKey{"aa", 1}]
	if s.LastPlayedAt != t0.Unix() || s.EvictedAt != t0.Add(time.Minute).Unix() {
		t.Fatalf("marks changed: %+v", s)
	}
	if s.Readability != "failed" || s.Cache != "cached" || s.FailedAttempts != 2 || s.FirstFailedAt != 10 ||
		s.LastAttemptAt != 20 || s.LastSwarmDeliveryAt != 15 || s.WarmUpdatedAt != 30 {
		t.Fatalf("warm columns = %+v", s)
	}
	// A new row gets no marks.
	if err := d.SaveFileWarm(FileWarm{Hash: "bb", FileIdx: 0, Readability: "present", UpdatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if s := states(t, d)[FileKey{"bb", 0}]; s.LastPlayedAt != 0 || s.EvictedAt != 0 {
		t.Fatalf("bb/0 = %+v", s)
	}
}

// Eviction sets the cache state in both arms of the upsert and never the readability.
func TestEvictionSetsCacheStateAndKeepsReadability(t *testing.T) {
	d := openTest(t)
	if err := d.SaveFileWarm(FileWarm{Hash: "aa", FileIdx: 1, Readability: "warmed", Cache: "cached", UpdatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	at := t0.Add(time.Hour)
	if err := d.MarkFilesEvicted([]FileKey{{"aa", 1}, {"new", 2}}, at); err != nil {
		t.Fatal(err)
	}
	s := states(t, d)
	if a := s[FileKey{"aa", 1}]; a.Cache != "evicted" || a.Readability != "warmed" || a.WarmUpdatedAt != at.Unix() {
		t.Fatalf("update arm: %+v", a)
	}
	if n := s[FileKey{"new", 2}]; n.Cache != "evicted" || n.Readability != "" || n.WarmUpdatedAt != at.Unix() {
		t.Fatalf("insert arm: %+v", n)
	}
}

// A torrent recorded after the cutoff survives the prune even with no play or eviction.
func TestPruneSparesAFreshWarmRow(t *testing.T) {
	d := openTest(t)
	cutoff := t0.Add(time.Hour)
	if err := d.SaveFileWarm(FileWarm{Hash: "fresh", Readability: "warmed", UpdatedAt: cutoff.Add(time.Minute).Unix()}); err != nil {
		t.Fatal(err)
	}
	if err := d.SaveFileWarm(FileWarm{Hash: "stale", Readability: "warmed", UpdatedAt: t0.Unix()}); err != nil {
		t.Fatal(err)
	}
	dropped, err := d.PruneFileStates(map[string]bool{}, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if len(dropped) != 1 || dropped[0] != "stale" {
		t.Fatalf("dropped %v", dropped)
	}
}

// A database at schema 15 gains queued_at once, keeps its rows and round-trips the
// column (roadmap step 8).
func TestMigrationAddsQueuedAt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v15.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE schema_version (version INTEGER PRIMARY KEY, description TEXT, applied_at INTEGER DEFAULT 0)`,
		`INSERT INTO schema_version (version, description) VALUES (15, 'x')`,
		`CREATE TABLE file_state (hash TEXT NOT NULL, file_idx INTEGER NOT NULL,
			last_played_at INTEGER NOT NULL DEFAULT 0, evicted_at INTEGER NOT NULL DEFAULT 0,
			readability TEXT NOT NULL DEFAULT '', cache_state TEXT NOT NULL DEFAULT '',
			failed_attempts INTEGER NOT NULL DEFAULT 0, first_failed_at INTEGER NOT NULL DEFAULT 0,
			last_attempt_at INTEGER NOT NULL DEFAULT 0, last_swarm_delivery_at INTEGER NOT NULL DEFAULT 0,
			warm_updated_at INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (hash, file_idx))`,
		`INSERT INTO file_state (hash, file_idx, readability, cache_state) VALUES ('old', 3, 'warmed', 'cached')`,
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
		var v int
		if err := d.db.QueryRow(`SELECT COUNT(*) FROM schema_version WHERE version = 16`).Scan(&v); err != nil || v != 1 {
			t.Fatalf("open %d: version 16 rows = %d, err %v", i, v, err)
		}
		old := states(t, d)[FileKey{"old", 3}]
		if old.QueuedAt != 0 || old.Readability != "warmed" || old.Cache != "cached" {
			t.Fatalf("open %d: old row = %+v", i, old)
		}
		if err := d.SaveFileWarm(FileWarm{Hash: "NEW", FileIdx: 1, QueuedAt: 123, UpdatedAt: 9}); err != nil {
			t.Fatal(err)
		}
		if n := states(t, d)[FileKey{"new", 1}]; n.QueuedAt != 123 {
			t.Fatalf("open %d: new row = %+v", i, n)
		}
		if err := d.SaveFileWarm(FileWarm{Hash: "NEW", FileIdx: 1, Cache: "lost", UpdatedAt: 10}); err != nil {
			t.Fatal(err)
		}
		if n := states(t, d)[FileKey{"new", 1}]; n.QueuedAt != 0 || n.Cache != "lost" {
			t.Fatalf("open %d: a save without queued_at must clear it: %+v", i, n)
		}
		_ = d.Close()
	}
}
