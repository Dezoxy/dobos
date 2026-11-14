package metadb

import (
	"bytes"
	"flodni/internal/flog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The migration lines are greppable by operators: the default text output
// keeps their wording.
func TestMigrationLinesKeepTheirText(t *testing.T) {
	var buf bytes.Buffer
	dir := t.TempDir()
	d, err := New(filepath.Join(dir, "state.db"), flog.New(&buf, "GoProxy", true))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })

	if err := d.MigrateFromJSON(dir); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "[StateDB] Migration already completed") {
		t.Fatalf("no 'already completed' line: %q", buf.String())
	}

	buf.Reset()
	if err := os.WriteFile(filepath.Join(dir, "tv_episode_registry.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := d.MigrateFromJSON(dir); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "[StateDB] Migration complete: 0 inodes, 0 caches, 0 episodes") {
		t.Fatalf("no 'complete' line: %q", buf.String())
	}
}
