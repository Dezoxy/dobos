package metadb

import "time"

// SetMark stamps a named daemon-level time. A stamp older than the stored one never
// moves it back, so concurrent writers and a clock step cannot undo a later stamp.
func (d *DB) SetMark(name string, at time.Time) error {
	_, err := d.db.Exec(`
		INSERT INTO daemon_marks (name, at) VALUES (?, ?)
		ON CONFLICT(name) DO UPDATE SET at = MAX(at, excluded.at)`,
		name, at.Unix())
	return err
}

// LoadMarks returns every mark as Unix seconds.
func (d *DB) LoadMarks() (map[string]int64, error) {
	rows, err := d.db.Query(`SELECT name, at FROM daemon_marks`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]int64{}
	for rows.Next() {
		var name string
		var at int64
		if err := rows.Scan(&name, &at); err != nil {
			return nil, err
		}
		out[name] = at
	}
	return out, rows.Err()
}
