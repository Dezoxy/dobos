package torr

import (
	"bytes"
	"crypto/sha1"
	"errors"
	"fmt"
	"io"
	"sort"
)

// ErrNotIdentical means a local copy is not the torrent's file: a different release,
// or a copy edited since (an in-place header edit keeps the size).
var ErrNotIdentical = errors.New("local copy differs from the torrent's file")

// LocalCopy is one of this torrent's files on a local disk.
type LocalCopy struct {
	R    io.ReaderAt
	Size int64
}

// Span is verified bytes of a file at a file-relative offset.
type Span struct {
	Off  int64
	Data []byte
}

type layoutFile struct {
	id          int
	off, length int64
}

// VerifiedWindows returns the bytes of target's windows (file-relative [start, end))
// that local copies prove (Dezoxy fork, index seeding): every piece a window touches is
// assembled from the copies of all the files it spans and hashed against the torrent,
// and the returned bytes are the hashed ones. A piece reaching into a file with no
// usable copy is skipped, leaving a gap for the swarm; a piece whose hash differs fails
// with ErrNotIdentical. copies is keyed by Status() file id. It also returns how many
// pieces it hashed.
func (t *Torrent) VerifiedWindows(copies map[int]LocalCopy, target int, windows [][2]int64) ([]Span, int, error) {
	if t == nil {
		return nil, 0, errors.New("torrent not active")
	}
	tt := t.Torrent // one snapshot: drop() may nil the field
	if tt == nil || tt.Info() == nil {
		return nil, 0, errors.New("torrent has no metadata")
	}
	info := tt.Info()
	if info.NumPieces() == 0 {
		return nil, 0, errors.New("torrent has no v1 piece hashes")
	}
	byPath := map[string][2]int64{}
	for _, f := range tt.Files() {
		byPath[f.Path()] = [2]int64{f.Offset(), f.Length()}
	}
	var layout []layoutFile
	for _, fs := range t.Status().FileStats {
		if fs == nil {
			continue
		}
		if ol, ok := byPath[fs.Path]; ok {
			layout = append(layout, layoutFile{fs.Id, ol[0], ol[1]})
		}
	}
	hashOf := func(i int) ([]byte, bool) {
		if i < 0 || i >= info.NumPieces() {
			return nil, false
		}
		h := info.Piece(i).Hash()
		return h[:], true
	}
	return verifyWindows(layout, info.PieceLength, info.TotalLength(), hashOf, copies, target, windows)
}

func verifyWindows(layout []layoutFile, pieceLen, total int64, hashOf func(int) ([]byte, bool),
	copies map[int]LocalCopy, target int, windows [][2]int64) ([]Span, int, error) {
	if pieceLen <= 0 {
		return nil, 0, errors.New("invalid piece length")
	}
	sort.Slice(layout, func(i, j int) bool { return layout[i].off < layout[j].off })
	var tf *layoutFile
	usable := map[int]LocalCopy{}
	for i := range layout {
		lf := &layout[i]
		if lf.id == target {
			tf = lf
		}
		if c, ok := copies[lf.id]; ok && c.R != nil && c.Size == lf.length {
			usable[lf.id] = c
		}
	}
	if tf == nil {
		return nil, 0, fmt.Errorf("file %d not found in torrent", target)
	}
	if c, ok := copies[target]; !ok || c.Size != tf.length {
		return nil, 0, fmt.Errorf("%w: size differs from the torrent's %d bytes", ErrNotIdentical, tf.length)
	}

	var spans []Span
	hashed := 0
	done := map[int64]bool{}
	for _, w := range windows {
		a, b := max(w[0], 0), min(w[1], tf.length)
		if b <= a {
			continue
		}
		for p := (tf.off + a) / pieceLen; p <= (tf.off+b-1)/pieceLen; p++ {
			if done[p] {
				continue
			}
			done[p] = true
			ps, pe := p*pieceLen, min((p+1)*pieceLen, total)
			buf := make([]byte, pe-ps)
			complete := true
			for _, lf := range layout {
				s, e := max(ps, lf.off), min(pe, lf.off+lf.length)
				if s >= e {
					continue
				}
				c, ok := usable[lf.id]
				if !ok {
					complete = false // spans a file we have no copy of
					break
				}
				if _, err := c.R.ReadAt(buf[s-ps:e-ps], s-lf.off); err != nil {
					return nil, 0, fmt.Errorf("read file %d for piece %d: %w", lf.id, p, err)
				}
			}
			if !complete {
				continue
			}
			want, ok := hashOf(int(p))
			if !ok {
				continue
			}
			if got := sha1.Sum(buf); !bytes.Equal(got[:], want) {
				return nil, hashed, fmt.Errorf("%w: piece %d", ErrNotIdentical, p)
			}
			hashed++
			fs, fe := max(ps, tf.off), min(pe, tf.off+tf.length)
			for _, w2 := range windows {
				s, e := max(fs, tf.off+w2[0]), min(fe, tf.off+w2[1])
				if s < e {
					spans = append(spans, Span{s - tf.off, buf[s-ps : e-ps]})
				}
			}
		}
	}
	return mergeSpans(spans), hashed, nil
}

// mergeSpans sorts spans and joins overlapping or touching ones.
func mergeSpans(spans []Span) []Span {
	if len(spans) == 0 {
		return nil
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].Off < spans[j].Off })
	out := []Span{{spans[0].Off, append([]byte(nil), spans[0].Data...)}}
	for _, s := range spans[1:] {
		last := &out[len(out)-1]
		end := last.Off + int64(len(last.Data))
		if s.Off > end {
			out = append(out, Span{s.Off, append([]byte(nil), s.Data...)})
			continue
		}
		if sEnd := s.Off + int64(len(s.Data)); sEnd > end {
			last.Data = append(last.Data, s.Data[end-s.Off:]...)
		}
	}
	return out
}
