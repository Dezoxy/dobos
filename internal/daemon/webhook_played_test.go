package daemon

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"flodni/internal/warmup"
)

// playedEnv opens one episode the way Open leaves it: a registry entry, a resolved
// target and a session. It returns the key the webhook should mark.
func playedEnv(t *testing.T) (path string, key warmup.FileKey) {
	t.Helper()
	oldStates := fileStates
	fileStates = newFileStateIndex(nil, 90)
	path = "/mnt/tv/Some Show/Some.Show.S01E02.1080p.mkv"
	key = warmup.FileKey{Hash: "abc123", FileIdx: 4}
	playbackRegistry.Store(path, &PlaybackState{Path: path, OpenedAt: time.Now()})
	tailFillTargets.Store(path, tailFillTarget{hash: key.Hash, fileID: key.FileIdx, size: 1 << 30})
	ttffRegister(path, 1<<30, key.Hash, key.FileIdx, false, false)
	t.Cleanup(func() {
		fileStates = oldStates
		playbackRegistry.Delete(path)
		tailFillTargets.Delete(path)
		sessions.Delete(path)
	})
	return path, key
}

func postWebhook(t *testing.T, payload string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/plex/webhook", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handlePlexWebhook(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
}

const episodePayload = `{"event":"%s","Metadata":{"title":"Pilot","grandparentTitle":"Some Show","librarySectionType":"show","parentIndex":1,"index":2,
 "Media":[{"Part":[{"file":"/data/Some.Show.S01E02.1080p.mkv"}]}]}}`

func evPayload(event string) string { return strings.Replace(episodePayload, "%s", event, 1) }

func TestWebhookMarksPlayedAndCountsOneStartPerTransition(t *testing.T) {
	_, key := playedEnv(t)
	starts := playbackStarts.Load()

	postWebhook(t, evPayload("media.play"))
	if !fileStates.IsPlayed(key) {
		t.Fatal("a confirmed play did not mark the file")
	}
	if got := playbackStarts.Load() - starts; got != 1 {
		t.Fatalf("starts +%d, want 1", got)
	}

	postWebhook(t, evPayload("media.resume")) // progress ping: no new start
	if got := playbackStarts.Load() - starts; got != 1 {
		t.Fatalf("a resume counted: starts +%d", got)
	}

	postWebhook(t, evPayload("media.stop"))
	postWebhook(t, evPayload("media.play"))
	if got := playbackStarts.Load() - starts; got != 2 {
		t.Fatalf("stop then play: starts +%d, want 2", got)
	}
}

func TestWebhookWithoutAUniqueFileCountsTheStartButMarksNothing(t *testing.T) {
	path, key := playedEnv(t)
	// A second episode whose name matches nothing the webhook reports exactly, and a
	// payload that names only the series: the registry match is a guess, no file is marked.
	starts := playbackStarts.Load()
	postWebhook(t, `{"event":"media.play","Metadata":{"grandparentTitle":"Some Show","librarySectionType":"show"}}`)
	if fileStates.EverPlayed(key) {
		t.Fatalf("marked %s from a series-only payload", path)
	}
	if got := playbackStarts.Load() - starts; got != 1 {
		t.Fatalf("starts +%d, want 1", got)
	}
}

func TestScanSizedReadsDoNotMarkPlayed(t *testing.T) {
	path, key := playedEnv(t)
	s, _ := sessions.Load(path)
	s.(*TTFFSession).recordRead(srcFetchBlock, 0, 4<<20, 0) // well over the 1 MiB probe line
	if fileStates.EverPlayed(key) {
		t.Fatal("a session that read over 1 MiB marked the file without a webhook")
	}
}

func TestColdReopenCountsOncePerEvictionAndClearsTheMark(t *testing.T) {
	path, key := playedEnv(t)
	fileStates.MarkPlayed(key) // played before it was evicted
	fileStates.OnEvicted([]warmup.Eviction{{Key: key}})
	markColdOpen(path, key, false) // Open found no ready head

	prev, notPrev := coldReopensPrevPlay.Load(), coldReopensNotPlayed.Load()
	postWebhook(t, evPayload("media.play"))
	if coldReopensPrevPlay.Load()-prev != 1 || coldReopensNotPlayed.Load() != notPrev {
		t.Fatalf("cold reopen not counted once under previously_played=true")
	}
	if fileStates.HasEvictedMark(key) {
		t.Fatal("the eviction mark survived the count")
	}
	postWebhook(t, evPayload("media.stop"))
	postWebhook(t, evPayload("media.play"))
	if coldReopensPrevPlay.Load()-prev != 1 {
		t.Fatal("the same eviction counted twice")
	}
}

func TestColdReopenOfAFileNeverPlayedCountsAsNotPreviouslyPlayed(t *testing.T) {
	path, key := playedEnv(t)
	fileStates.OnEvicted([]warmup.Eviction{{Key: key}})
	markColdOpen(path, key, false)
	prev, notPrev := coldReopensPrevPlay.Load(), coldReopensNotPlayed.Load()
	postWebhook(t, evPayload("media.play"))
	if coldReopensNotPlayed.Load()-notPrev != 1 || coldReopensPrevPlay.Load() != prev {
		t.Fatal("not counted under previously_played=false")
	}
}

func TestEvictedFileOpenedWarmIsNotCounted(t *testing.T) {
	path, key := playedEnv(t)
	fileStates.OnEvicted([]warmup.Eviction{{Key: key}})
	markColdOpen(path, key, true) // the head was ready at Open
	prev, notPrev := coldReopensPrevPlay.Load(), coldReopensNotPlayed.Load()
	postWebhook(t, evPayload("media.play"))
	if coldReopensPrevPlay.Load() != prev || coldReopensNotPlayed.Load() != notPrev {
		t.Fatal("a play that opened warm counted as a cold reopen")
	}
}

func TestPathWithoutAResolvedTargetIsSkipped(t *testing.T) {
	path, key := playedEnv(t)
	tailFillTargets.Delete(path)
	postWebhook(t, evPayload("media.play"))
	if fileStates.EverPlayed(key) {
		t.Fatal("marked a file with no resolved hash and index")
	}
}
