package requestStrategy

import (
	"slices"
	"testing"
)

func TestAjwernerBtreeOrderDeleteScan(t *testing.T) {
	bt := NewAjwernerBtree()
	// Lower availability sorts first, so the order differs from insertion and index order.
	items := []pieceRequestOrderItem{
		{key: PieceRequestOrderKey{Index: 0}, state: PieceRequestOrderState{Availability: 2}},
		{key: PieceRequestOrderKey{Index: 1}, state: PieceRequestOrderState{Availability: 1}},
		{key: PieceRequestOrderKey{Index: 2}, state: PieceRequestOrderState{Availability: 0}},
	}
	for _, item := range items {
		bt.Add(item)
	}
	scan := func(limit int) (ret []int) {
		bt.Scan(func(item pieceRequestOrderItem) bool {
			ret = append(ret, item.key.Index)
			return len(ret) < limit
		})
		return
	}
	check := func(got, want []int) {
		t.Helper()
		if !slices.Equal(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	check(scan(10), []int{2, 1, 0})
	check(scan(2), []int{2, 1}) // returning false stops the scan
	bt.Delete(items[1])
	check(scan(10), []int{2, 0})
	mustPanic := func(name string, f func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Fatalf("%s: expected panic", name)
			}
		}()
		f()
	}
	mustPanic("delete missing", func() { bt.Delete(items[1]) })
	mustPanic("add duplicate", func() { bt.Add(items[0]) })
}
