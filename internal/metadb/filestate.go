package metadb

import (
	"fmt"
	"strings"
	"time"
)

// FileState is one media file's stored cache state, times in Unix seconds, 0 when
// unset. LastPlayedAt is the last play a media server webhook confirmed;
// EvictedAt is the time quota eviction removed the file's windows, kept until a
// play that started cold is counted.
type FileState struct {
	Hash         string
	FileIdx      int
	LastPlayedAt int64
	EvictedAt    int64
}

// MarkFilePlayed stamps a confirmed play. Every play moves the time forward; a
// stamp older than the stored one never moves it back.
func (d *DB) MarkFilePlayed(hash string, fileIdx int, at time.Time) error {
	_, err := d.db.Exec(`
		INSERT INTO file_state (hash, file_idx, last_played_at) VALUES (?, ?, ?)
		ON CONFLICT(hash, file_idx) DO UPDATE SET
			last_played_at = MAX(last_played_at, excluded.last_played_at)`,
		strings.ToLower(hash), fileIdx, at.Unix())
	return err
}

// FileKey names one media file of a torrent.
type FileKey struct {
	Hash    string
	FileIdx int
}

// MarkFilesEvicted stamps the eviction of several files in one transaction. It keeps
// last_played_at: the played mark outlives the bytes.
func (d *DB) MarkFilesEvicted(keys []FileKey, at time.Time) error {
	if len(keys) == 0 {
		return nil
	}
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	for _, k := range keys {
		if _, err := tx.Exec(`
			INSERT INTO file_state (hash, file_idx, evicted_at) VALUES (?, ?, ?)
			ON CONFLICT(hash, file_idx) DO UPDATE SET evicted_at = excluded.evicted_at`,
			strings.ToLower(k.Hash), k.FileIdx, at.Unix()); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("mark evicted %s/%d: %w", k.Hash, k.FileIdx, err)
		}
	}
	return tx.Commit()
}

// ClearFileEvicted removes the eviction mark and keeps the played time.
func (d *DB) ClearFileEvicted(hash string, fileIdx int) error {
	_, err := d.db.Exec(`UPDATE file_state SET evicted_at = 0 WHERE hash = ? AND file_idx = ?`,
		strings.ToLower(hash), fileIdx)
	return err
}

// LoadFileStates returns every stored row.
func (d *DB) LoadFileStates() ([]FileState, error) {
	rows, err := d.db.Query(`SELECT hash, file_idx, last_played_at, evicted_at FROM file_state ORDER BY hash, file_idx`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FileState{}
	for rows.Next() {
		var s FileState
		if err := rows.Scan(&s.Hash, &s.FileIdx, &s.LastPlayedAt, &s.EvictedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// DeleteFileStates deletes every row of a torrent that left the library.
func (d *DB) DeleteFileStates(hash string) error {
	_, err := d.db.Exec(`DELETE FROM file_state WHERE hash = ?`, strings.ToLower(hash))
	return err
}

// PruneFileStates deletes rows whose hash is not in keep (lower-case) and whose
// last play and eviction are both older than before; the guard spares a file played
// or evicted while the caller built its set. Returns the hashes it removed, so the
// caller can drop its in-memory copies.
func (d *DB) PruneFileStates(keep map[string]bool, before time.Time) ([]string, error) {
	cutoff := before.Unix()
	rows, err := d.db.Query(`
		SELECT hash FROM file_state
		GROUP BY hash HAVING MAX(last_played_at) < ? AND MAX(evicted_at) < ?`, cutoff, cutoff)
	if err != nil {
		return nil, err
	}
	var drop []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if !keep[strings.ToLower(h)] {
			drop = append(drop, h)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(drop) == 0 {
		return nil, nil
	}
	tx, err := d.db.Begin()
	if err != nil {
		return nil, err
	}
	for _, h := range drop {
		if _, err := tx.Exec(`DELETE FROM file_state WHERE hash = ?`, h); err != nil {
			_ = tx.Rollback()
			return nil, fmt.Errorf("prune %s: %w", h, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return drop, nil
}
