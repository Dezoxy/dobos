package library

import (
	"net/http"
	"reflect"
	"testing"
)

// OnAdded is how warm on add hears about a title: once per successful add, with the
// engine's file ids, and never for a refusal or a title that was already filed.

type addedCall struct {
	hash  string
	files []AddedFile
}

func hookOnAdded(e *charEnv) *[]addedCall {
	var calls []addedCall
	e.m.cfg.OnAdded = func(hash string, files []AddedFile) {
		calls = append(calls, addedCall{hash, files})
	}
	return &calls
}

func TestOnAddedFiresOnceAfterASuccessfulAdd(t *testing.T) {
	e := newCharEnv(t, movieTorrent...)
	calls := hookOnAdded(e)
	resp, err := e.add(AddRequest{Title: "Inception", Hash: charPackHash, Year: 2010})
	if err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || (*calls)[0].hash != charPackHash || !reflect.DeepEqual((*calls)[0].files, resp.Files) {
		t.Fatalf("calls = %+v, want one with the response's files %+v", *calls, resp.Files)
	}
	if got := (*calls)[0].files[0].FileIndex; got != 2 {
		t.Fatalf("file index = %d, want the engine id 2", got)
	}
}

func TestOnAddedIsNotCalledForAnAlreadyPresentTitle(t *testing.T) {
	e := newCharEnv(t, movieTorrent...)
	if _, err := e.add(AddRequest{Title: "Inception", Hash: charPackHash, Year: 2010}); err != nil {
		t.Fatal(err)
	}
	calls := hookOnAdded(e)
	second, err := e.add(AddRequest{Title: "Inception", Hash: charPackHash, Year: 2010})
	if err != nil || !second.AlreadyPresent {
		t.Fatalf("second add: %+v, %v", second, err)
	}
	if len(*calls) != 0 {
		t.Fatalf("OnAdded called for an already present title: %+v", *calls)
	}
}

func TestOnAddedIsNotCalledForAFailedAdd(t *testing.T) {
	e := newCharEnv(t, FileStat{ID: 1, Path: "readme.txt", Length: 5})
	calls := hookOnAdded(e)
	_, err := e.add(AddRequest{Title: "Nothing", Hash: charPackHash})
	if statusOf(t, err) != http.StatusUnprocessableEntity {
		t.Fatalf("err = %v, want 422", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("OnAdded called for a refused add: %+v", *calls)
	}
}

func TestOnAddedCarriesEveryEpisodeOfAPackWithItsFileIndex(t *testing.T) {
	e := newCharEnv(t,
		FileStat{ID: 3, Path: "Show.S01E01.1080p.mkv", Length: 100},
		FileStat{ID: 5, Path: "Show.S01E02.1080p.mkv", Length: 200})
	calls := hookOnAdded(e)
	resp, err := e.add(AddRequest{Type: "tv", Title: "Show", Hash: charPackHash, Season: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || len((*calls)[0].files) != len(resp.Files) || len(resp.Files) != 2 {
		t.Fatalf("calls = %+v, resp = %+v", *calls, resp.Files)
	}
	idx := map[int]int64{}
	for _, f := range (*calls)[0].files {
		idx[f.FileIndex] = f.Size
	}
	if !reflect.DeepEqual(idx, map[int]int64{3: 100, 5: 200}) {
		t.Fatalf("files = %v, want engine ids with sizes", idx)
	}
}
