package library

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// FileFacts is what the daemon knows about one file apart from its stub: the warm
// record, the played mark, the eviction mark and the media-server session. The library
// reads it through Config.FileState and keeps no copy.
type FileFacts struct {
	// Readability is the swarm's evidence (warmed, present, failed), Cache the state of
	// the windows on disk (cached, evicted, dropped_*); empty means nothing recorded.
	Readability string
	Cache       string
	// Queued is true while warm on add holds the file for an attempt that has not
	// recorded an outcome.
	Queued         bool
	FailedAttempts int64
	// PlayedAt is the last recorded play, zero when never played. Protected is true
	// while that play still keeps the file out of the first rounds of eviction.
	PlayedAt  time.Time
	Protected bool
	Evicted   bool
	// Confirmed is true while a media-server session is open on the file and a webhook
	// has confirmed it.
	Confirmed bool
}

// File states, the one-word summary of FileStateResponse.
const (
	StateAdded     = "added"
	StateWarming   = "warming"
	StateWarm      = "warm"
	StateEvicted   = "evicted"
	StatePlayed    = "played"
	StateConfirmed = "confirmed"
)

// FileStub is the stub the file is linked from.
type FileStub struct {
	Path     string `json:"path"`
	FusePath string `json:"fuse_path"`
	Size     int64  `json:"size"`
	Type     string `json:"type"`
	Season   int    `json:"season,omitempty"`
	Episode  int    `json:"episode,omitempty"`
}

// FileRegistry is the episode registry row behind a TV stub.
type FileRegistry struct {
	EpisodeKey   string `json:"episode_key"`
	QualityScore int    `json:"quality_score"`
	Source       string `json:"source"`
}

// FileWarm is the warm record as the health API reports it.
type FileWarm struct {
	Readability    string `json:"readability"`
	Cache          string `json:"cache"`
	Queued         bool   `json:"queued"`
	FailedAttempts int64  `json:"failed_attempts"`
}

// FileStateResponse is the answer of GET /api/library/file: the facts apart, and one
// summary state because a file is warm and played at once.
type FileStateResponse struct {
	Hash      string        `json:"hash"`
	FileIndex int           `json:"file_index"`
	State     string        `json:"state"`
	Stub      FileStub      `json:"stub"`
	Registry  *FileRegistry `json:"registry"`
	Warm      FileWarm      `json:"warm"`
	PlayedAt  *string       `json:"played_at"`
	Protected bool          `json:"protected"`
	Evicted   bool          `json:"evicted"`
	Confirmed bool          `json:"confirmed"`
}

// summarize picks the one word. The order is what a caller most needs to act on: a
// session open now, then bytes gone, then a protecting play, then warmth, then a queued
// attempt; a stub nothing has touched is just added.
func summarize(f FileFacts) string {
	warm := (f.Readability == "warmed" || f.Readability == "present") && f.Cache == "cached"
	switch {
	case f.Confirmed:
		return StateConfirmed
	case f.Evicted || f.Cache == "evicted":
		return StateEvicted
	case f.Protected:
		return StatePlayed
	case warm:
		return StateWarm
	case f.Queued:
		return StateWarming
	}
	return StateAdded
}

// findFile returns the stub that links to file fileIndex of the torrent, and its kind.
// A file nothing links to (never filed, removed, or replaced) is a 404.
func (m *Manager) findFile(hash string, fileIndex int) (*AddedFile, string, error) {
	hash = strings.ToLower(strings.TrimSpace(hash))
	if !reInfoHash.MatchString(hash) {
		return nil, "", errf(http.StatusBadRequest, "malformed info hash %q", hash)
	}
	if fileIndex < 0 {
		return nil, "", errf(http.StatusBadRequest, "file_index must not be negative")
	}
	hash = canonicalHashKey(hash)
	for _, k := range []string{"movie", "tv"} {
		dir := m.cfg.MoviesDir
		if k == "tv" {
			dir = m.cfg.TVDir
		}
		if dir == "" {
			continue
		}
		found, err := m.findByHash(k, hash)
		if err != nil {
			return nil, "", err
		}
		for i := range found {
			if found[i].FileIndex == fileIndex {
				return &found[i], k, nil
			}
		}
	}
	return nil, "", errf(http.StatusNotFound, "no stub links to file %d of %s", fileIndex, hash)
}

// FileState projects what the stubs, the registry and the daemon's file state say about
// one file. It is read-only and keeps no store: a file with no stub linking to it (a
// replaced or removed one, whose state rows are deleted with its torrent) is a 404.
func (m *Manager) FileState(hash string, fileIndex int) (*FileStateResponse, error) {
	stub, kind, err := m.findFile(hash, fileIndex)
	if err != nil {
		return nil, err
	}
	hash = canonicalHashKey(hash)

	resp := &FileStateResponse{
		Hash: hash, FileIndex: fileIndex,
		Stub: FileStub{Path: stub.Path, FusePath: stub.FusePath, Size: stub.Size, Type: kind,
			Season: stub.Season, Episode: stub.Episode},
		Warm: FileWarm{Readability: "unknown", Cache: "unknown"},
	}
	if kind == "tv" && m.cfg.Registry != nil {
		rows, err := m.cfg.Registry.EpisodesByFilePath(stub.Path)
		if err != nil {
			m.cfg.Logger.Printf("[LibraryAPI] WARNING: cannot read the registry rows of %s: %v", stub.Path, err)
			return nil, errf(http.StatusServiceUnavailable, "cannot read the episode registry")
		}
		if len(rows) > 0 {
			resp.Registry = &FileRegistry{EpisodeKey: rows[0].EpisodeKey, QualityScore: rows[0].QualityScore, Source: rows[0].Source}
		}
	}

	var facts FileFacts
	if m.cfg.FileState != nil {
		facts = m.cfg.FileState(hash, fileIndex)
	}
	if facts.Readability != "" {
		resp.Warm.Readability = facts.Readability
	}
	if facts.Cache != "" {
		resp.Warm.Cache = facts.Cache
	}
	resp.Warm.Queued, resp.Warm.FailedAttempts = facts.Queued, facts.FailedAttempts
	if !facts.PlayedAt.IsZero() {
		s := facts.PlayedAt.UTC().Format(time.RFC3339)
		resp.PlayedAt = &s
	}
	resp.Protected, resp.Evicted, resp.Confirmed = facts.Protected, facts.Evicted, facts.Confirmed
	resp.State = summarize(facts)
	return resp, nil
}

// File serves GET /api/library/file?hash=&file_index=.
func (h *Handler) File(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	q := r.URL.Query()
	idx, err := strconv.Atoi(q.Get("file_index"))
	if q.Get("hash") == "" || q.Get("file_index") == "" || err != nil {
		writeError(w, http.StatusBadRequest, "hash and a numeric file_index are required")
		return
	}
	resp, err := h.mgr.FileState(q.Get("hash"), idx)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// ConfirmRequest names the file a manual play mark is for.
type ConfirmRequest struct {
	Hash      string `json:"hash"`
	FileIndex *int   `json:"file_index"`
}

// Confirm records a play of the file by hand, the way a play webhook does for the one
// file it resolves to, and answers with the file's state. It records the play only: it
// does not confirm a session, so a reader-budget hold stays as it is. A file no stub
// links to is a 404 and nothing is recorded.
func (m *Manager) Confirm(req ConfirmRequest) (*FileStateResponse, error) {
	if req.FileIndex == nil {
		return nil, errf(http.StatusBadRequest, "hash and file_index are required")
	}
	if _, _, err := m.findFile(req.Hash, *req.FileIndex); err != nil {
		return nil, err
	}
	if m.cfg.Confirm == nil {
		return nil, errf(http.StatusServiceUnavailable, "play marks are unavailable")
	}
	m.cfg.Confirm(canonicalHashKey(strings.ToLower(strings.TrimSpace(req.Hash))), *req.FileIndex)
	return m.FileState(req.Hash, *req.FileIndex)
}

// Confirm serves POST /api/library/confirm. A manual play mark changes what eviction
// protects, so it fails closed: with no api_token set the control port is open to
// anyone who can reach it, and this route answers 403 and records nothing.
func (h *Handler) Confirm(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	if h.mgr.cfg.TokenSet == nil || !h.mgr.cfg.TokenSet() {
		writeError(w, http.StatusForbidden, "confirm needs api_token to be set: a manual play mark changes eviction protection")
		return
	}
	var req ConfirmRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	resp, err := h.mgr.Confirm(req)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}
