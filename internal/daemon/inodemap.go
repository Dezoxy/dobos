package daemon

import (
	"flodni/internal/vfs"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

// --- InodeMap globals & wrappers ---

var globalInodeMap *vfs.InodeMap

func InitGlobalInodeMap(stateDir string, logger *slog.Logger) error {
	savePath := vfs.GetDefaultInodeMapPath(stateDir)
	dir := filepath.Dir(savePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create inode map directory: %w", err)
	}
	globalInodeMap = vfs.NewInodeMap(savePath, logger)
	return nil
}

// finishGlobalInodeMapInit loads the map and starts the saver. Split from
// InitGlobalInodeMap so the load runs once, after StateDB had a chance to attach:
// loading JSON first would report an empty map on every migrated install.
func finishGlobalInodeMapInit(logger *slog.Logger) {
	if globalInodeMap == nil {
		return
	}
	if !globalInodeMap.HasDB() {
		if err := globalInodeMap.LoadFromDisk(); err != nil {
			logger.Warn(fmt.Sprintf("WARNING: Failed to load inode map: %v (falling back to filename hash)", err), "err", err)
		}
		files, dirs, _, _ := GetInodeMapStats()
		logger.Info(fmt.Sprintf("InodeMap: Initialized with %d files, %d dirs from %s", files, dirs, vfs.GetDefaultInodeMapPath(GetStateDir())), "files", files, "dirs", dirs)
	}
	globalInodeMap.StartBackgroundSaver()
}

func ShutdownGlobalInodeMap() {
	if globalInodeMap != nil {
		globalInodeMap.Stop()
	}
}

func getFileInodeFromMap(fullPath string) uint64 {
	if globalInodeMap == nil {
		return hashFilenameToInode(filepath.Base(fullPath)) & vfs.InodeFileMask
	}
	if inode := globalInodeMap.GetFileInode(fullPath); inode != 0 {
		return inode
	}
	if inode := globalInodeMap.GetFileInodeByName(filepath.Base(fullPath)); inode != 0 {
		return inode
	}
	return hashFilenameToInode(filepath.Base(fullPath)) & vfs.InodeFileMask
}

func getDirInodeFromMap(relativePath string) uint64 {
	if globalInodeMap == nil {
		return vfs.GenerateDirInode(relativePath)
	}
	return globalInodeMap.GetDirInode(relativePath)
}

func addFileToInodeMap(fullPath, url string) uint64 {
	if globalInodeMap == nil {
		return 0
	}
	hash, index := vfs.ExtractHashAndIndex(url)
	if hash == "" {
		return 0
	}
	return globalInodeMap.AddFile(fullPath, hash, index)
}

func GetInodeMapStats() (files, dirs, hits, misses int64) {
	if globalInodeMap == nil {
		return 0, 0, 0, 0
	}
	return globalInodeMap.Stats()
}
