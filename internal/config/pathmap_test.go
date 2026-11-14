package config

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"strings"
	"testing"
)

// plex_path_map maps media server library prefixes to paths inside the container. Empty
// is off; an entry that is not absolute on both sides is dropped with one warning.

func TestPlexPathMapDefaultsToOff(t *testing.T) {
	if got := LoadConfig().PlexPathMap; len(got) != 0 {
		t.Fatalf("plex_path_map = %v, want empty", got)
	}
}

func TestPlexPathMapKeepsAbsoluteEntriesAndWarnsOncePerDroppedOne(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	got := loadWith(t, `{"plex_path_map": {
		"/movies/": "/media/movies/",
		"/series/": "/media/series/",
		"series/": "/media/series/",
		"/anime/": "media/anime/",
		"": "/media/x/"
	}}`).PlexPathMap
	if len(got) != 2 || got["/movies/"] != "/media/movies/" || got["/series/"] != "/media/series/" {
		t.Fatalf("plex_path_map = %v, want the two absolute entries", got)
	}
	if n := strings.Count(buf.String(), "plex_path_map entry"); n != 3 {
		t.Fatalf("%d warnings, want 3 (one per dropped entry):\n%s", n, buf.String())
	}
}

func TestConfigExampleCarriesPlexPathMap(t *testing.T) {
	raw, err := os.ReadFile("../../config.json.example")
	if err != nil {
		t.Fatal(err)
	}
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"plex_path_map"`) || len(c.PlexPathMap) != 0 {
		t.Fatal(`config.json.example must carry an empty "plex_path_map"`)
	}
}
