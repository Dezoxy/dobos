package metadb

// EpisodeGap is an episode a sync removed because its release was dead and nothing
// live could replace it. Nothing records new gaps since the catalogue sync was
// removed; the rows already in the database stay readable through the library's gap
// listing and are cleared when the episode is filed again.
type EpisodeGap struct {
	EpisodeKey string
	ShowIMDB   string
	Season     int
	FilePath   string
	DeadHash   string
	RemovedAt  int64
	// LastAttempt is when a repair pass last tried this hole. Zero means never.
	LastAttempt int64
}

// ClearEpisodeGap drops the record once the episode is back.
func (d *DB) ClearEpisodeGap(episodeKey string) error {
	_, err := d.db.Exec(`DELETE FROM episode_gaps WHERE episode_key = ?`, episodeKey)
	return err
}

// EpisodeGaps returns every hole still open, least recently tried first: a gap never
// attempted sorts by when it was created, one already tried by when it was tried.
func (d *DB) EpisodeGaps() ([]EpisodeGap, error) {
	rows, err := d.db.Query(
		`SELECT episode_key, COALESCE(show_imdb, ''), season, file_path, dead_hash, removed_at,
		        COALESCE(last_attempt, 0)
		 FROM episode_gaps
		 ORDER BY MAX(COALESCE(last_attempt, 0), removed_at), episode_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EpisodeGap
	for rows.Next() {
		var g EpisodeGap
		if err := rows.Scan(&g.EpisodeKey, &g.ShowIMDB, &g.Season, &g.FilePath, &g.DeadHash, &g.RemovedAt, &g.LastAttempt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}
