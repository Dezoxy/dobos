package settings

import "sync/atomic"

// Startup events the daemon's startup report reads. Each is set where the matching log
// line is written, so the report and the line cannot disagree.
var (
	// SettingsMigrated is set when the Settings record moved between the BBolt and
	// JSON stores.
	SettingsMigrated atomic.Bool
	// SettingsQuarantined is set when an unparseable settings file was set aside.
	SettingsQuarantined atomic.Bool
	// TorrentsMigrated counts torrents carried over from the legacy torrserver.db.
	TorrentsMigrated atomic.Int64
)

// Log texts operators grep for; startup tests pin them.
const (
	settingsMigratedFmt = "Successfully migrated %s/%s to %s"
	settingsCorruptMsg  = "Settings file was corrupt, moved to"
	migrateTorrentMsg   = "Migrate torrent"
)
