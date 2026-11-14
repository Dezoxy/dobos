package config

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"
)

// media_server_type survives only so an old config.json parses: Plex is the only media
// server, and any other value logs one warning at load.

func TestMediaServerTypeWarnsOnceForJellyfinAndNotForPlexOrEmpty(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	const want = "Jellyfin is no longer supported and Flodni uses Plex"
	for body, warns := range map[string]int{
		`{"media_server_type": "jellyfin"}`: 1,
		`{"media_server_type": "plex"}`:     0,
		`{}`:                                0,
	} {
		buf.Reset()
		loadWith(t, body)
		if got := strings.Count(buf.String(), want); got != warns {
			t.Errorf("%s: %d warnings, want %d\n%s", body, got, warns, buf.String())
		}
	}
}
