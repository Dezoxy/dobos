package warmup

import (
	"errors"
	"time"
)

// Seed is verified bytes of a file at a file-relative offset.
type Seed struct {
	Off  int64
	Data []byte
}

// SeedSpans writes bytes the caller has verified against the torrent's piece hashes
// into a file's head and tail windows (Dezoxy fork, index seeding from local copies).
// The head takes the contiguous run from offset 0, so it stays dense; the tail takes any
// verified range in its window. Index mode only. It returns the bytes written to each.
func (d *DiskWarmupCache) SeedSpans(hash string, fileID int, size int64, spans []Seed) (head, tail int64, err error) {
	if d == nil || !persistTails {
		return 0, 0, errors.New("index cache not enabled (warmup_dir)")
	}
	if size <= 0 {
		return 0, 0, errors.New("empty file")
	}
	headLen := min(FileSize, size)
	for _, s := range spans {
		if s.Off != 0 {
			continue
		}
		n := min(int64(len(s.Data)), headLen)
		if d.GetAvailableRange(hash, fileID) >= n {
			break
		}
		// The writer returns the buffer to its pool, so it must come from there.
		bufPtr := warmupWritePool.Get().(*[]byte)
		if int64(len(*bufPtr)) < n {
			b := make([]byte, max(int64(warmupWriteBuf), n))
			bufPtr = &b
		}
		copy(*bufPtr, s.Data[:n])
		// Through the single writer, which keeps the head dense; waiting on the queue
		// rather than dropping the write as the streaming path may.
		d.writeCh <- warmupWrite{hash, fileID, bufPtr, int(n), 0}
		path := d.filePath(hash, fileID)
		deadline := time.Now().Add(30 * time.Second)
		for {
			d.sizeCache.Delete(path)
			d.missing.Delete(path)
			if d.GetAvailableRange(hash, fileID) >= n {
				break
			}
			if time.Now().After(deadline) {
				return 0, 0, errors.New("head write did not complete")
			}
			time.Sleep(20 * time.Millisecond)
		}
		head = n
		break
	}

	tailStart := max(size-TailWarmupSize, 0)
	for _, s := range spans {
		a, b := max(s.Off, tailStart), min(s.Off+int64(len(s.Data)), size)
		if a >= b {
			continue
		}
		d.writeTail(hash, fileID, s.Data[a-s.Off:b-s.Off], a, size)
		tail += b - a
	}
	return head, tail, nil
}
