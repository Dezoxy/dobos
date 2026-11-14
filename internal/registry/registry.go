package registry

import (
	"encoding/json"
	"flodni/internal/flog"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"flodni/internal/metadb"
)

var logger = flog.New(os.Stdout, "Registry", true)

// EpisodeEntry matches the Python registry format.
type EpisodeEntry struct {
	QualityScore int    `json:"quality_score"`
	Hash         string `json:"hash"`
	FilePath     string `json:"file_path"`
	Source       string `json:"source"`
	Created      int64  `json:"created"`
}

var registryMutex sync.Mutex
var registryDB *metadb.DB

// SetRegistryDB enables SQLite persistence for the episode registry.
func SetRegistryDB(db *metadb.DB) {
	registryMutex.Lock()
	defer registryMutex.Unlock()
	registryDB = db
}

var stateDir string

// SetStateDir sets the state directory path for this package.
func SetStateDir(dir string) {
	stateDir = dir
}

// GetRegistryPath returns the path to tv_episode_registry.json.
func GetRegistryPath() string {
	return filepath.Join(stateDir, "tv_episode_registry.json")
}

// StartRegistryWatchdog runs the self-healing check at startup and then every 24 hours.
func StartRegistryWatchdog(stopChan chan struct{}) {
	SyncRegistryWithDisk()

	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()

	logger.Info("[Registry] Watchdog active (interval: 24h)")

	for {
		select {
		case <-ticker.C:
			SyncRegistryWithDisk()
		case <-stopChan:
			logger.Info("[Registry] Watchdog stopping")
			return
		}
	}
}

// SyncRegistryWithDisk cleans up orphaned entries.
// With StateDB: deletes entries whose file_path no longer exists via SQL.
// Without StateDB: falls back to JSON read-filter-write cycle.
func SyncRegistryWithDisk() {
	registryMutex.Lock()
	defer registryMutex.Unlock()

	if registryDB != nil {
		syncRegistryWithDB()
		return
	}
	syncRegistryWithJSON()
}

func syncRegistryWithDB() {
	path := GetRegistryPath()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return
	}

	logger.Info(fmt.Sprintf("[Registry] Starting self-healing check (StateDB): %s", path), "path", path)

	entries, err := registryDB.AllEpisodes()
	if err != nil {
		logger.Error(fmt.Sprintf("[Registry] ERROR: could not read episodes from DB: %v", err), "err", err)
		return
	}

	removed := 0
	for _, entry := range entries {
		if _, err := os.Stat(entry.FilePath); err != nil {
			if err := registryDB.DeleteEpisode(entry.EpisodeKey); err != nil {
				logger.Error(fmt.Sprintf("[Registry] ERROR: failed to delete episode %s: %v", entry.EpisodeKey, err), "episode_key", entry.EpisodeKey, "err", err)
			} else {
				removed++
			}
		}
	}

	if removed == 0 {
		logger.Info(fmt.Sprintf("[Registry] Audit complete: 0 ghost entries found (Total: %d)", len(entries)))
	} else {
		logger.Info(fmt.Sprintf("[Registry] Self-Healing (StateDB): Purged %d ghost entries. Remaining: %d", removed, len(entries)-removed), "removed", removed)
	}
}

func syncRegistryWithJSON() {
	path := GetRegistryPath()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return
	}

	logger.Info(fmt.Sprintf("[Registry] Starting self-healing check: %s", path), "path", path)

	data, err := os.ReadFile(path)
	if err != nil {
		logger.Error(fmt.Sprintf("[Registry] ERROR: could not read registry: %v", err), "err", err)
		return
	}

	var registry map[string]EpisodeEntry
	if err := unmarshalJSON(data, &registry); err != nil {
		logger.Error(fmt.Sprintf("[Registry] ERROR: could not parse registry: %v", err), "err", err)
		return
	}

	initialCount := len(registry)
	newRegistry := make(map[string]EpisodeEntry)
	removed := 0

	for key, entry := range registry {
		if _, err := os.Stat(entry.FilePath); err == nil {
			newRegistry[key] = entry
		} else {
			removed++
		}
	}

	if removed == 0 {
		logger.Info(fmt.Sprintf("[Registry] Audit complete: 0 ghost entries found (Total: %d)", initialCount), "initial_count", initialCount)
		return
	}

	if err := saveRegistryLocked(path, newRegistry); err != nil {
		logger.Error(fmt.Sprintf("[Registry] ERROR: could not save cleaned registry: %v", err), "err", err)
	} else {
		logger.Info(fmt.Sprintf("[Registry] Self-Healing: Purged %d ghost entries. Remaining: %d", removed, len(newRegistry)), "removed", removed)
	}
}

// RemoveFromRegistry removes a specific file path from the registry.
func RemoveFromRegistry(filePath string) {
	registryMutex.Lock()
	defer registryMutex.Unlock()

	if registryDB != nil {
		removeFromRegistryDB(filePath)
		return
	}
	removeFromRegistryJSON(filePath)
}

func removeFromRegistryDB(filePath string) {
	entries, err := registryDB.EpisodesByFilePath(filePath)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if err := registryDB.DeleteEpisode(entry.EpisodeKey); err != nil {
			logger.Error(fmt.Sprintf("[Registry] ERROR: failed to delete episode %s: %v", entry.EpisodeKey, err), "episode_key", entry.EpisodeKey, "err", err)
		} else {
			logger.Info(fmt.Sprintf("[Registry] Real-time (StateDB): Removed deleted file from registry: %s", filepath.Base(filePath)), "file", filepath.Base(filePath))
		}
	}
}

func removeFromRegistryJSON(filePath string) {
	path := GetRegistryPath()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return
	}

	var registry map[string]EpisodeEntry
	if err := unmarshalJSON(data, &registry); err != nil {
		return
	}

	removed := false
	for key, entry := range registry {
		if entry.FilePath == filePath {
			delete(registry, key)
			removed = true
			logger.Info(fmt.Sprintf("[Registry] Real-time: Removed deleted file from registry: %s", filepath.Base(filePath)), "file", filepath.Base(filePath))
			break
		}
	}

	if removed {
		if err := saveRegistryLocked(path, registry); err != nil {
			logger.Warn(fmt.Sprintf("[Registry] WARNING: could not save registry after removing %s: %v", filepath.Base(filePath), err), "file", filepath.Base(filePath), "err", err)
		}
	}
}

// saveRegistryLocked saves the registry using syscall.Flock (JSON fallback).
func saveRegistryLocked(path string, registry map[string]EpisodeEntry) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }() // the checked Close is at the end of the write path

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }() // closing the file drops the lock anyway

	data, err := json.Marshal(registry)
	if err != nil {
		return err
	}

	if err := f.Truncate(0); err != nil {
		return err
	}
	if _, err := f.Seek(0, 0); err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		return err
	}
	return f.Close()
}

func unmarshalJSON(data []byte, target interface{}) error {
	return json.Unmarshal(data, target)
}
