package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"time"

	"flodni/internal/metadb"
)

// readabilityLister is the read side of the readability record.
type readabilityLister interface {
	ListReadability(minFailures int) ([]metadb.Readability, error)
}

type torrentHealth struct {
	Hash                string  `json:"hash"`
	ConsecutiveFailures int64   `json:"consecutive_failures"`
	FirstFailureAt      *string `json:"first_failure_at"`
	LastFailureAt       *string `json:"last_failure_at"`
	LastFailureReason   *string `json:"last_failure_reason"`
	LastFailureSource   *string `json:"last_failure_source"`
	LastSwarmReadAt     *string `json:"last_swarm_read_at"`
}

func unixOrNil(sec int64) *string {
	if sec <= 0 {
		return nil
	}
	s := time.Unix(sec, 0).UTC().Format(time.RFC3339)
	return &s
}

func strOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// newTorrentsHealthHandler serves GET /api/torrents/health: the readability record
// as stored, per torrent. A nil store (state database off or failed to open) answers
// 503, because nothing is recorded then.
func newTorrentsHealthHandler(store readabilityLister, logger *log.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if store == nil {
			http.Error(w, "readability record unavailable: state database is off", http.StatusServiceUnavailable)
			return
		}
		q := r.URL.Query()
		minFailures := 0
		switch q.Get("failing") {
		case "", "0":
		case "1":
			minFailures = 1
		default:
			http.Error(w, "failing must be 0 or 1", http.StatusBadRequest)
			return
		}
		if v := q.Get("min_failures"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				http.Error(w, "min_failures must be a non-negative integer", http.StatusBadRequest)
				return
			}
			minFailures = max(minFailures, n)
		}
		rows, err := store.ListReadability(minFailures)
		if err != nil {
			logger.Printf("WARNING: readability record: %v", err)
			http.Error(w, "cannot read the readability record", http.StatusInternalServerError)
			return
		}
		out := make([]torrentHealth, 0, len(rows))
		for _, row := range rows {
			out = append(out, torrentHealth{
				Hash:                row.Hash,
				ConsecutiveFailures: row.FailCount,
				FirstFailureAt:      unixOrNil(row.FirstFail),
				LastFailureAt:       unixOrNil(row.LastFail),
				LastFailureReason:   strOrNil(row.LastReason),
				LastFailureSource:   strOrNil(row.LastSource),
				LastSwarmReadAt:     unixOrNil(row.LastSwarmRead),
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			Total    int             `json:"total"`
			Torrents []torrentHealth `json:"torrents"`
		}{Total: len(out), Torrents: out})
	}
}
