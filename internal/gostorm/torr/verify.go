package torr

import "github.com/anacrolix/torrent"

// RangeVerified reports whether every piece covering [off, off+n) of file fileID has
// passed its checksum (Dezoxy fork). fileID is the 1-based id Status() gives the file,
// numbered over the files sorted by path.
func (t *Torrent) RangeVerified(fileID int, off, n int64) bool {
	tt, first, last, ok := t.rangePieces(fileID, off, n)
	if !ok {
		return false
	}
	for i := first; i <= last; i++ {
		if !tt.PieceState(i).Complete {
			return false
		}
	}
	return true
}

// VerifiedPieces counts the pieces covering [off, off+n) of file fileID that have passed
// their checksum, and how many pieces cover it (Dezoxy fork). Piece storage is RAM only,
// so a piece that was not complete and is now came from the swarm: warmup compares the
// count before and after a fill chunk to tell delivery from bytes the engine already held.
// A missing torrent or an unresolvable range counts as 0, 0.
func (t *Torrent) VerifiedPieces(fileID int, off, n int64) (verified, total int) {
	tt, first, last, ok := t.rangePieces(fileID, off, n)
	if !ok {
		return 0, 0
	}
	for i := first; i <= last; i++ {
		total++
		if tt.PieceState(i).Complete {
			verified++
		}
	}
	return verified, total
}

// RequestRange asks the swarm for every whole piece covering [off, off+n) of file
// fileID. A scan reads part of a piece, and a piece is hashed only once all of it,
// including any part in a neighbouring file, has arrived: without this the rest is never
// requested and the block never verifies (Dezoxy fork).
func (t *Torrent) RequestRange(fileID int, off, n int64) {
	if tt, first, last, ok := t.rangePieces(fileID, off, n); ok {
		requestIncomplete(tt, first, last)
	}
}

// requestIncomplete raises the incomplete pieces of [first, last] to Normal, one
// DownloadPieces call per contiguous run. DownloadPieces raises complete pieces too, and a
// priority on a complete piece is never cleared (only MarkComplete or RAM eviction do),
// while the engine spends its storage cap on it.
func requestIncomplete(tt *torrent.Torrent, first, last int) {
	for _, r := range incompleteRuns(first, last, func(i int) bool { return tt.PieceState(i).Complete }) {
		tt.DownloadPieces(r[0], r[1])
	}
}

// incompleteRuns returns the contiguous runs [begin, end) of pieces in [first, last] for
// which complete is false.
func incompleteRuns(first, last int, complete func(int) bool) (runs [][2]int) {
	for i := first; i <= last; i++ {
		if complete(i) {
			continue
		}
		if n := len(runs); n > 0 && runs[n-1][1] == i {
			runs[n-1][1] = i + 1
		} else {
			runs = append(runs, [2]int{i, i + 1})
		}
	}
	return runs
}

// rangePieces resolves a file range to its pieces on one snapshot of the torrent.
func (t *Torrent) rangePieces(fileID int, off, n int64) (*torrent.Torrent, int, int, bool) {
	if n <= 0 || off < 0 {
		return nil, 0, 0, false
	}
	tt, f, ok := t.fileByID(fileID)
	if !ok || off+n > f.Length() {
		return nil, 0, 0, false
	}
	first, last, ok := pieceSpan(f.Offset()+off, n, tt.Info().PieceLength)
	return tt, first, last, ok
}

// fileByID resolves a Status() file id on one snapshot of the torrent.
func (t *Torrent) fileByID(fileID int) (*torrent.Torrent, *torrent.File, bool) {
	if t == nil {
		return nil, nil, false
	}
	tt := t.Torrent // drop() may nil the field; use one snapshot throughout
	if tt == nil || tt.Info() == nil {
		return nil, nil, false
	}
	// Resolve the id through Status() and match by path, exactly as Stream does: ids
	// come from Status()'s cached order, which a fresh sort need not reproduce.
	var path string
	for _, fs := range t.Status().FileStats {
		if fs != nil && fs.Id == fileID {
			path = fs.Path
			break
		}
	}
	if path == "" {
		return nil, nil, false
	}
	for _, tf := range tt.Files() {
		if tf.Path() == path {
			return tt, tf, true
		}
	}
	return nil, nil, false
}

// pieceSpan returns the first and last piece covering n bytes at torrent offset off.
func pieceSpan(off, n, pieceLen int64) (first, last int, ok bool) {
	if pieceLen <= 0 || n <= 0 || off < 0 {
		return 0, 0, false
	}
	return int(off / pieceLen), int((off + n - 1) / pieceLen), true
}
