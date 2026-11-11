package library

import (
	"path/filepath"
	"regexp"
	"strings"
)

// reShowFolderYear splits "A_Good_Girls_Guide_to_Murder (2024)" into name and year.
var reShowFolderYear = regexp.MustCompile(`^(.*?)\s*\((\d{4})\)$`)

// reSeasonDir matches the season directory a TV stub lives in, e.g. "Season.01".
var reSeasonDir = regexp.MustCompile(`(?i)^season[._ ]?\d{1,3}$`)

// ShowNameFromEpisodePath recovers the readable show name from an episode path
// laid out as <tv>/<Show Folder>/Season.NN/<episode>.mkv. It returns "" when the
// path does not follow that layout: on a flat or manually placed file the parent
// of the parent is the library root, which is not a show name.
func ShowNameFromEpisodePath(episodePath string) string {
	seasonDir := filepath.Dir(episodePath)
	if !reSeasonDir.MatchString(filepath.Base(seasonDir)) {
		return ""
	}
	folder := filepath.Base(filepath.Dir(seasonDir))
	if folder == "." || folder == string(filepath.Separator) || folder == "" {
		return ""
	}
	if m := reShowFolderYear.FindStringSubmatch(folder); len(m) == 3 {
		folder = m[1]
	}
	return strings.TrimSpace(strings.ReplaceAll(folder, "_", " "))
}
