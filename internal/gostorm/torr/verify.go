package torr

import "github.com/anacrolix/torrent"

// RangeVerified reports whether every piece covering [off, off+n) of file fileID has
// passed its checksum (Dezoxy fork). fileID is the 1-based id Status() gives the file,
// numbered over the files sorted by path.
func (t *Torrent) RangeVerified(fileID int, off, n int64) bool {
	if t == nil || n <= 0 || off < 0 {
		return false
	}
	tt := t.Torrent // drop() may nil the field; use one snapshot throughout
	if tt == nil || tt.Info() == nil {
		return false
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
		return false
	}
	var f *torrent.File
	for _, tf := range tt.Files() {
		if tf.Path() == path {
			f = tf
			break
		}
	}
	if f == nil || off+n > f.Length() {
		return false
	}
	first, last, ok := pieceSpan(f.Offset()+off, n, tt.Info().PieceLength)
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

// pieceSpan returns the first and last piece covering n bytes at torrent offset off.
func pieceSpan(off, n, pieceLen int64) (first, last int, ok bool) {
	if pieceLen <= 0 || n <= 0 || off < 0 {
		return 0, 0, false
	}
	return int(off / pieceLen), int((off + n - 1) / pieceLen), true
}
