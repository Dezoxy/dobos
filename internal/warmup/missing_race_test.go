package warmup

import (
	"bytes"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A reader that polls a head while it is created must never leave the negative cache
// hiding the finished file: the reader's stat can fail just before the writer creates
// the file, and its missing entry then lands after the writer cleared it.
func TestGetAvailableRangeDoesNotHideAHeadCreatedDuringTheMiss(t *testing.T) {
	d := indexCache(t, t.TempDir())
	const n = 4 << 10
	data := bytes.Repeat([]byte{7}, n)

	var stop atomic.Bool
	var wg sync.WaitGroup
	var hash atomic.Value
	hash.Store("")
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				if h := hash.Load().(string); h != "" {
					d.GetAvailableRange(h, 1)
				}
			}
		}()
	}
	defer func() { stop.Store(true); wg.Wait() }()

	for i := range 3000 {
		h := "h" + strconv.Itoa(i)
		hash.Store(h)
		d.processWrite(h, 1, data, 0)
		// A concurrent reader may show its miss for an instant before it looks again, so
		// give the file a moment; the bug hid it for missingTTL (10 s).
		deadline := time.Now().Add(time.Second)
		for d.GetAvailableRange(h, 1) != n {
			if time.Now().After(deadline) {
				t.Fatalf("iteration %d: the negative cache still hides the finished head after 1s", i)
			}
			time.Sleep(time.Millisecond)
		}
	}
}
