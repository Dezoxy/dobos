package daemon

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"flodni/internal/cache"
	"flodni/internal/gostorm/torr"
)

// CleanupManager provides periodic cleanup of various in-memory structures
// to prevent memory leaks on long-running instances
type CleanupManager struct {
	// Deleted torrent hashes (24h TTL)
	deletedHashes map[string]time.Time
	deletedMu     sync.RWMutex

	// File read offsets (1h TTL)
	fileOffsets map[string]*offsetEntry
	offsetsMu   sync.RWMutex

	// File activity timestamps (1h TTL)
	fileActivities map[string]time.Time
	activitiesMu   sync.RWMutex

	// External components to clean (V238 Audit 1.A)
	metaCache    *cache.LRUCache
	nativeBridge engineBridge

	// Configuration
	deletedHashTTL  time.Duration
	offsetTTL       time.Duration
	activityTTL     time.Duration
	cleanupInterval time.Duration

	// Control
	mu      sync.Mutex
	stopped bool
	stopCh  chan struct{}
	logger  *slog.Logger

	// inodePruneSkipped is set while the periodic inode prune is being skipped, so the
	// skip is logged once per spell and not on every cycle.
	inodePruneSkipped atomic.Bool
}

// offsetEntry tracks read position and timestamp for sequential detection
type offsetEntry struct {
	offset    int64
	length    int
	timestamp time.Time
}

// NewCleanupManager creates a new cleanup manager with references to components
func NewCleanupManager(logger *slog.Logger, mc *cache.LRUCache, nb engineBridge) *CleanupManager {
	return &CleanupManager{
		deletedHashes:   make(map[string]time.Time),
		fileOffsets:     make(map[string]*offsetEntry),
		fileActivities:  make(map[string]time.Time),
		metaCache:       mc,
		nativeBridge:    nb,
		deletedHashTTL:  24 * time.Hour,
		offsetTTL:       1 * time.Hour,
		activityTTL:     1 * time.Hour,
		cleanupInterval: 5 * time.Minute,
		stopCh:          make(chan struct{}),
		logger:          logger,
	}
}

// Start begins the periodic cleanup loop
func (cm *CleanupManager) Start() {
	go cm.cleanupLoop()
	cm.logger.Info(fmt.Sprintf("Cleanup manager started (interval: %v)", cm.cleanupInterval), "cleanup_interval", cm.cleanupInterval)
}

// Stop stops the cleanup manager
func (cm *CleanupManager) Stop() {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if !cm.stopped {
		cm.stopped = true
		close(cm.stopCh)
		cm.logger.Info("Cleanup manager stopped")
	}
}

// cleanupLoop runs periodic cleanup
func (cm *CleanupManager) cleanupLoop() {
	ticker := time.NewTicker(cm.cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			cm.mu.Lock()
			isStopped := cm.stopped
			cm.mu.Unlock()
			if isStopped {
				return
			}
			cm.runCleanup()
		case <-cm.stopCh:
			return
		}
	}
}

// runCleanup performs cleanup of all tracked structures
func (cm *CleanupManager) runCleanup() {
	now := time.Now()
	stats := CleanupStats{}

	// Cleanup deleted hashes (24h TTL)
	cm.deletedMu.Lock()
	for hash, deletedAt := range cm.deletedHashes {
		if now.Sub(deletedAt) > cm.deletedHashTTL {
			delete(cm.deletedHashes, hash)
			stats.DeletedHashesRemoved++
		}
	}
	stats.DeletedHashesTotal = len(cm.deletedHashes)
	cm.deletedMu.Unlock()

	// Cleanup file offsets (1h TTL)
	cm.offsetsMu.Lock()
	for path, entry := range cm.fileOffsets {
		if now.Sub(entry.timestamp) > cm.offsetTTL {
			delete(cm.fileOffsets, path)
			stats.OffsetsRemoved++
		}
	}
	stats.OffsetsTotal = len(cm.fileOffsets)
	cm.offsetsMu.Unlock()

	// Cleanup file activities (1h TTL)
	cm.activitiesMu.Lock()
	for path, lastActivity := range cm.fileActivities {
		if now.Sub(lastActivity) > cm.activityTTL {
			delete(cm.fileActivities, path)
			stats.ActivitiesRemoved++
		}
	}
	stats.ActivitiesTotal = len(cm.fileActivities)
	cm.activitiesMu.Unlock()

	// V238 Audit 1.A: Cleanup Expired Metadata
	if cm.metaCache != nil {
		stats.MetadataPruned = cm.metaCache.CleanupExpired()
	}

	// LEAK-3: DirCache periodic cleanup (lazy eviction alone leaves unvisited dirs in memory)
	if globalDirCache != nil {
		globalDirCache.CleanupExpired()
	}

	// V238 Audit 1.A: Cleanup Native Bridge hashes
	if cm.nativeBridge != nil {
		stats.HashesRemoved = cm.nativeBridge.CleanupHashes()
	}

	// 5. V170-LeakFix: Cleanup InodeMap (Garbage Collection)
	// V302-Fix: WalkDir is the primary source of truth. Physical files AND directories
	// are always preserved. Deleted MKV entries are pruned when gone from disk.
	// Active torrent virtual paths are added on top.
	// Safety: skip pruning if validFiles is empty (mount unavailable).
	stats.InodeMapPruned = cm.pruneInodes(physicalSourcePath)

	// 6. V170-LeakFix: Cleanup PlaybackRegistry
	// key: path(string), value: *PlaybackState

	// Prepare allow-list of active paths from activeHandles
	activePaths := make(map[string]bool)
	app.activeHandles.Range(func(key, value interface{}) bool {
		h := key.(*MkvHandle)
		activePaths[h.path] = true
		return true
	})

	// V750: Persist active playback states to SQLite before cleanup
	app.playbackRegistry.Range(func(key, value interface{}) bool {
		ps, ok := value.(*PlaybackState)
		if ok {
			savePlaybackStateToDB(ps)
		}
		return true
	})

	app.playbackRegistry.Range(func(key, value interface{}) bool {
		path := key.(string)
		ps, ok := value.(*PlaybackState)
		if !ok {
			app.playbackRegistry.Delete(key)
			stats.PlaybackRegistryPruned++
			// V750: Also remove from SQLite
			if stateDB != nil {
				if err := stateDB.DeletePlaybackState(path); err != nil {
					cm.logger.Warn(fmt.Sprintf("[Cleanup] WARNING: delete playback state for %s failed: %v", filepath.Base(path), err), "file", filepath.Base(path), "err", err)
				}
			}
			return true
		}

		// V182: Remove if no longer active (with grace period logic if needed, but for now strict)
		// We trust activeHandles as the ground truth for "currently open file"
		// If not in activePaths, it means FUSE Release() was called.
		if !activePaths[path] {
			// Orphaned entry (playback stopped/paused)
			// V216: Allow extended grace period for resume (15 mins) to keep Priority capability active.
			// Determine the most recent sign of life:
			// 1. OpenedAt (initial open)
			// 2. ConfirmedAt (Plex Webhook)
			// 3. File Activity (Read operations)

			lastActivity := ps.LastSignOfLife()

			// Check file read activity (for non-Plex players like VLC)
			if act, ok := cm.GetLastActivity(path); ok && act.After(lastActivity) {
				lastActivity = act
			}

			if now.Sub(lastActivity) > 15*time.Minute {
				// V238 Audit 1.B: Ensure Priority is OFF before deleting zombie registry entry
				// V273: PeekTorrent instead of GetTorrent — cleanup is read-only monitoring,
				// must NOT reactivate dormant torrents (same pattern as cache.go fix).
				if ps.Hash != "" {
					if t := torr.PeekTorrent(ps.Hash); t != nil && t.Torrent != nil && t.IsPriority.Load() {
						t.IsPriority.Store(false)
						t.SetAggressiveMode(false, 0)
						cm.logger.Info(fmt.Sprintf("[V273] Force Priority OFF for zombie torrent: %s", ps.Hash[:8]), "hash", ps.Hash[:8])
					}
				}
				app.playbackRegistry.Delete(key)
				stats.PlaybackRegistryPruned++
				// V750: Also remove from SQLite
				if stateDB != nil {
					if err := stateDB.DeletePlaybackState(path); err != nil {
						cm.logger.Warn(fmt.Sprintf("[Cleanup] WARNING: delete playback state for %s failed: %v", filepath.Base(path), err), "file", filepath.Base(path), "err", err)
					}
				}
				// F1 backstop: terminate any pump still registered for this path, otherwise it
				// keeps its master slot and keeps fetching until the reader idle timeout.
				if terminateOrphanPump(path) {
					cm.logger.Info(fmt.Sprintf("[Cleanup] Force-terminated orphan pump for zombie path: %s", filepath.Base(path)), "file", filepath.Base(path))
				}
			}
			return true
		}

		// Remove entries older than 24h (just in case). This branch is only reached with
		// activePaths[path] == true, so a FUSE handle is open: its pump is not an orphan, it
		// may be serving a playback that has been running for days.
		// Terminating it here would cut the data path under a live reader, and a leaked handle
		// cannot be told apart from a long playback. Reclaiming is left to the idle handle
		// reaper and to the 15-minute branch above, which runs once no handle is open.
		if now.Sub(ps.GetOpenedAt()) > 24*time.Hour {
			app.playbackRegistry.Delete(key)
			stats.PlaybackRegistryPruned++
			// V750: Also remove from SQLite
			if stateDB != nil {
				if err := stateDB.DeletePlaybackState(path); err != nil {
					cm.logger.Warn(fmt.Sprintf("[Cleanup] WARNING: delete playback state for %s failed: %v", filepath.Base(path), err), "file", filepath.Base(path), "err", err)
				}
			}
		}
		return true
	})

	// V750: Cleanup stale playback states from SQLite (>4h old)
	if stateDB != nil {
		if n, err := stateDB.CleanupPlaybackStates(4 * time.Hour); err == nil && n > 0 {
			cm.logger.Info(fmt.Sprintf("[V750] Cleaned up %d stale playback states from DB", n), "n", n)
		}
	}

	// Log only if something was cleaned
	if stats.DeletedHashesRemoved > 0 || stats.OffsetsRemoved > 0 || stats.ActivitiesRemoved > 0 ||
		stats.InodeMapPruned > 0 || stats.PlaybackRegistryPruned > 0 || stats.MetadataPruned > 0 || stats.HashesRemoved > 0 {
		cm.logger.Info(fmt.Sprintf("Cleanup: hashes=%d offsets=%d acts=%d inodes=%d registry=%d meta=%d", stats.DeletedHashesRemoved, stats.OffsetsRemoved, stats.ActivitiesRemoved, stats.InodeMapPruned, stats.PlaybackRegistryPruned, stats.MetadataPruned), "deleted_hashes_removed", stats.DeletedHashesRemoved, "offsets_removed", stats.OffsetsRemoved, "activities_removed", stats.ActivitiesRemoved, "inode_map_pruned", stats.InodeMapPruned, "playback_registry_pruned", stats.PlaybackRegistryPruned, "metadata_pruned", stats.MetadataPruned)
	}
}

// --- Deleted Hashes Management ---

// pruneInodes drops the inode entries of files the walk of root did not find and
// returns how many it dropped.
func (cm *CleanupManager) pruneInodes(root string) int {
	if globalInodeMap == nil {
		return 0
	}
	validFiles := make(map[string]bool)

	// Primary: all physical files AND directories on disk (including virtual MKV stubs).
	// Directories must be included — their inodes are in the InodeMap too, and pruning
	// them causes FUSE to regenerate them on every Plex/Samba directory traversal.
	walk := walkLibrary(root, validFiles)

	// Secondary: virtual paths for active torrents (served by FUSE, not on disk).
	// ListActiveTorrent() returns only in-memory torrents — no BoltDB deserialization.
	// ListTorrent() would unmarshal all ~3000+ DB entries every cleanup cycle → OOM.
	for _, t := range torr.ListActiveTorrent() {
		if t == nil {
			continue
		}
		for _, f := range t.Files() {
			validFiles[filepath.Join(root, f.Path())] = true
		}
	}

	// Protezione streaming: i file attualmente in lettura non devono perdere
	// la loro inode entry anche se WalkDir non li vede (es. rescan Plex concorrente).
	for _, openPath := range globalOpenTracker.OpenPaths() {
		validFiles[openPath] = true
	}

	// The walk adds root itself, so an empty or unmounted library still leaves a
	// non-empty set: the same guard as the start-up prune decides instead.
	if !pruneGuard(cm.logger, "Inode", walk.complete, walk.errors, walk.stubs, &cm.inodePruneSkipped) {
		return 0
	}
	return globalInodeMap.PruneMissing(validFiles)
}

// libraryWalk is what walkLibrary saw: both movies and tv present, the errors met and
// the stubs found under them.
type libraryWalk struct {
	complete bool
	errors   int
	stubs    int
}

// walkLibrary adds every path under root, files and directories, to valid.
func walkLibrary(root string, valid map[string]bool) libraryWalk {
	var w libraryWalk
	if root == "" {
		return w
	}
	movies, tv := filepath.Join(root, "movies"), filepath.Join(root, "tv")
	w.complete = isDir(movies) && isDir(tv)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			w.errors++
			return nil
		}
		valid[path] = true
		if !d.IsDir() && filepath.Ext(path) == ".mkv" && (underDir(path, movies) || underDir(path, tv)) {
			w.stubs++
		}
		return nil
	})
	if err != nil {
		w.errors++
	}
	return w
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func underDir(path, dir string) bool {
	return strings.HasPrefix(path, dir+string(filepath.Separator))
}

// --- File Offset Management ---

// UpdateOffset records the last read position for a file
func (cm *CleanupManager) UpdateOffset(path string, offset int64, length int) {
	cm.offsetsMu.Lock()
	cm.fileOffsets[path] = &offsetEntry{
		offset:    offset,
		length:    length,
		timestamp: time.Now(),
	}
	cm.offsetsMu.Unlock()
}

// --- File Activity Management ---

// UpdateActivity records activity for a file
func (cm *CleanupManager) UpdateActivity(path string) {
	cm.activitiesMu.Lock()
	cm.fileActivities[path] = time.Now()
	cm.activitiesMu.Unlock()
}

func (cm *CleanupManager) GetLastActivity(path string) (time.Time, bool) {
	cm.activitiesMu.RLock()
	t, exists := cm.fileActivities[path]
	cm.activitiesMu.RUnlock()
	return t, exists
}

// Statistics

// CleanupStats represents cleanup statistics
type CleanupStats struct {
	DeletedHashesTotal     int
	DeletedHashesRemoved   int
	OffsetsTotal           int
	OffsetsRemoved         int
	ActivitiesTotal        int
	ActivitiesRemoved      int
	InodeMapPruned         int // V170
	PlaybackRegistryPruned int // V170
	MetadataPruned         int // V238
	HashesRemoved          int // V238
}

// Stats returns current cleanup manager statistics
func (cm *CleanupManager) Stats() CleanupStats {
	cm.deletedMu.RLock()
	deletedHashesTotal := len(cm.deletedHashes)
	cm.deletedMu.RUnlock()

	cm.offsetsMu.RLock()
	offsetsTotal := len(cm.fileOffsets)
	cm.offsetsMu.RUnlock()

	cm.activitiesMu.RLock()
	activitiesTotal := len(cm.fileActivities)
	cm.activitiesMu.RUnlock()

	return CleanupStats{
		DeletedHashesTotal: deletedHashesTotal,
		OffsetsTotal:       offsetsTotal,
		ActivitiesTotal:    activitiesTotal,
	}
}
