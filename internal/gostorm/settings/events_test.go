package settings

import (
	"fmt"
	"testing"
)

// The settings lines operators grep for. The first two are logged as a prefix.
func TestPinnedStartupTexts(t *testing.T) {
	if got, want := fmt.Sprintf(settingsMigratedFmt, "Settings", "BitTorr", "JSON"), "Successfully migrated Settings/BitTorr to JSON"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if settingsCorruptMsg != "Settings file was corrupt, moved to" {
		t.Errorf("corrupt text = %q", settingsCorruptMsg)
	}
	if migrateTorrentMsg != "Migrate torrent" {
		t.Errorf("migrate torrent text = %q", migrateTorrentMsg)
	}
}
