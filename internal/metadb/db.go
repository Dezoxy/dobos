package metadb

import (
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// DB wraps a SQLite database connection for flodni state persistence.
type DB struct {
	db     *sql.DB
	path   string
	logger *slog.Logger
}

// New opens or creates a SQLite database at dbPath, applies pragmas,
// creates tables, and returns a ready-to-use DB instance.
func New(dbPath string, logger *slog.Logger) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return nil, fmt.Errorf("metadb: create dir: %w", err)
	}

	d, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("metadb: open: %w", err)
	}

	d.SetMaxOpenConns(1)

	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	db := &DB{
		db:     d,
		path:   dbPath,
		logger: logger,
	}

	if err := db.applyPragmas(); err != nil {
		_ = d.Close() // the pragma error is the one reported
		return nil, fmt.Errorf("metadb: pragmas: %w", err)
	}

	if err := db.ExecSchema(); err != nil {
		_ = d.Close() // the schema error is the one reported
		return nil, fmt.Errorf("metadb: schema: %w", err)
	}

	return db, nil
}

// Close performs a WAL checkpoint and closes the database connection.
func (d *DB) Close() error {
	_, _ = d.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	return d.db.Close()
}

// hasColumn reports whether a table already carries a column.
func (d *DB) hasColumn(table, column string) bool {
	var n int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, column).Scan(&n); err != nil {
		return false
	}
	return n > 0
}

// addColumn applies an additive migration guarded by hasColumn. A lookup that fails for
// its own reasons answers "missing", so the ALTER is attempted on a table that already
// has the column: that duplicate is tolerated here, because the alternative is New()
// returning an error and flodni refusing to start.
func (d *DB) addColumn(table, column, ddl string) error {
	if d.hasColumn(table, column) {
		return nil
	}
	if _, err := d.db.Exec(ddl); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "duplicate column name") {
			return nil
		}
		return err
	}
	return nil
}

// SetLogger sets a custom logger for the database.
func (d *DB) SetLogger(l *slog.Logger) {
	if l != nil {
		d.logger = l
	}
}

// SQL returns the underlying *sql.DB for direct access (e.g. transactions).
func (d *DB) SQL() *sql.DB {
	return d.db
}

func (d *DB) applyPragmas() error {
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
	}
	for _, p := range pragmas {
		if _, err := d.db.Exec(p); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
	}
	return nil
}

// ExecSchema creates all tables if they do not exist. Idempotent.
func (d *DB) ExecSchema() error {
	schema := `
CREATE TABLE IF NOT EXISTS schema_version (
    version INTEGER PRIMARY KEY,
    applied_at TEXT DEFAULT (datetime('now')),
    description TEXT
);

CREATE TABLE IF NOT EXISTS inodes (
    type        TEXT NOT NULL CHECK(type IN ('file','dir')),
    infohash    TEXT,
    file_idx    INTEGER,
    full_path   TEXT UNIQUE,
    rel_path    TEXT UNIQUE,
    basename    TEXT,
    inode_value INTEGER NOT NULL,
    PRIMARY KEY (type, full_path)
);
CREATE INDEX IF NOT EXISTS idx_inodes_infohash ON inodes(infohash, file_idx);
CREATE INDEX IF NOT EXISTS idx_inodes_basename ON inodes(basename);

CREATE TABLE IF NOT EXISTS sync_caches (
    hash        TEXT NOT NULL,
    cache_type  TEXT NOT NULL CHECK(cache_type IN ('negative','fullpack')),
    title       TEXT,
    timestamp   TEXT NOT NULL,
    PRIMARY KEY (hash, cache_type)
);
CREATE INDEX IF NOT EXISTS idx_sync_type ON sync_caches(cache_type);

CREATE TABLE IF NOT EXISTS tv_episodes (
    episode_key   TEXT PRIMARY KEY,
    quality_score INTEGER NOT NULL,
    hash          TEXT NOT NULL,
    file_path     TEXT NOT NULL,
    source        TEXT NOT NULL,
    created       INTEGER NOT NULL,
    updated_at    TEXT DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_tv_hash ON tv_episodes(hash);

CREATE TABLE IF NOT EXISTS playback_states (
    path          TEXT PRIMARY KEY,
    hash          TEXT,
    imdb_id       TEXT,
    opened_at     TEXT,
    confirmed_at  TEXT,
    is_healthy    INTEGER DEFAULT 0,
    is_stopped    INTEGER DEFAULT 0,
    last_read_at  TEXT,
    read_count    INTEGER DEFAULT 0,
    last_seek_off INTEGER DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_playback_hash ON playback_states(hash);
CREATE INDEX IF NOT EXISTS idx_playback_healthy ON playback_states(is_healthy);

CREATE TABLE IF NOT EXISTS v304_bans (
    ip        TEXT PRIMARY KEY,
    banned_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS metadata_failures (
    hash       TEXT PRIMARY KEY,
    fail_count INTEGER NOT NULL DEFAULT 0,
    first_fail INTEGER NOT NULL,
    last_fail  INTEGER NOT NULL
);
`
	_, err := d.db.Exec(schema)
	if err != nil {
		return err
	}

	// V750: Register schema versions
	_, _ = d.db.Exec(`INSERT OR IGNORE INTO schema_version (version, description) VALUES (1, 'initial schema')`)
	_, _ = d.db.Exec(`INSERT OR IGNORE INTO schema_version (version, description) VALUES (2, 'add playback_states table')`)
	_, _ = d.db.Exec(`INSERT OR IGNORE INTO schema_version (version, description) VALUES (3, 'add v304_bans table')`)
	// 4 is taken on installations that ran the V754 webhook-position build, so this
	// lands on 5: INSERT OR IGNORE would otherwise drop it there and leave the
	// registry claiming two different things for the same version.
	_, _ = d.db.Exec(`INSERT OR IGNORE INTO schema_version (version, description) VALUES (5, 'add metadata_failures table')`)

	// Additive column: SQLite has no ADD COLUMN IF NOT EXISTS, so it is guarded by a
	// lookup instead. Without the show id an episode file cannot be traced back to
	// TMDB, which is what a re-search needs.
	if err := d.addColumn("tv_episodes", "show_imdb",
		`ALTER TABLE tv_episodes ADD COLUMN show_imdb TEXT DEFAULT ''`); err != nil {
		return err
	}
	_, _ = d.db.Exec(`INSERT OR IGNORE INTO schema_version (version, description) VALUES (6, 'add tv_episodes.show_imdb')`)

	// Episodes the reaper removed. Discovery returns a fraction of the library, so a
	// season it does not reach would keep the hole forever: this is what a later run
	// consults to try again.
	if _, err := d.db.Exec(`
		CREATE TABLE IF NOT EXISTS episode_gaps (
		    episode_key TEXT PRIMARY KEY,
		    show_imdb   TEXT DEFAULT '',
		    season      INTEGER NOT NULL,
		    file_path   TEXT NOT NULL,
		    dead_hash   TEXT NOT NULL,
		    removed_at  INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_gaps_season ON episode_gaps(show_imdb, season);`); err != nil {
		return err
	}
	_, _ = d.db.Exec(`INSERT OR IGNORE INTO schema_version (version, description) VALUES (7, 'add episode_gaps table')`)

	// last_attempt sends a gap to the back of the queue once it has been tried. Without
	// it a show whose name cannot be resolved is retried first on every run, and the
	// rest of the backlog is never reached.
	if err := d.addColumn("episode_gaps", "last_attempt",
		`ALTER TABLE episode_gaps ADD COLUMN last_attempt INTEGER DEFAULT 0`); err != nil {
		return err
	}
	_, _ = d.db.Exec(`INSERT OR IGNORE INTO schema_version (version, description) VALUES (8, 'add episode_gaps.last_attempt')`)

	// Schema 11, additive. Nothing reads or writes these two columns any more;
	// they stay so existing databases and new ones keep the same shape.
	if err := d.addColumn("playback_states", "external_id",
		`ALTER TABLE playback_states ADD COLUMN external_id TEXT DEFAULT ''`); err != nil {
		return err
	}
	if err := d.addColumn("playback_states", "external_id_ns",
		`ALTER TABLE playback_states ADD COLUMN external_id_ns TEXT DEFAULT ''`); err != nil {
		return err
	}
	_, _ = d.db.Exec(`INSERT OR IGNORE INTO schema_version (version, description) VALUES (11, 'add playback_states external identity')`)

	// Schema 12, additive: the readability record. The time of the last read the
	// swarm served, and the reason and source of the last failure. The CREATE TABLE
	// above stays as it was; a database from an older build gains the columns here.
	for _, col := range []struct{ name, ddl string }{
		{"last_swarm_read", `ALTER TABLE metadata_failures ADD COLUMN last_swarm_read INTEGER NOT NULL DEFAULT 0`},
		{"last_fail_reason", `ALTER TABLE metadata_failures ADD COLUMN last_fail_reason TEXT NOT NULL DEFAULT ''`},
		{"last_fail_source", `ALTER TABLE metadata_failures ADD COLUMN last_fail_source TEXT NOT NULL DEFAULT ''`},
	} {
		if err := d.addColumn("metadata_failures", col.name, col.ddl); err != nil {
			return err
		}
	}
	_, _ = d.db.Exec(`INSERT OR IGNORE INTO schema_version (version, description) VALUES (12, 'add metadata_failures readability columns')`)

	// Schema 13, additive: what the warmup cache remembers per media file. The time of
	// the last play a media server webhook confirmed, and the time quota eviction last
	// removed the file's windows (0 = no pending eviction mark). Rows appear on the
	// first play or eviction.
	if _, err := d.db.Exec(`
		CREATE TABLE IF NOT EXISTS file_state (
		    hash           TEXT NOT NULL,
		    file_idx       INTEGER NOT NULL,
		    last_played_at INTEGER NOT NULL DEFAULT 0,
		    evicted_at     INTEGER NOT NULL DEFAULT 0,
		    PRIMARY KEY (hash, file_idx)
		)`); err != nil {
		return err
	}
	_, _ = d.db.Exec(`INSERT OR IGNORE INTO schema_version (version, description) VALUES (13, 'add file_state table')`)

	// Schema 14, additive: the per-file warm record, beside the marks of schema 13.
	// readability and cache_state are empty until a record exists (served as unknown);
	// the times are Unix seconds, 0 when unset. warm_updated_at is internal: the prune
	// guard reads it so a row recorded while the caller built its set survives.
	for _, col := range []struct{ name, ddl string }{
		{"readability", `ALTER TABLE file_state ADD COLUMN readability TEXT NOT NULL DEFAULT ''`},
		{"cache_state", `ALTER TABLE file_state ADD COLUMN cache_state TEXT NOT NULL DEFAULT ''`},
		{"failed_attempts", `ALTER TABLE file_state ADD COLUMN failed_attempts INTEGER NOT NULL DEFAULT 0`},
		{"first_failed_at", `ALTER TABLE file_state ADD COLUMN first_failed_at INTEGER NOT NULL DEFAULT 0`},
		{"last_attempt_at", `ALTER TABLE file_state ADD COLUMN last_attempt_at INTEGER NOT NULL DEFAULT 0`},
		{"last_swarm_delivery_at", `ALTER TABLE file_state ADD COLUMN last_swarm_delivery_at INTEGER NOT NULL DEFAULT 0`},
		{"warm_updated_at", `ALTER TABLE file_state ADD COLUMN warm_updated_at INTEGER NOT NULL DEFAULT 0`},
	} {
		if err := d.addColumn("file_state", col.name, col.ddl); err != nil {
			return err
		}
	}
	_, _ = d.db.Exec(`INSERT OR IGNORE INTO schema_version (version, description) VALUES (14, 'add file_state warm columns')`)

	// Schema 15, additive: daemon-level times, one row per name, Unix seconds. Today:
	// the last play webhook that matched a file, the last that did not, and the last
	// unconfirmed reader session (internal/daemon/webhook_status.go).
	if _, err := d.db.Exec(`
		CREATE TABLE IF NOT EXISTS daemon_marks (
		    name TEXT PRIMARY KEY,
		    at   INTEGER NOT NULL
		)`); err != nil {
		return err
	}
	_, _ = d.db.Exec(`INSERT OR IGNORE INTO schema_version (version, description) VALUES (15, 'add daemon_marks table')`)

	// Schema 16, additive: the time warm on add queued the file, Unix seconds, 0 when
	// the file is not waiting for an attempt. Cleared when an attempt records an outcome;
	// a row older than an hour is a file warm on add never finished (warm_catchup_scope).
	if err := d.addColumn("file_state", "queued_at",
		`ALTER TABLE file_state ADD COLUMN queued_at INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	_, _ = d.db.Exec(`INSERT OR IGNORE INTO schema_version (version, description) VALUES (16, 'add file_state.queued_at')`)

	return nil
}
