package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"flodni/internal/config"
	"flodni/internal/warmup"
)

const (
	pmStub      = "/mnt/flodni/tv/Show Name (2020)/Season.01/Show_Name_S01E02_1a2b3c4d.mkv"
	pmDecoyStub = "/mnt/flodni/tv/Show Name (2020)/Season.01/Show_Name_S01E03_1a2b3c4d.mkv"
	pmLibFile   = "/series/Show Name (2020)/Season 01/Show Name - S01E02 - Title WEBDL-720p.mkv"
)

var (
	pmKey      = warmup.FileKey{Hash: "1a2b3c4d00", FileIdx: 2}
	pmDecoyKey = warmup.FileKey{Hash: "1a2b3c4d00", FileIdx: 3}
)

// pathMapEnv registers the target stub and a decoy sibling the way Open leaves them, and
// sets the config the webhook reads. It returns the stub state.
func pathMapEnv(t *testing.T, pathMap map[string]string) *PlaybackState {
	t.Helper()
	prevCfg, oldStates := globalConfig.Load(), fileStates
	fileStates = newFileStateIndex(nil, 90)
	globalConfig.Store(&config.Config{PlexPathMap: pathMap})
	state := &PlaybackState{Path: pmStub, OpenedAt: time.Now()}
	playbackRegistry.Store(pmStub, state)
	playbackRegistry.Store(pmDecoyStub, &PlaybackState{Path: pmDecoyStub, OpenedAt: time.Now()})
	tailFillTargets.Store(pmStub, tailFillTarget{hash: pmKey.Hash, fileID: pmKey.FileIdx, size: 1 << 30})
	tailFillTargets.Store(pmDecoyStub, tailFillTarget{hash: pmDecoyKey.Hash, fileID: pmDecoyKey.FileIdx, size: 1 << 30})
	t.Cleanup(func() {
		globalConfig.Store(prevCfg)
		fileStates = oldStates
		for _, p := range []string{pmStub, pmDecoyStub} {
			playbackRegistry.Delete(p)
			tailFillTargets.Delete(p)
		}
	})
	return state
}

// pmLink makes a library file at root+file that links to target, creating its folders.
func pmLink(t *testing.T, root, file, target string) {
	t.Helper()
	link := filepath.Join(root, file)
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func pmPayload(event, file, imdb string) string {
	guid := ""
	if imdb != "" {
		guid = `,"Guid":[{"id":"imdb://` + imdb + `"}]`
	}
	return `{"event":"` + event + `","Metadata":{"title":"Zzz","grandparentTitle":"Unrelated Programme","librarySectionType":"show","parentIndex":1,"index":2` +
		guid + `,"Media":[{"Part":[{"file":"` + file + `"}]}]}}`
}

func ruleCounts() (c [numWebhookRules]int64) {
	for i := range c {
		c[i] = playbackWebhookRules[i].Load()
	}
	return c
}

// playRule posts a play webhook and returns which rule counter moved (-1 if none or several).
func playRule(t *testing.T, payload string) int {
	t.Helper()
	before := ruleCounts()
	postWebhook(t, payload)
	after, moved := ruleCounts(), -1
	for i := range after {
		if d := after[i] - before[i]; d == 1 && moved == -1 {
			moved = i
		} else if d != 0 {
			return -1
		}
	}
	return moved
}

func TestPathMapMatchesTheSymlinkTarget(t *testing.T) {
	root := t.TempDir()
	state := pathMapEnv(t, map[string]string{"/series/": root + "/series/"})
	pmLink(t, root, pmLibFile, pmStub)

	if got := playRule(t, pmPayload("media.play", pmLibFile, "")); got != rulePathMap {
		t.Fatalf("rule = %d, want path_map (%d)", got, rulePathMap)
	}
	if !state.GetStatus() {
		t.Fatal("the symlink's target was not confirmed")
	}
	if v, _ := playbackRegistry.Load(pmDecoyStub); v.(*PlaybackState).GetStatus() {
		t.Fatal("the sibling episode was confirmed")
	}
}

func TestPathMapFollowsARelativeLink(t *testing.T) {
	root := t.TempDir()
	state := pathMapEnv(t, map[string]string{"/series/": root + "/series/"})
	// Relative to the link's own folder: a name and a ../ detour that ends at the stub.
	pmLink(t, root, pmLibFile, "../../../mnt/flodni/tv/Show Name (2020)/Season.01/"+filepath.Base(pmStub))

	if got := playRule(t, pmPayload("media.play", pmLibFile, "")); got != rulePathMap {
		t.Fatalf("rule = %d, want path_map (%d)", got, rulePathMap)
	}
	if !state.GetStatus() {
		t.Fatal("the relative link's target was not confirmed")
	}
}

func TestPathMapLongestPrefixWins(t *testing.T) {
	root := t.TempDir()
	good, bad := filepath.Join(root, "good"), filepath.Join(root, "bad")
	pathMapEnv(t, map[string]string{"/series/": bad + "/", "/series/Show Name (2020)/": good + "/"})
	// Only the longer prefix's folder holds the link, and it points at the stub; the shorter
	// prefix's folder holds a link to the decoy.
	pmLink(t, good, "Season 01/Show Name - S01E02 - Title WEBDL-720p.mkv", pmStub)
	pmLink(t, bad, "Show Name (2020)/Season 01/Show Name - S01E02 - Title WEBDL-720p.mkv", pmDecoyStub)

	postWebhook(t, pmPayload("media.play", pmLibFile, ""))
	v, _ := playbackRegistry.Load(pmStub)
	d, _ := playbackRegistry.Load(pmDecoyStub)
	if !v.(*PlaybackState).GetStatus() || d.(*PlaybackState).GetStatus() {
		t.Fatal("the shorter prefix decided")
	}
}

func TestPathMapMissingOrRegularFileFallsThrough(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(root string)
	}{
		{"missing", func(string) {}},
		{"regular file", func(root string) {
			p := filepath.Join(root, pmLibFile)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			state := pathMapEnv(t, map[string]string{"/series/": root + "/series/"})
			tc.setup(filepath.Join(root, "series"))
			state.mu.Lock()
			state.ImdbID = "tt0000001"
			state.mu.Unlock()

			// The IMDb fallback still runs and resolves it.
			if got := playRule(t, pmPayload("media.play", pmLibFile, "tt0000001")); got != ruleIMDB {
				t.Fatalf("rule = %d, want imdb (%d)", got, ruleIMDB)
			}
			// With nothing to fall back to, no rule matches.
			state.mu.Lock()
			state.ImdbID = ""
			state.mu.Unlock()
			if got := playRule(t, pmPayload("media.play", pmLibFile, "")); got != ruleNone {
				t.Fatalf("rule = %d, want none (%d)", got, ruleNone)
			}
		})
	}
}

func TestPathMapOffChangesNothing(t *testing.T) {
	root := t.TempDir()
	state := pathMapEnv(t, nil)
	pmLink(t, root, pmLibFile, pmStub)
	if got := playRule(t, pmPayload("media.play", pmLibFile, "")); got != ruleNone {
		t.Fatalf("rule = %d, want none (%d)", got, ruleNone)
	}
	if state.GetStatus() {
		t.Fatal("matched with no plex_path_map")
	}
}

func TestWebhookRuleCounterLabels(t *testing.T) {
	root := t.TempDir()
	state := pathMapEnv(t, map[string]string{"/series/": root + "/series/"})
	if got := playRule(t, pmPayload("media.play", "/x/"+filepath.Base(pmStub), "")); got != ruleBasename {
		t.Fatalf("basename: rule = %d", got)
	}
	state.mu.Lock()
	state.ImdbID = "tt0000002"
	state.mu.Unlock()
	if got := playRule(t, pmPayload("media.play", "/x/other.mkv", "tt0000002")); got != ruleIMDB {
		t.Fatalf("imdb: rule = %d", got)
	}
	state.mu.Lock()
	state.ImdbID = ""
	state.mu.Unlock()
	fuzzy := `{"event":"media.play","Metadata":{"title":"Show Name","librarySectionType":"show"}}`
	if got := playRule(t, fuzzy); got != ruleFuzzy {
		t.Fatalf("fuzzy: rule = %d", got)
	}
	if got := playRule(t, pmPayload("media.play", "/x/other.mkv", "")); got != ruleNone {
		t.Fatalf("none: rule = %d", got)
	}
}

func TestPathMapPlayMarksTheLinkTarget(t *testing.T) {
	root := t.TempDir()
	pathMapEnv(t, map[string]string{"/series/": root + "/series/"})
	pmLink(t, root, pmLibFile, pmStub)

	postWebhook(t, pmPayload("media.play", pmLibFile, ""))
	if !fileStates.IsPlayed(pmKey) {
		t.Fatal("the symlink's target was not marked played")
	}
	if fileStates.EverPlayed(pmDecoyKey) {
		t.Fatal("the sibling episode was marked played")
	}
}

func TestPathMapStopReleasesTheLinkTarget(t *testing.T) {
	root := t.TempDir()
	state := pathMapEnv(t, map[string]string{"/series/": root + "/series/"})
	pmLink(t, root, pmLibFile, pmStub)
	postWebhook(t, pmPayload("media.play", pmLibFile, ""))
	if !state.GetStatus() {
		t.Fatal("the play did not reach the symlink's target")
	}
	postWebhook(t, pmPayload("media.stop", pmLibFile, ""))
	if state.GetStatus() {
		t.Fatal("the stop did not reach the symlink's target")
	}
}
