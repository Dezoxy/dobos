package daemon

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"flodni/internal/atomicfile"
)

// TorrentRemover writes the blacklist and names a title from its path. It no
// longer removes torrents: unlink goes through the library's reference check.
type TorrentRemover struct {
	logger *slog.Logger
}

// NewTorrentRemover creates a new torrent remover
func NewTorrentRemover(logger *slog.Logger) *TorrentRemover {
	return &TorrentRemover{logger: logger}
}

// deriveTitleFromPath gets the show or movie name from the directory structure
func (tr *TorrentRemover) deriveTitleFromPath(path string) string {
	// Example: /mnt/torrserver/tv/9-1-1_Nashville (2025)/Season.01/file.mkv
	// We want "9-1-1_Nashville (2025)"
	parts := strings.Split(filepath.Dir(path), string(os.PathSeparator))

	// Default to filename if path is shallow
	if len(parts) < 2 {
		return filepath.Base(path)
	}

	// If it's a TV show (contains "Season.XX"), take the parent
	lastDir := parts[len(parts)-1]
	if strings.HasPrefix(lastDir, "Season.") && len(parts) >= 2 {
		return parts[len(parts)-2]
	}

	return lastDir
}

// addToBlacklist adds a hash and title to the persistent blacklist file
func (tr *TorrentRemover) addToBlacklist(hash, title string) error {
	return tr.addToBlacklistAt(filepath.Join(GetStateDir(), "blacklist.json"), hash, title)
}

// blacklistMu serialises the read-modify-write: the unlink handler, the Library API
// and the reaper write concurrently, and the last rename would drop the others' hashes.
var blacklistMu sync.Mutex

func (tr *TorrentRemover) addToBlacklistAt(blacklistPath, hash, title string) error {
	blacklistMu.Lock()
	defer blacklistMu.Unlock()
	// Readers look the hash up lowercased, so a capitalised entry would never match.
	hash = strings.ToLower(hash)

	type Blacklist struct {
		Hashes map[string]string `json:"hashes"`
		Titles []string          `json:"titles"`
	}

	var bl Blacklist
	data, err := ioutil.ReadFile(blacklistPath)
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &bl); err != nil {
			// Set aside, never rewritten in place: the bytes stay for a repair, and the
			// readers already see an empty blacklist while the file is unparsable.
			aside := fmt.Sprintf("%s.corrupt-%d", blacklistPath, time.Now().UnixNano())
			if rerr := os.Rename(blacklistPath, aside); rerr != nil {
				return fmt.Errorf("blacklist %s is unreadable (%v) and cannot be set aside: %w", blacklistPath, err, rerr)
			}
			tr.logger.Warn(fmt.Sprintf("BLACKLIST WARNING: %s was unreadable (%v), moved to %s; starting a new one: every release blacklisted before is no longer excluded until that file is repaired and merged back", blacklistPath, err, aside), "blacklist_path", blacklistPath, "err", err, "aside", aside)
			bl = Blacklist{}
		}
	case !os.IsNotExist(err):
		return fmt.Errorf("read blacklist: %w", err)
	}
	if bl.Hashes == nil {
		bl.Hashes = make(map[string]string)
	}
	if bl.Titles == nil {
		bl.Titles = []string{}
	}

	changed := false
	if _, exists := bl.Hashes[hash]; !exists {
		bl.Hashes[hash] = title
		changed = true
	}

	// SUPER-CLEAN Title Normalization (for robust matching)
	if title != "" {
		// 1. Convert to lowercase
		cleanTitle := strings.ToLower(title)
		// 2. Replace underscores and dots with spaces
		cleanTitle = strings.ReplaceAll(cleanTitle, "_", " ")
		cleanTitle = strings.ReplaceAll(cleanTitle, ".", " ")
		// 3. Remove year (e.g., "(2023)" or "2023")
		yearRegex := regexp.MustCompile(`\(?\d{4}\)?`)
		cleanTitle = yearRegex.ReplaceAllString(cleanTitle, "")
		// 4. Remove ALL non-alphanumeric characters AND spaces (SQUEEZE everything)
		// This ensures "Grey's Anatomy" becomes "greysanatomy" and "9-1-1" becomes "911"
		nonAlphaRegex := regexp.MustCompile(`[^a-z0-9]`)
		cleanTitle = nonAlphaRegex.ReplaceAllString(cleanTitle, "")

		if cleanTitle != "" {

			found := false
			for _, t := range bl.Titles {
				if t == cleanTitle {
					found = true
					break
				}
			}
			if !found {
				bl.Titles = append(bl.Titles, cleanTitle)
				changed = true
			}
		}
	}

	if !changed {
		return nil
	}
	newData, err := json.MarshalIndent(bl, "", "  ")
	if err != nil {
		return err
	}
	if err := atomicfile.WriteFile(blacklistPath, newData, 0644); err != nil {
		return fmt.Errorf("write blacklist: %w", err)
	}
	tr.logger.Info(fmt.Sprintf("BLACKLISTED: %s (%s)", hash[:8], title), "hash", hash[:8], "title", title)
	return nil
}
