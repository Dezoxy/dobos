package torr

import (
	sets "flodni/internal/gostorm/settings"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
)

// ScrapeTarget is a stored torrent and the trackers its scrape may ask: the
// announce URLs of its stored record, nothing else. The URLs hold passkeys, so
// callers must not log or return them.
type ScrapeTarget struct {
	Hash     string // lower-case hex info hash
	Trackers []string
}

// StoredScrapeTargets reads the stored records only (no torrent is woken, no
// lock of the engine is taken beyond the settings read).
func StoredScrapeTargets() []ScrapeTarget {
	return scrapeTargetsOf(sets.ListTorrent(), publicTrackers)
}

// scrapeTargetsOf is the pure part. Hard rule 3: a torrent that is private, or
// whose metadata is not known yet and so might be, is scraped at its own
// trackers only. Every tracker we add on our own account (the public lists) is
// dropped for it, including ones a record saved before the retracker gate holds;
// no DHT, PEX or public fallback exists in this path at all. A public torrent
// keeps its stored trackers as they are.
func scrapeTargetsOf(list []*sets.TorrentDB, public func() []string) []ScrapeTarget {
	var out []ScrapeTarget
	for _, db := range list {
		if db == nil || db.TorrentSpec == nil {
			continue
		}
		var info *metainfo.Info
		if len(db.InfoBytes) > 0 {
			var i metainfo.Info
			if bencode.Unmarshal(db.InfoBytes, &i) == nil {
				info = &i
			}
		}
		drop := map[string]bool{}
		if !infoPublic(info) {
			for _, tr := range public() {
				drop[tr] = true
			}
		}
		seen := map[string]bool{}
		var trackers []string
		for _, tier := range db.Trackers {
			for _, tr := range tier {
				if tr == "" || seen[tr] || drop[tr] {
					continue
				}
				seen[tr] = true
				trackers = append(trackers, tr)
			}
		}
		out = append(out, ScrapeTarget{Hash: db.InfoHash.HexString(), Trackers: trackers})
	}
	return out
}
