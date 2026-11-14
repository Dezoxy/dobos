package metadb

import (
	"bytes"
	"flodni/internal/flog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type bufLogger struct{ buf bytes.Buffer }

func (l *bufLogger) has(sub string) bool { return strings.Contains(l.buf.String(), sub) }

// The two migration lines operators grep for, and MigrationResult beside them.
func TestMigrationLinesAndResult(t *testing.T) {
	lg := &bufLogger{}
	db, err := New(filepath.Join(t.TempDir(), "s.db"), flog.NewText(&lg.buf))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if db.MigrationResult() != "" {
		t.Fatalf("result before the migration = %q", db.MigrationResult())
	}

	stateDir := t.TempDir()
	if err := db.MigrateFromJSON(stateDir); err != nil {
		t.Fatal(err)
	}
	if !lg.has("[StateDB] Migration already completed") || db.MigrationResult() != "already_completed" {
		t.Fatalf("log %q, result %q", lg.buf.String(), db.MigrationResult())
	}

	js := `{"version":2,"files":{"` + strings.Repeat("a", 40) + `:1":"5"},"dirs":{},"filename_index":{"/x/a.mkv":"` + strings.Repeat("a", 40) + `:1"}}`
	if err := os.WriteFile(filepath.Join(stateDir, "inode_map.json"), []byte(js), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := db.MigrateFromJSON(stateDir); err != nil {
		t.Fatal(err)
	}
	if !lg.has("[StateDB] Migration complete: 1 inodes, 0 caches, 0 episodes") || db.MigrationResult() != "migrated" {
		t.Fatalf("log %q, result %q", lg.buf.String(), db.MigrationResult())
	}
}
