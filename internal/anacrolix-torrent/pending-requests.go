package torrent

import (
	rbm "github.com/RoaringBitmap/roaring/v2"
	roaring "github.com/RoaringBitmap/roaring/v2/BitSliceIndexing"
)

type pendingRequests struct {
	m *roaring.BSI
}

var allBits rbm.Bitmap

func init() {
	allBits.AddRange(0, rbm.MaxRange)
}
