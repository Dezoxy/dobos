package daemon

import (
	"path/filepath"
	"testing"
	"time"

	"flodni/internal/metadb"
	"flodni/internal/warmup"
)

func fk(h string, i int) warmup.FileKey { return warmup.FileKey{Hash: h, FileIdx: i} }

func testStore(t *testing.T, path string) *metadb.DB {
	t.Helper()
	db, err := metadb.New(path, nopLogger{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

type nopLogger struct{}

func (nopLogger) Printf(string, ...interface{}) {}

func TestPlayedProtectionExpiresAfterTheConfiguredDays(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	for _, tt := range []struct {
		name string
		days int
		age  time.Duration
		want bool
	}{
		{"inside 90 days", 90, 89 * 24 * time.Hour, true},
		{"after 90 days", 90, 91 * 24 * time.Hour, false},
		{"zero never expires", 0, 5000 * 24 * time.Hour, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			x := newFileStateIndex(nil, tt.days)
			x.now = func() time.Time { return now.Add(-tt.age) }
			x.MarkPlayed(fk("AA", 1))
			x.now = func() time.Time { return now }
			if got := x.IsPlayed(fk("aa", 1)); got != tt.want {
				t.Fatalf("IsPlayed = %v", got)
			}
			if !x.EverPlayed(fk("aa", 1)) {
				t.Fatal("an expired play must still count as played before")
			}
		})
	}
	x := newFileStateIndex(nil, 90)
	if x.IsPlayed(fk("zz", 0)) {
		t.Fatal("a file never seen is not played")
	}
}

func TestFileStateWorksInMemoryWithNoStore(t *testing.T) {
	x := newFileStateIndex(nil, 90)
	x.MarkPlayed(fk("aa", 1))
	x.OnEvicted([]warmup.Eviction{{Key: fk("aa", 1)}, {Key: fk("bad", -1)}})
	if !x.IsPlayed(fk("aa", 1)) || !x.HasEvictedMark(fk("aa", 1)) {
		t.Fatal("marks lost")
	}
	if !x.ClearEvicted(fk("aa", 1)) || x.ClearEvicted(fk("aa", 1)) {
		t.Fatal("the eviction mark must clear exactly once")
	}
	x.ForgetHash("AA")
	if x.EverPlayed(fk("aa", 1)) {
		t.Fatal("ForgetHash kept the play")
	}
}

func TestFileStateSurvivesAReloadThroughTheDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.db")
	db := testStore(t, path)
	x := newFileStateIndex(nil, 90)
	if err := x.attach(db); err != nil {
		t.Fatal(err)
	}
	x.MarkPlayed(fk("aa", 1))
	x.OnEvicted([]warmup.Eviction{{Key: fk("aa", 1)}, {Key: fk("bb", 0)}})
	x.ClearEvicted(fk("aa", 1))

	y := newFileStateIndex(nil, 90)
	if err := y.attach(db); err != nil {
		t.Fatal(err)
	}
	if !y.IsPlayed(fk("aa", 1)) || y.HasEvictedMark(fk("aa", 1)) {
		t.Fatal("aa/1: played must survive, cleared eviction must stay cleared")
	}
	if !y.HasEvictedMark(fk("bb", 0)) || y.EverPlayed(fk("bb", 0)) {
		t.Fatal("bb/0: evicted, never played")
	}
	y.ForgetHash("aa")
	z := newFileStateIndex(nil, 90)
	if err := z.attach(db); err != nil {
		t.Fatal(err)
	}
	if z.EverPlayed(fk("aa", 1)) {
		t.Fatal("ForgetHash did not delete the rows")
	}
}

func TestAttachKeepsWhatMemoryLearnedBeforeTheDatabaseOpened(t *testing.T) {
	db := testStore(t, filepath.Join(t.TempDir(), "s.db"))
	if err := db.MarkFilePlayed("aa", 1, time.Unix(1_700_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	x := newFileStateIndex(nil, 0)
	x.OnEvicted([]warmup.Eviction{{Key: fk("aa", 1)}})
	if err := x.attach(db); err != nil {
		t.Fatal(err)
	}
	if !x.IsPlayed(fk("aa", 1)) || !x.HasEvictedMark(fk("aa", 1)) {
		t.Fatal("merge lost a mark")
	}
}
