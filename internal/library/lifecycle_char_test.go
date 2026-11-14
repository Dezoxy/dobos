package library

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"flodni/internal/metadb"
)

// Characterization of the file lifecycle as v0.1.0 shipped it (ADR 3, step 1). These
// tests change no behaviour and must stay green through every later step: a request
// that carries none of the new fields gets today's answer, byte for byte.

func postAdd(t *testing.T, e *charEnv, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	NewHandler(e.m).Add(rec, httptest.NewRequest(http.MethodPost, "/api/library/add", strings.NewReader(body)))
	return rec
}

// The v0.1.0 body of an add: these five keys, in this order, nothing else.
func TestAddResponseKeepsTheV010Shape(t *testing.T) {
	e := newCharEnv(t, movieTorrent...)
	rec := postAdd(t, e, `{"title":"Inception","hash":"`+charPackHash+`","year":2010,"metadata_wait":1}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body %s", rec.Code, rec.Body)
	}
	name := "Inception_2010_1080p_55555555.mkv"
	want := `{"hash":"` + charPackHash + `","title":"Inception","type":"movie","files":[{"path":"` +
		jsonPath(filepath.Join(e.movies, name)) + `","fuse_path":"movies/` + name +
		`","size":8589934592,"file_index":2}],"already_present":false}` + "\n"
	if rec.Body.String() != want {
		t.Fatalf("body\n got %s\nwant %s", rec.Body, want)
	}

	// The same release again: 200 and already_present, still the same keys.
	rec = postAdd(t, e, `{"title":"Inception","hash":"`+charPackHash+`","year":2010,"metadata_wait":1}`)
	want = strings.Replace(want, `"already_present":false`, `"already_present":true`, 1)
	if rec.Code != http.StatusOK || rec.Body.String() != want {
		t.Fatalf("re-add: status %d\n got %s\nwant %s", rec.Code, rec.Body, want)
	}
}

func TestEpisodeAddResponseKeepsTheV010Shape(t *testing.T) {
	e := newCharEnv(t,
		FileStat{ID: 1, Path: "p/Show.S01E01.mkv", Length: 10},
		FileStat{ID: 2, Path: "p/Show.S01E02.mkv", Length: 20})
	rec := postAdd(t, e, `{"type":"tv","title":"Show","hash":"`+charPackHash+`","season":1,"quality_score":5,"metadata_wait":1}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d; body %s", rec.Code, rec.Body)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	for k := range got {
		keys[k] = true
	}
	for _, k := range []string{"hash", "title", "type", "files", "already_present"} {
		if !keys[k] {
			t.Errorf("key %q missing", k)
		}
		delete(keys, k)
	}
	if len(keys) != 0 {
		t.Fatalf("keys beyond v0.1.0: %v", keys)
	}
	var files []map[string]interface{}
	if err := json.Unmarshal(got["files"], &files); err != nil || len(files) != 2 {
		t.Fatalf("files = %s, %v", got["files"], err)
	}
	for _, f := range files {
		for _, k := range []string{"path", "fuse_path", "size", "file_index", "season", "episode"} {
			if _, ok := f[k]; !ok {
				t.Errorf("file key %q missing in %v", k, f)
			}
		}
		if len(f) != 6 {
			t.Errorf("file entry has keys beyond v0.1.0: %v", f)
		}
	}
}

func jsonPath(p string) string {
	b, _ := json.Marshal(p)
	return strings.Trim(string(b), `"`)
}

// The registry row an add writes carries the caller's quality_score and source api; a
// lower score does not stop a replacement, because today the score is stored and never
// compared.
func TestReplacementIgnoresTheQualityScoreToday(t *testing.T) {
	e := newCharEnv(t, FileStat{ID: 1, Path: "Show.S01E02.mkv", Length: 1 << 20})
	old := scatter(t, e)
	key := EpisodeKey("Show", 1, 2)
	row := e.reg.rows[key]
	row.QualityScore = 90
	e.reg.rows[key] = row

	resp, err := e.add(AddRequest{Type: "tv", Title: "Show", Hash: charThirdHash, Season: 1, Episode: 2, QualityScore: 10})
	if err != nil || len(resp.Files) != 1 {
		t.Fatalf("add: %+v, %v", resp, err)
	}
	if exists(old[1]) {
		t.Fatal("the better-scored stub survived: the score is compared now")
	}
	got := e.reg.rows[key]
	if got.Hash != charThirdHash || got.QualityScore != 10 || got.Source != "api" {
		t.Fatalf("row = %+v", got)
	}
}

// A movie never replaces anything: a second release is a second stub.
func TestMovieSecondReleaseIsASecondStub(t *testing.T) {
	e := newCharEnv(t, movieTorrent...)
	if _, err := e.add(AddRequest{Title: "Inception", Hash: charPackHash, Year: 2010}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.add(AddRequest{Title: "Inception", Hash: charOtherHash, Year: 2010}); err != nil {
		t.Fatal(err)
	}
	if got := mkvs(t, e.movies); len(got) != 2 {
		t.Fatalf("movies dir = %v, want two stubs", got)
	}
	if len(e.eng.removed) != 0 || e.eng.reads != 0 {
		t.Fatalf("removed = %v, reads = %d: a movie add touched an existing release", e.eng.removed, e.eng.reads)
	}
}

// One add: OnAdded once, the registry row, the media-server scan scheduled for the
// section. A repeat does none of it.
func TestAddCallsOnAddedOnceAndWritesTheRegistryRow(t *testing.T) {
	e := newCharEnv(t, FileStat{ID: 7, Path: "Show.S01E01.mkv", Length: 10})
	calls := hookOnAdded(e)
	if _, err := e.add(AddRequest{Type: "tv", Title: "Show", Hash: charPackHash, Season: 1, Episode: 1, QualityScore: 3}); err != nil {
		t.Fatal(err)
	}
	row, ok := e.reg.rows[EpisodeKey("Show", 1, 1)]
	if !ok || row.Hash != charPackHash || row.QualityScore != 3 {
		t.Fatalf("row = %+v, %v", row, ok)
	}
	if len(*calls) != 1 || (*calls)[0].files[0].FileIndex != 7 {
		t.Fatalf("OnAdded calls = %+v", *calls)
	}
	if _, err := e.add(AddRequest{Type: "tv", Title: "Show", Hash: charPackHash, Season: 1, Episode: 1}); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("OnAdded called again by a repeat: %d", len(*calls))
	}
}

// Remove by path (what the unlink through the mount does) and remove by hash end in
// the same state: stub gone, registry row gone, torrent dropped once with its last stub.
func TestRemoveByPathAndByHashEndInTheSameState(t *testing.T) {
	end := func(t *testing.T, byHash bool) (map[string]metadb.EpisodeEntry, []string, bool) {
		e := newCharEnv(t, FileStat{ID: 1, Path: "Show.S01E01.mkv", Length: 10})
		resp, err := e.add(AddRequest{Type: "tv", Title: "Show", Hash: charPackHash, Season: 1, Episode: 1})
		if err != nil {
			t.Fatal(err)
		}
		req := RemoveRequest{Path: resp.Files[0].Path}
		if byHash {
			req = RemoveRequest{Hash: charPackHash}
		}
		if _, err := e.m.Remove(t.Context(), req); err != nil {
			t.Fatal(err)
		}
		return e.reg.rows, e.eng.removed, exists(resp.Files[0].Path)
	}
	rowsA, removedA, stubA := end(t, false)
	rowsB, removedB, stubB := end(t, true)
	if len(rowsA) != 0 || len(rowsB) != 0 || stubA || stubB {
		t.Fatalf("rows %v / %v, stub %v / %v", rowsA, rowsB, stubA, stubB)
	}
	if len(removedA) != 1 || len(removedB) != 1 || removedA[0] != removedB[0] {
		t.Fatalf("dropped torrents %v / %v", removedA, removedB)
	}
}
