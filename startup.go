package main

import (
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"flodni/internal/cache"
	"flodni/internal/vfs"
)

// StartupCacheBuilder pre-populates metadata cache at startup
// This dramatically improves Plex scan performance (30s -> 10s for 1000+ files)
// Runs in background to avoid delaying mount operation
type StartupCacheBuilder struct {
	sourcePath string
	metaCache  *cache.LRUCache
	logger     *log.Logger

	// Statistics
	mu             sync.Mutex
	filesProcessed int
	filesSkipped   int
	errors         int
	startTime      time.Time
	foundFiles     map[string]bool // V142: Track files for GC
	foundHashes    map[string]bool // lower-case info hashes of every stub seen, for pruning
}

// NewStartupCacheBuilder creates a new startup cache builder
func NewStartupCacheBuilder(sourcePath string, metaCache *cache.LRUCache, logger *log.Logger) *StartupCacheBuilder {
	return &StartupCacheBuilder{
		sourcePath:  sourcePath,
		metaCache:   metaCache,
		logger:      logger,
		startTime:   time.Now(),
		foundFiles:  make(map[string]bool),
		foundHashes: make(map[string]bool),
	}
}

// Start begins background cache population
// Returns immediately, processing happens in goroutine
func (b *StartupCacheBuilder) Start() {
	b.logger.Printf("Starting cache pre-population from %s", b.sourcePath)

	go func() {
		// Process movies directory
		walked := 0
		moviesPath := filepath.Join(b.sourcePath, "movies")
		if _, err := os.Stat(moviesPath); err == nil {
			b.processDirectory(moviesPath, false) // Not recursive
			walked++
		}

		// Process TV directory (recursive for seasons)
		tvPath := filepath.Join(b.sourcePath, "tv")
		if _, err := os.Stat(tvPath); err == nil {
			b.processDirectory(tvPath, true) // Recursive
			walked++
		}

		// Log final statistics
		duration := time.Since(b.startTime)
		b.mu.Lock()
		processed := b.filesProcessed
		skipped := b.filesSkipped
		errors := b.errors
		b.mu.Unlock()

		b.logger.Printf("Cache pre-population complete: %d processed, %d skipped, %d errors in %v",
			processed, skipped, errors, duration)

		// Log cache statistics
		stats := b.metaCache.Stats()
		b.logger.Printf("Cache after startup: %d entries, %.2f MB used of %.2f MB capacity",
			stats.Entries, float64(stats.Size)/(1024*1024), float64(stats.Capacity)/(1024*1024))

		b.pruneReadability(walked == 2)
		b.pruneFileState(walked == 2)

		// V133: Save inode map after startup scan completes
		// This ensures the map is persisted even if no background save triggered
		if globalInodeMap != nil && globalInodeMap.IsDirty() {
			if err := globalInodeMap.SaveToDisk(); err != nil {
				b.logger.Printf("InodeMap: Post-startup save error: %v", err)
			} else {
				files, dirs, _, _ := GetInodeMapStats()
				b.logger.Printf("InodeMap: Saved after startup scan (%d files, %d dirs)", files, dirs)
			}
		}

		// V142: Inode GC - Remove ghost entries (files that no longer exist)
		if globalInodeMap != nil {
			b.mu.Lock()
			// Make a copy or pass reference? Pass reference since we are done modifying it
			// However, PruneMissing only reads from it, so it's safe if we don't modify it anymore.
			// We hold b.mu just to be safe or we can just pass it as we are in the final serial block.
			foundSet := b.foundFiles
			b.mu.Unlock()

			pruned := globalInodeMap.PruneMissing(foundSet)
			if pruned > 0 {
				b.logger.Printf("Startup GC: Pruned %d ghost inodes", pruned)
			}
		}
	}()
}

// processDirectory walks directory and processes all .mkv files
// If recursive is true, also processes subdirectories
func (b *StartupCacheBuilder) processDirectory(dirPath string, recursive bool) {
	// Semaphore to limit concurrent file operations
	// Too many concurrent reads can overwhelm the filesystem
	sem := make(chan struct{}, 10) // Max 10 concurrent
	var wg sync.WaitGroup

	// Walk directory
	err := filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			b.logger.Printf("Startup cache: error accessing %s: %v", path, err)
			b.incrementErrors()
			return nil // Continue walking
		}

		// Skip directories if not recursive
		if info.IsDir() {
			if !recursive && path != dirPath {
				return filepath.SkipDir
			}
			// V133: Register directory in inode map, keyed by the full path the FUSE
			// call sites look up — a relative key leaves an entry nobody reads.
			if path != b.sourcePath {
				getDirInodeFromMap(path)
			}
			return nil
		}

		// Process only .mkv files
		if filepath.Ext(path) != ".mkv" {
			return nil
		}

		// Check if already in cache (fast path)
		if cached, ok := b.metaCache.Get(path); ok {
			// Skipped files still belong to the library: a FUSE lookup can fill the
			// cache while this walk runs, and their rows must not be pruned.
			b.noteHash(cached.URL)
			b.incrementSkipped()
			return nil
		}

		// Acquire semaphore
		wg.Add(1)
		sem <- struct{}{}

		// Process file in goroutine
		go func(filePath string) {
			defer wg.Done()
			defer func() { <-sem }()

			b.processFile(filePath)
		}(path)

		return nil
	})

	if err != nil {
		b.logger.Printf("Startup cache: error walking %s: %v", dirPath, err)
		b.incrementErrors()
	}

	// Wait for all goroutines to complete
	wg.Wait()
}

// processFile reads metadata for a single file and adds to cache
func (b *StartupCacheBuilder) processFile(path string) {
	// Read metadata from file
	fileMeta, err := vfs.ReadMetadataFromFile(path)
	if err != nil {
		b.logger.Printf("Startup cache: error reading metadata for %s: %v", path, err)
		b.incrementErrors()
		return
	}

	// Convert to Metadata format
	meta := &vfs.Metadata{
		URL:    fileMeta.URL,
		Size:   fileMeta.Size,
		Mtime:  fileMeta.Mtime,
		Path:   fileMeta.Path,
		ImdbID: fileMeta.ImdbID,
	}

	// Calculate approximate size
	size := approximateMetadataSize(meta)

	// Add to cache
	b.metaCache.Put(path, meta, size)

	// V133: Add to inode map for deterministic inode generation
	// This ensures Plex sees the same inode after restarts
	// BUG FIX: Pass full path instead of just filename to avoid collisions
	addFileToInodeMap(path, fileMeta.URL)

	b.noteHash(fileMeta.URL)
	b.incrementProcessed(path)
}

// noteHash records the info hash of a stub's stream URL as part of the library.
func (b *StartupCacheBuilder) noteHash(url string) {
	hash, _ := vfs.ExtractHashAndIndex(url)
	if hash == "" {
		return
	}
	b.mu.Lock()
	b.foundHashes[hash] = true
	b.mu.Unlock()
}

// libraryIfWhole returns the hashes of the stubs the walk found, only when the walk saw
// the library whole: a missing source directory, any walk error or an empty result
// means it did not, and an unmounted library must not wipe a table.
func (b *StartupCacheBuilder) libraryIfWhole(what string, complete bool) (map[string]bool, bool) {
	b.mu.Lock()
	errs, keep := b.errors, b.foundHashes
	b.mu.Unlock()
	if !complete || errs > 0 || len(keep) == 0 {
		b.logger.Printf("%s prune skipped: complete=%v errors=%d stubs=%d", what, complete, errs, len(keep))
		return nil, false
	}
	return keep, true
}

// pruneReadability drops readability rows of torrents that left the library. It runs
// only after a complete walk. Rows touched since the process started are kept.
func (b *StartupCacheBuilder) pruneReadability(complete bool) {
	if stateDB == nil {
		return
	}
	keep, ok := b.libraryIfWhole("Readability", complete)
	if !ok {
		return
	}
	n, err := stateDB.PruneReadability(keep, b.startTime)
	if err != nil {
		b.logger.Printf("WARNING: readability prune: %v", err)
		return
	}
	if n > 0 {
		b.logger.Printf("Readability prune: removed %d records of torrents no longer in the library", n)
	}
}

// pruneFileState drops the played and evicted marks of torrents that left the library,
// under the same guard as pruneReadability.
func (b *StartupCacheBuilder) pruneFileState(complete bool) {
	if stateDB == nil {
		return
	}
	keep, ok := b.libraryIfWhole("File state", complete)
	if !ok {
		return
	}
	dropped, err := stateDB.PruneFileStates(keep, b.startTime)
	if err != nil {
		b.logger.Printf("WARNING: file state prune: %v", err)
		return
	}
	for _, h := range dropped {
		fileStates.ForgetMemory(h)
	}
	if len(dropped) > 0 {
		b.logger.Printf("File state prune: removed the marks of %d torrents no longer in the library", len(dropped))
	}
}

// Statistics helpers (thread-safe)
func (b *StartupCacheBuilder) incrementProcessed(path string) {
	b.mu.Lock()
	b.filesProcessed++
	b.foundFiles[path] = true // V142: Mark as found
	b.mu.Unlock()
}

func (b *StartupCacheBuilder) incrementSkipped() {
	b.mu.Lock()
	b.filesSkipped++
	b.mu.Unlock()
}

func (b *StartupCacheBuilder) incrementErrors() {
	b.mu.Lock()
	b.errors++
	b.mu.Unlock()
}
