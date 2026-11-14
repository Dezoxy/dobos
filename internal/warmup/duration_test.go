package warmup

import (
	"testing"
	"time"
)

func TestWarmupDurationSnapshotKeepsSum(t *testing.T) {
	for i := range warmupDurationBuckets {
		warmupDurationBuckets[i].Store(0)
	}
	warmupDurationSumMicros.Store(0)

	recordWarmupDuration(1500 * time.Millisecond)
	recordWarmupDuration(500 * time.Microsecond) // sub-millisecond still adds to the sum
	recordWarmupDuration(3 * time.Second)
	recordWarmupDuration(200 * time.Second)

	counts, sum := WarmupDurationSnapshot()
	want := [8]int64{2, 1, 0, 0, 0, 0, 0, 1}
	if counts != want {
		t.Fatalf("counts = %v, want %v", counts, want)
	}
	if sum != 204.5005 {
		t.Fatalf("sum = %v, want 204.5005", sum)
	}
	if len(WarmupDurationBoundsSeconds)+1 != len(counts) {
		t.Fatalf("%d bounds for %d buckets", len(WarmupDurationBoundsSeconds), len(counts))
	}
	// The JSON view keeps its counts.
	if got := WarmupDurationBucketCounts(); got != want {
		t.Fatalf("JSON counts = %v, want %v", got, want)
	}
}
