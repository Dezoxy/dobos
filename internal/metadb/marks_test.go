package metadb

import (
	"database/sql"
	"log/slog"
	"path/filepath"
	"testing"
	"time"
)

// daemon_marks (schema 15) holds three daemon-level times as Unix seconds. A stamp older
// than the stored one never moves it back, as with MarkFilePlayed.
func TestSetMarkKeepsTheLatestTimeAndLoadMarksReturnsAll(t *testing.T) {
	d := openTest(t)
	if m, err := d.LoadMarks(); err != nil || len(m) != 0 {
		t.Fatalf("fresh DB: %v, %v", m, err)
	}
	for _, at := range []time.Time{t0.Add(time.Hour), t0, t0.Add(2 * time.Hour), t0.Add(time.Hour)} {
		if err := d.SetMark("play_webhook_matched", at); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.SetMark("unconfirmed_read", t0); err != nil {
		t.Fatal(err)
	}
	m, err := d.LoadMarks()
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 2 || m["play_webhook_matched"] != t0.Add(2*time.Hour).Unix() || m["unconfirmed_read"] != t0.Unix() {
		t.Fatalf("marks = %v", m)
	}
}

// A database at schema 14 gains daemon_marks once, keeps its other rows, and the marks
// survive a reopen.
func TestMigrationAddsDaemonMarks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v14.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE schema_version (version INTEGER PRIMARY KEY, description TEXT, applied_at INTEGER DEFAULT 0)`,
		`INSERT INTO schema_version (version, description) VALUES (14, 'x')`,
	} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	_ = raw.Close()
	for i := 0; i < 3; i++ {
		d, err := New(path, slog.New(slog.DiscardHandler))
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		var v int
		if err := d.db.QueryRow(`SELECT COUNT(*) FROM schema_version WHERE version = 15`).Scan(&v); err != nil || v != 1 {
			t.Fatalf("open %d: version 15 rows = %d, err %v", i, v, err)
		}
		if i == 0 {
			if err := d.SetMark("play_webhook_unmatched", t0); err != nil {
				t.Fatal(err)
			}
		} else if m, err := d.LoadMarks(); err != nil || m["play_webhook_unmatched"] != t0.Unix() {
			t.Fatalf("open %d: marks %v, err %v", i, m, err)
		}
		_ = d.Close()
	}
}
