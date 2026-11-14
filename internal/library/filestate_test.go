package library

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"flodni/internal/apiauth"
)

// GET /api/library/file (ADR 3, step 4): a projection of the stub, the registry and
// the daemon's file state.

func TestFileStateSummary(t *testing.T) {
	for _, c := range []struct {
		name  string
		facts FileFacts
		want  string
	}{
		{"nothing recorded", FileFacts{}, StateAdded},
		{"queued for warming", FileFacts{Queued: true}, StateWarming},
		{"warmed and cached", FileFacts{Readability: "warmed", Cache: "cached"}, StateWarm},
		{"present and cached", FileFacts{Readability: "present", Cache: "cached"}, StateWarm},
		{"warmed, windows not kept", FileFacts{Readability: "warmed", Cache: "dropped_quota"}, StateAdded},
		{"failed", FileFacts{Readability: "failed"}, StateAdded},
		{"evicted by the cache state", FileFacts{Readability: "warmed", Cache: "evicted"}, StateEvicted},
		{"evicted by the mark", FileFacts{Evicted: true}, StateEvicted},
		{"played and warm", FileFacts{Readability: "warmed", Cache: "cached", Protected: true}, StatePlayed},
		{"played and evicted", FileFacts{Protected: true, Evicted: true}, StateEvicted},
		{"confirmed beats the rest", FileFacts{Protected: true, Evicted: true, Confirmed: true}, StateConfirmed},
	} {
		if got := summarize(c.facts); got != c.want {
			t.Errorf("%s: state = %q, want %q", c.name, got, c.want)
		}
	}
}

func getFile(t *testing.T, e *charEnv, query string) (int, FileStateResponse) {
	t.Helper()
	rec := httptest.NewRecorder()
	NewHandler(e.m).File(rec, httptest.NewRequest(http.MethodGet, "/api/library/file?"+query, nil))
	var out FileStateResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%v: %s", err, rec.Body)
		}
	}
	return rec.Code, out
}

func TestFileStateReportsEachFact(t *testing.T) {
	e := newCharEnv(t, FileStat{ID: 3, Path: "Show.S01E02.mkv", Length: 1234})
	resp, err := e.add(AddRequest{Type: "tv", Title: "Show", Hash: charPackHash, Season: 1, Episode: 2, QualityScore: 42})
	if err != nil {
		t.Fatal(err)
	}
	q := "hash=" + charPackHash + "&file_index=3"

	// Nothing recorded by the daemon: added, with the stub and the registry row.
	code, got := getFile(t, e, q)
	if code != http.StatusOK || got.State != StateAdded {
		t.Fatalf("status %d, %+v", code, got)
	}
	if got.Stub.Path != resp.Files[0].Path || got.Stub.FusePath != resp.Files[0].FusePath || got.Stub.Size != 1234 ||
		got.Stub.Type != "tv" || got.Stub.Season != 1 || got.Stub.Episode != 2 {
		t.Fatalf("stub = %+v", got.Stub)
	}
	if got.Registry == nil || got.Registry.QualityScore != 42 || got.Registry.Source != "api" ||
		got.Registry.EpisodeKey != EpisodeKey("Show", 1, 2) {
		t.Fatalf("registry = %+v", got.Registry)
	}
	if got.Warm.Readability != "unknown" || got.Warm.Cache != "unknown" || got.PlayedAt != nil ||
		got.Protected || got.Evicted || got.Confirmed {
		t.Fatalf("an untouched file reports facts: %+v", got)
	}

	// Each fact comes from the callback, apart from the others.
	played := time.Unix(1_800_000_000, 0)
	var facts FileFacts
	e.m.cfg.FileState = func(hash string, idx int) FileFacts {
		if hash != charPackHash || idx != 3 {
			t.Errorf("callback asked for %s/%d", hash, idx)
		}
		return facts
	}
	facts = FileFacts{Readability: "warmed", Cache: "cached"}
	if _, got = getFile(t, e, q); got.State != StateWarm || got.Warm.Readability != "warmed" || got.Warm.Cache != "cached" {
		t.Fatalf("warm: %+v", got)
	}
	facts = FileFacts{Queued: true, FailedAttempts: 2, Readability: "failed"}
	if _, got = getFile(t, e, q); got.State != StateWarming || !got.Warm.Queued || got.Warm.FailedAttempts != 2 {
		t.Fatalf("warming: %+v", got)
	}
	facts = FileFacts{Readability: "warmed", Cache: "evicted", Evicted: true, PlayedAt: played, Protected: true}
	if _, got = getFile(t, e, q); got.State != StateEvicted || !got.Evicted || !got.Protected ||
		got.PlayedAt == nil || *got.PlayedAt != "2027-01-15T08:00:00Z" || got.Warm.Readability != "warmed" {
		t.Fatalf("evicted and played: %+v", got)
	}
	facts = FileFacts{PlayedAt: played, Protected: true}
	if _, got = getFile(t, e, q); got.State != StatePlayed {
		t.Fatalf("played: %+v", got)
	}
	facts = FileFacts{Confirmed: true}
	if _, got = getFile(t, e, q); got.State != StateConfirmed || !got.Confirmed {
		t.Fatalf("confirmed: %+v", got)
	}
}

func TestFileStateOfAMovieHasNoRegistryRow(t *testing.T) {
	e := newCharEnv(t, movieTorrent...)
	if _, err := e.add(AddRequest{Title: "Inception", Hash: charPackHash, Year: 2010}); err != nil {
		t.Fatal(err)
	}
	code, got := getFile(t, e, "hash="+charPackHash+"&file_index=2")
	if code != http.StatusOK || got.Registry != nil || got.Stub.Type != "movie" {
		t.Fatalf("status %d, %+v", code, got)
	}
}

// A file nothing links to is a 404: never filed, removed, or replaced (the rows of a
// dropped torrent are deleted with it, so there is no history to report).
func TestFileStateOfAFileThatIsGoneIs404(t *testing.T) {
	e := newCharEnv(t, FileStat{ID: 1, Path: "Show.S01E01.mkv", Length: 10})
	e.m.cfg.FileState = func(string, int) FileFacts { return FileFacts{Protected: true, Readability: "warmed"} }
	if code, _ := getFile(t, e, "hash="+charPackHash+"&file_index=1"); code != http.StatusNotFound {
		t.Fatalf("never filed: status %d", code)
	}
	if _, err := e.add(AddRequest{Type: "tv", Title: "Show", Hash: charPackHash, Season: 1, Episode: 1}); err != nil {
		t.Fatal(err)
	}
	if code, _ := getFile(t, e, "hash="+charPackHash+"&file_index=1"); code != http.StatusOK {
		t.Fatalf("filed: status %d", code)
	}
	if code, _ := getFile(t, e, "hash="+charPackHash+"&file_index=2"); code != http.StatusNotFound {
		t.Fatalf("another index: status %d", code)
	}

	// Replaced by another release: the old one is gone.
	if _, err := e.add(AddRequest{Type: "tv", Title: "Show", Hash: charOtherHash, Season: 1, Episode: 1}); err != nil {
		t.Fatal(err)
	}
	if code, _ := getFile(t, e, "hash="+charPackHash+"&file_index=1"); code != http.StatusNotFound {
		t.Fatalf("replaced: status %d", code)
	}
	if code, _ := getFile(t, e, "hash="+charOtherHash+"&file_index=1"); code != http.StatusOK {
		t.Fatalf("replacement: status %d", code)
	}

	// Removed.
	if _, err := e.m.Remove(context.Background(), RemoveRequest{Hash: charOtherHash}); err != nil {
		t.Fatal(err)
	}
	if code, _ := getFile(t, e, "hash="+charOtherHash+"&file_index=1"); code != http.StatusNotFound {
		t.Fatalf("removed: status %d", code)
	}
}

func TestFileStateRefusesBadRequests(t *testing.T) {
	e := newCharEnv(t)
	for _, q := range []string{"", "hash=" + charPackHash, "file_index=1", "hash=xyz&file_index=1",
		"hash=" + charPackHash + "&file_index=abc", "hash=" + charPackHash + "&file_index=-1"} {
		if code, _ := getFile(t, e, q); code != http.StatusBadRequest {
			t.Errorf("%q: status %d, want 400", q, code)
		}
	}
	rec := httptest.NewRecorder()
	NewHandler(e.m).File(rec, httptest.NewRequest(http.MethodPost, "/api/library/file?hash="+charPackHash+"&file_index=1", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST: status %d, want 405", rec.Code)
	}
}

// The route sits behind the control port's bearer token like the others.
func TestFileStateNeedsTheTokenWhenOneIsSet(t *testing.T) {
	e := newCharEnv(t, FileStat{ID: 1, Path: "Show.S01E01.mkv", Length: 10})
	if _, err := e.add(AddRequest{Type: "tv", Title: "Show", Hash: charPackHash, Season: 1, Episode: 1}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/library/file", NewHandler(e.m).File)
	srv := apiauth.Middleware("secret", nil, mux)

	for _, c := range []struct {
		auth string
		want int
	}{{"", http.StatusUnauthorized}, {"Bearer wrong", http.StatusUnauthorized}, {"Bearer secret", http.StatusOK}} {
		req := httptest.NewRequest(http.MethodGet, "/api/library/file?hash="+charPackHash+"&file_index=1", nil)
		if c.auth != "" {
			req.Header.Set("Authorization", c.auth)
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("auth %q: status %d, want %d", c.auth, rec.Code, c.want)
		}
	}
}
