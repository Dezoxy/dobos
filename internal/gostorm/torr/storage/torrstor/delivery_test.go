package torrstor

import (
	"testing"
	"time"
)

// MarkComplete stamps the piece with the time it completed; LastDelivery reports
// the latest stamp over a piece range and how many pieces in it carry one. The stamp
// survives Release (RAM eviction) and MarkNotComplete, and reads 0, 0 after Close.
func TestLastDeliveryStampsCompletionAndSurvivesRelease(t *testing.T) {
	_, cache := newPriorityTestCache(t)

	if at, n := cache.LastDelivery(0, 7); at != 0 || n != 0 {
		t.Fatalf("before any piece completed: %d, %d", at, n)
	}

	before := time.Now().Unix()
	for _, id := range []int{2, 3} {
		if err := cache.pieces[id].MarkComplete(); err != nil {
			t.Fatal(err)
		}
	}
	after := time.Now().Unix()

	at, n := cache.LastDelivery(0, 7)
	if n != 2 || at < before || at > after {
		t.Fatalf("LastDelivery(0,7) = %d, %d; want 2 stamps in [%d,%d]", at, n, before, after)
	}
	if at, n := cache.LastDelivery(3, 5); n != 1 || at == 0 {
		t.Fatalf("LastDelivery(3,5) = %d, %d; want 1 stamp", at, n)
	}
	if at, n := cache.LastDelivery(4, 7); at != 0 || n != 0 {
		t.Fatalf("LastDelivery(4,7) = %d, %d; want none", at, n)
	}

	cache.pieces[2].Release()
	_ = cache.pieces[3].MarkNotComplete()
	if _, n := cache.LastDelivery(0, 7); n != 2 {
		t.Fatalf("stamps after Release and MarkNotComplete: %d, want 2", n)
	}

	if err := cache.Close(); err != nil {
		t.Fatal(err)
	}
	if at, n := cache.LastDelivery(0, 7); at != 0 || n != 0 {
		t.Fatalf("after Close: %d, %d", at, n)
	}
	var nilCache *Cache
	if at, n := nilCache.LastDelivery(0, 7); at != 0 || n != 0 {
		t.Fatalf("nil cache: %d, %d", at, n)
	}
}
