package torr

import (
	"bytes"
	"crypto/sha1"
	"errors"
	"testing"
)

// Dezoxy fork: index seeding writes only bytes whose pieces were hashed from local copies.

const tpl = 16

// fixture: a 100-byte torrent of three files - id 1 [0,10), id 2 [10,80), id 3 [80,100).
func fixture() ([]byte, []layoutFile, func(int) ([]byte, bool)) {
	data := make([]byte, 100)
	for i := range data {
		data[i] = byte(i*7 + 3)
	}
	hashes := map[int][]byte{}
	for p := 0; p*tpl < len(data); p++ {
		h := sha1.Sum(data[p*tpl : min((p+1)*tpl, len(data))])
		hashes[p] = h[:]
	}
	layout := []layoutFile{{2, 10, 70}, {1, 0, 10}, {3, 80, 20}} // unsorted on purpose
	return data, layout, func(i int) ([]byte, bool) { h, ok := hashes[i]; return h, ok }
}

func cp(b []byte) LocalCopy { return LocalCopy{R: bytes.NewReader(b), Size: int64(len(b))} }

func TestVerifyWindowsAllCopies(t *testing.T) {
	data, layout, hashOf := fixture()
	copies := map[int]LocalCopy{1: cp(data[0:10]), 2: cp(data[10:80]), 3: cp(data[80:100])}
	spans, hashed, err := verifyWindows(layout, tpl, 100, hashOf, copies, 2, [][2]int64{{0, 20}, {50, 70}})
	if err != nil {
		t.Fatal(err)
	}
	// Head [0,20) of file 2 is torrent [10,30): pieces 0 (straddles file 1) and 1.
	// Tail [50,70) is torrent [60,80): pieces 3 and 4.
	if hashed != 4 || len(spans) != 2 {
		t.Fatalf("hashed=%d spans=%d", hashed, len(spans))
	}
	if spans[0].Off != 0 || !bytes.Equal(spans[0].Data, data[10:30]) {
		t.Errorf("head span %d %v", spans[0].Off, spans[0].Data)
	}
	if spans[1].Off != 50 || !bytes.Equal(spans[1].Data, data[60:80]) {
		t.Errorf("tail span %d %v", spans[1].Off, spans[1].Data)
	}
}

func TestVerifyWindowsSkipsPiecesWithoutNeighbourCopy(t *testing.T) {
	data, layout, hashOf := fixture()
	// No copy of file 1: piece 0 cannot be hashed, so the head starts at piece 1.
	copies := map[int]LocalCopy{2: cp(data[10:80])}
	spans, hashed, err := verifyWindows(layout, tpl, 100, hashOf, copies, 2, [][2]int64{{0, 20}})
	if err != nil || hashed != 1 || len(spans) != 1 || spans[0].Off != 6 {
		t.Fatalf("spans=%v hashed=%d err=%v", spans, hashed, err)
	}
}

func TestVerifyWindowsCatchesAnInPlaceEdit(t *testing.T) {
	data, layout, hashOf := fixture()
	edited := append([]byte(nil), data[10:80]...)
	edited[1] ^= 0xff // header byte inside straddling piece 0: same size, other content
	copies := map[int]LocalCopy{1: cp(data[0:10]), 2: cp(edited), 3: cp(data[80:100])}
	if _, _, err := verifyWindows(layout, tpl, 100, hashOf, copies, 2, [][2]int64{{0, 20}}); !errors.Is(err, ErrNotIdentical) {
		t.Fatalf("edited header accepted: %v", err)
	}
	if _, _, err := verifyWindows(layout, tpl, 100, hashOf, map[int]LocalCopy{2: cp(data[10:79])}, 2, [][2]int64{{0, 20}}); !errors.Is(err, ErrNotIdentical) {
		t.Fatalf("size mismatch accepted: %v", err)
	}
}

func TestVerifyWindowsIgnoresAWrongSizedNeighbour(t *testing.T) {
	data, layout, hashOf := fixture()
	copies := map[int]LocalCopy{1: cp(data[0:9]), 2: cp(data[10:80])} // file 1 is another release
	if _, hashed, err := verifyWindows(layout, tpl, 100, hashOf, copies, 2, [][2]int64{{0, 20}}); err != nil || hashed != 1 {
		t.Fatalf("hashed=%d err=%v", hashed, err)
	}
}

func TestMergeSpans(t *testing.T) {
	got := mergeSpans([]Span{{4, []byte{4, 5}}, {0, []byte{0, 1, 2}}, {3, []byte{3, 4}}, {9, []byte{9}}})
	if len(got) != 2 || got[0].Off != 0 || !bytes.Equal(got[0].Data, []byte{0, 1, 2, 3, 4, 5}) || got[1].Off != 9 {
		t.Fatalf("%v", got)
	}
}
