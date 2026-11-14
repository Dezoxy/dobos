package warmup

import (
	"bytes"
	"errors"
	"math/rand"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// Dezoxy fork: the fill primitive (Readiness step 2), tested against a fake swarm.

// fakeSwarm is a torrent in memory. Pieces in arrive come from peers, but only after a
// reader asked for them (RequestPieces) and then read (TailFetch), as in the engine;
// pieces in have are verified already; the rest never arrive. A read of an unverified
// piece returns garbage, as the engine returns bytes before they are hashed.
type fakeSwarm struct {
	mu       sync.Mutex
	data     []byte
	pieceLen int64
	have     map[int]bool
	arrive   map[int]bool
	asked    map[int]bool
	fetches  int
	// slow: an asked piece needs two reads to complete. dropAsked: a read clears the
	// request on the pieces it leaves incomplete, as closing the engine's reader does.
	slow, dropAsked bool
	progress        map[int]int
	requests        [][2]int64 // off, n of every RequestPieces

	onRequest func(call int) // runs before the call is recorded as done
}

func newSwarm(size int64) *fakeSwarm {
	s := &fakeSwarm{data: make([]byte, size), pieceLen: mb, have: map[int]bool{}, arrive: map[int]bool{}, asked: map[int]bool{}, progress: map[int]int{}}
	rand.New(rand.NewSource(1)).Read(s.data)
	return s
}

func (s *fakeSwarm) span(off, n int64) (first, last int) {
	return int(off / s.pieceLen), int((off + n - 1) / s.pieceLen)
}

// allArrive makes every piece come from peers.
func (s *fakeSwarm) allArrive() *fakeSwarm {
	for i := 0; i < len(s.data)/int(s.pieceLen)+1; i++ {
		s.arrive[i] = true
	}
	return s
}

func (s *fakeSwarm) verified(off, n int64) bool {
	f, l := s.span(off, n)
	for i := f; i <= l; i++ {
		if !s.have[i] {
			return false
		}
	}
	return true
}

func (s *fakeSwarm) request(off, n int64) {
	s.mu.Lock()
	call := len(s.requests) + 1
	hook := s.onRequest
	s.mu.Unlock()
	if hook != nil {
		hook(call)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, [2]int64{off, n})
	f, l := s.span(off, n)
	for i := f; i <= l; i++ {
		s.asked[i] = true
	}
}

func (s *fakeSwarm) fetch(off int64, buf []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fetches++
	n := int64(len(buf))
	if off+n > int64(len(s.data)) {
		n = int64(len(s.data)) - off
	}
	if n <= 0 {
		return 0, nil
	}
	f, l := s.span(off, n)
	for i := f; i <= l; i++ {
		if s.asked[i] && s.arrive[i] {
			s.progress[i]++
			if !s.slow || s.progress[i] >= 2 {
				s.have[i] = true
			}
		}
	}
	if s.dropAsked {
		defer func() {
			for i := f; i <= l; i++ {
				if !s.have[i] {
					delete(s.asked, i)
				}
			}
		}()
	}
	copy(buf, s.data[off:off+n])
	for i := f; i <= l; i++ {
		if !s.have[i] { // garbage for the unverified piece
			lo, hi := int64(i)*s.pieceLen-off, int64(i+1)*s.pieceLen-off
			if lo < 0 {
				lo = 0
			}
			if hi > n {
				hi = n
			}
			for j := lo; j < hi; j++ {
				buf[j] = 0xEE
			}
		}
	}
	return int(n), nil
}

func (s *fakeSwarm) count(off, n int64) (verified, total int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, l := s.span(off, n)
	for i := f; i <= l; i++ {
		total++
		if s.have[i] {
			verified++
		}
	}
	return
}

func (s *fakeSwarm) fetchCount() int { s.mu.Lock(); defer s.mu.Unlock(); return s.fetches }
func (s *fakeSwarm) requestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

const fillFile = 32 * mb // the file under test: 9 MiB head window, 5 MiB tail window

// fillEnv is an evicting cache with the windows of fillFile and the fake swarm wired in.
func fillEnv(t *testing.T, s *fakeSwarm) *DiskWarmupCache {
	t.Helper()
	d := evictEnv(t)
	oldPoll, oldChunk, oldFile := verifyPollInterval, fillChunkTimeout, fillFileTimeout
	oldReq, oldPieces, oldState := RequestPieces, PiecesVerified, OnWarmupStateChange
	t.Cleanup(func() {
		verifyPollInterval, fillChunkTimeout, fillFileTimeout = oldPoll, oldChunk, oldFile
		RequestPieces, PiecesVerified, OnWarmupStateChange = oldReq, oldPieces, oldState
	})
	FileSize, TailWarmupSize = 9*mb, 5*mb
	verifyPollInterval, fillChunkTimeout, fillFileTimeout = time.Millisecond, 2*time.Second, 10*time.Second
	RangeVerified = func(_ string, _ int, off, n int64) bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.verified(off, n)
	}
	RequestPieces = func(_ string, _ int, off, n int64) { s.request(off, n) }
	TailFetch = func(_ string, _ int, off int64, buf []byte) (int, error) { return s.fetch(off, buf) }
	PiecesVerified = func(_ string, _ int, off, n int64) (int, int) { return s.count(off, n) }
	OnWarmupStateChange = func(string, int, bool) { t.Error("a fill must not fire OnWarmupStateChange") }
	return d
}

var fillKey = FileKey{"fill", 1}

func fillReq() FillRequest {
	return FillRequest{Key: fillKey, FileSize: fillFile, Policy: EvictNeverPlayed}
}

// onDisk returns the head file and the covered tail bytes of fillKey.
func onDisk(t *testing.T, d *DiskWarmupCache) (head []byte) {
	t.Helper()
	b, err := os.ReadFile(d.filePath(fillKey.Hash, fillKey.FileIdx))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return b
}

func readTailWindow(t *testing.T, d *DiskWarmupCache) []byte {
	t.Helper()
	buf := make([]byte, TailWarmupSize)
	n, _ := d.ReadTail(fillKey.Hash, fillKey.FileIdx, buf, fillFile-TailWarmupSize, fillFile)
	return buf[:n]
}

func TestFillWritesBothWindowsFromTheSwarm(t *testing.T) {
	s := newSwarm(fillFile).allArrive()
	d := fillEnv(t, s)
	res, err := d.FillWindows(fillReq())
	if err != nil {
		t.Fatal(err)
	}
	if res.HeadBytes != 9*mb || res.TailBytes != 5*mb || res.AlreadyWarm || res.Evicted != 0 {
		t.Fatalf("result = %+v", res)
	}
	if res.DeliveredPieces != 14 {
		t.Fatalf("delivered %d pieces, want 14 (9 head + 5 tail)", res.DeliveredPieces)
	}
	if !bytes.Equal(onDisk(t, d), s.data[:9*mb]) {
		t.Fatal("head bytes differ from the torrent")
	}
	if !d.TailReady(fillKey.Hash, fillKey.FileIdx) || !bytes.Equal(readTailWindow(t, d), s.data[fillFile-5*mb:]) {
		t.Fatal("tail missing or differs from the torrent")
	}
	// 4+4+1 MiB of head and 4+1 MiB of tail: five requests, none beyond a chunk.
	if got := s.requestCount(); got != 5 {
		t.Fatalf("%d piece requests, want 5", got)
	}
	for _, r := range s.requests {
		if r[1] > fillChunk {
			t.Fatalf("request of %d bytes: a window must never be asked for whole", r[1])
		}
	}
}

func TestFillSkipsAWarmFile(t *testing.T) {
	s := newSwarm(fillFile).allArrive()
	d := fillEnv(t, s)
	if _, err := d.FillWindows(fillReq()); err != nil {
		t.Fatal(err)
	}
	fetches, reqs := s.fetchCount(), s.requestCount()
	res, err := d.FillWindows(fillReq())
	if err != nil || !res.AlreadyWarm || res.HeadBytes != 0 || res.TailBytes != 0 || res.DeliveredPieces != 0 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if s.fetchCount() != fetches || s.requestCount() != reqs {
		t.Fatal("a warm file was fetched again")
	}
}

func TestFillOfAnAlreadyVerifiedTorrentDeliversNothing(t *testing.T) {
	s := newSwarm(fillFile) // every piece held: nothing arrives from peers
	for i := 0; i < 32; i++ {
		s.have[i] = true
	}
	d := fillEnv(t, s)
	res, err := d.FillWindows(fillReq())
	if err != nil {
		t.Fatal(err)
	}
	if got := s.requestCount(); got != 0 {
		t.Fatalf("%d piece requests for chunks that already verified: a request raises a priority nothing clears", got)
	}
	if res.DeliveredPieces != 0 {
		t.Fatalf("delivered %d, want 0: RAM-held pieces are not swarm delivery", res.DeliveredPieces)
	}
	if res.HeadBytes != 9*mb || res.TailBytes != 5*mb || !bytes.Equal(onDisk(t, d), s.data[:9*mb]) {
		t.Fatalf("windows not written: %+v", res)
	}
}

func TestFillCountsOnlyThePiecesThatArrived(t *testing.T) {
	s := newSwarm(fillFile)
	for i := 0; i < 32; i++ {
		s.have[i] = true
	}
	// Three head pieces and one tail piece were not held and come from peers.
	for _, i := range []int{1, 5, 8, 29} {
		s.have[i] = false
		s.arrive[i] = true
	}
	d := fillEnv(t, s)
	res, err := d.FillWindows(fillReq())
	if err != nil {
		t.Fatal(err)
	}
	if res.DeliveredPieces != 4 {
		t.Fatalf("delivered %d, want 4", res.DeliveredPieces)
	}
}

func TestFillWritesNothingUnverified(t *testing.T) {
	s := newSwarm(fillFile).allArrive()
	delete(s.arrive, 5) // second head chunk (pieces 4-7) never completes
	d := fillEnv(t, s)
	fillChunkTimeout = 60 * time.Millisecond
	res, err := d.FillWindows(fillReq())
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	head := onDisk(t, d)
	if int64(len(head)) != 4*mb || res.HeadBytes != 4*mb || !bytes.Equal(head, s.data[:4*mb]) {
		t.Fatalf("head = %d bytes (result %d): only the verified chunk may be on disk", len(head), res.HeadBytes)
	}
	if bytes.Contains(head, []byte{0xEE, 0xEE, 0xEE, 0xEE}) {
		t.Fatal("unverified bytes reached the disk")
	}
	if res.TailBytes != 0 || exists(d.tailPath(fillKey.Hash, fillKey.FileIdx)) {
		t.Fatal("the tail must not start after the head failed")
	}
}

func TestFillTimesOutPerFile(t *testing.T) {
	s := newSwarm(fillFile).allArrive()
	delete(s.arrive, 0)
	d := fillEnv(t, s)
	fillFileTimeout = 40 * time.Millisecond // below the chunk limit: the file limit fires
	if _, err := d.FillWindows(fillReq()); !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	if exists(d.filePath(fillKey.Hash, fillKey.FileIdx)) {
		t.Fatal("a head was created without a verified byte")
	}
}

func TestFillResumesFromAPartialHead(t *testing.T) {
	s := newSwarm(fillFile).allArrive()
	d := fillEnv(t, s)
	// A scan wrote the first 4 MiB earlier.
	d.writeHeadLocked(d.filePath(fillKey.Hash, fillKey.FileIdx), 0, s.data[:4*mb])
	res, err := d.FillWindows(fillReq())
	if err != nil {
		t.Fatal(err)
	}
	if res.HeadBytes != 5*mb {
		t.Fatalf("HeadBytes = %d, want the 5 MiB that were missing", res.HeadBytes)
	}
	for _, r := range s.requests {
		if r[0] < 4*mb { // below the resume point (the tail starts at 27 MiB)
			t.Fatalf("request at %d re-fetched the head that was already there", r[0])
		}
	}
	if !bytes.Equal(onDisk(t, d), s.data[:9*mb]) {
		t.Fatal("resumed head differs from the torrent")
	}
}

func TestFillOfAShortFile(t *testing.T) {
	const small = 3 * mb // smaller than both windows
	s := newSwarm(small).allArrive()
	d := fillEnv(t, s)
	res, err := d.FillWindows(FillRequest{Key: fillKey, FileSize: small, Policy: EvictNone})
	if err != nil {
		t.Fatal(err)
	}
	if res.HeadBytes != small || res.TailBytes != small {
		t.Fatalf("result = %+v", res)
	}
	if !d.TailReady(fillKey.Hash, fillKey.FileIdx) || !bytes.Equal(onDisk(t, d), s.data) {
		t.Fatal("short file not warm")
	}
	for _, r := range s.requests {
		if r[0]+r[1] > small {
			t.Fatalf("request %v beyond the file", r)
		}
	}
	if again, err := d.FillWindows(FillRequest{Key: fillKey, FileSize: small, Policy: EvictNone}); err != nil || !again.AlreadyWarm {
		t.Fatalf("second call: %+v %v", again, err)
	}
}

// crowded lays out three media files of 400 MiB (head 300, tail 100) so the quota is
// 176 MiB over; a fill reserving 14 MiB then needs exactly one victim.
func crowded(t *testing.T, d *DiskWarmupCache, ages map[string]time.Duration) {
	t.Helper()
	for stem, age := range ages {
		sparseFile(t, d.dir+"/"+stem+warmupSuffix, 300*mb, age)
		sparseFile(t, d.dir+"/"+stem+tailSuffix, 100*mb, age)
	}
	setTotal(d, 1200*mb)
}

func TestFillReservesRoomBeforeAnyFetch(t *testing.T) {
	for _, policy := range []EvictPolicy{EvictNone, EvictNeverPlayed} {
		s := newSwarm(fillFile).allArrive()
		d := fillEnv(t, s)
		crowded(t, d, map[string]time.Duration{"a-1": 3 * time.Hour, "b-1": 2 * time.Hour, "c-1": time.Hour})
		IsPlayed = func(FileKey) bool { return true } // nothing may be evicted
		var evicted int
		OnEvicted = func(ev []Eviction) { evicted += len(ev) }
		req := fillReq()
		req.Policy = policy
		res, err := d.FillWindows(req)
		if !errors.Is(err, ErrNoRoom) {
			t.Fatalf("policy %d: err = %v, want ErrNoRoom", policy, err)
		}
		if s.fetchCount() != 0 || s.requestCount() != 0 {
			t.Fatalf("policy %d: %d fetches, %d requests before ErrNoRoom", policy, s.fetchCount(), s.requestCount())
		}
		if res.HeadBytes != 0 || res.TailBytes != 0 || evicted != 0 {
			t.Fatalf("policy %d: result %+v, evicted %d", policy, res, evicted)
		}
		for _, stem := range []string{"a-1", "b-1", "c-1"} {
			if !exists(d.dir + "/" + stem + warmupSuffix) {
				t.Fatalf("policy %d: %s lost", policy, stem)
			}
		}
	}
}

func TestFillEvictsOnlyNeverPlayedOldestFirst(t *testing.T) {
	s := newSwarm(fillFile).allArrive()
	d := fillEnv(t, s)
	// c is the oldest of all but played; a is the oldest never-played.
	crowded(t, d, map[string]time.Duration{"a-1": 2 * time.Hour, "b-1": time.Hour, "c-1": 3 * time.Hour})
	IsPlayed = func(k FileKey) bool { return k.Hash == "c" }
	var got []Eviction
	OnEvicted = func(ev []Eviction) { got = append(got, ev...) }
	req := fillReq()
	req.Trigger = TriggerWarmAdd
	res, err := d.FillWindows(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Evicted != 1 || len(got) != 1 || got[0].Key.Hash != "a" || got[0].Trigger != TriggerWarmAdd || got[0].Played {
		t.Fatalf("evicted %d: %+v", res.Evicted, got)
	}
	if exists(d.dir+"/a-1"+warmupSuffix) || exists(d.dir+"/a-1"+tailSuffix) {
		t.Fatal("a survived")
	}
	for _, stem := range []string{"b-1", "c-1"} {
		if !exists(d.dir+"/"+stem+warmupSuffix) || !exists(d.dir+"/"+stem+tailSuffix) {
			t.Fatalf("%s was evicted", stem)
		}
	}
}

func TestFillRespectsTheProtectSet(t *testing.T) {
	s := newSwarm(fillFile).allArrive()
	d := fillEnv(t, s)
	crowded(t, d, map[string]time.Duration{"a-1": 2 * time.Hour, "b-1": time.Hour, "c-1": 3 * time.Hour})
	IsPlayed = func(k FileKey) bool { return k.Hash == "c" }
	req := fillReq()
	req.Protect = func(k FileKey) bool { return k.Hash == "a" }
	if _, err := d.FillWindows(req); err != nil {
		t.Fatal(err)
	}
	if !exists(d.dir+"/a-1"+warmupSuffix) || exists(d.dir+"/b-1"+warmupSuffix) {
		t.Fatal("the protected file must stay and the next never-played one must go")
	}
}

func TestFillEvictionIsAllOrNothing(t *testing.T) {
	s := newSwarm(fillFile).allArrive()
	d := fillEnv(t, s)
	crowded(t, d, map[string]time.Duration{"a-1": 2 * time.Hour, "b-1": time.Hour, "c-1": 3 * time.Hour})
	IsPlayed = func(k FileKey) bool { return k.Hash == "c" }
	req := fillReq()
	req.Protect = func(k FileKey) bool { return k.Hash != "c" } // only the played file is left
	if _, err := d.FillWindows(req); !errors.Is(err, ErrNoRoom) {
		t.Fatalf("err = %v, want ErrNoRoom", err)
	}
	for _, stem := range []string{"a-1", "b-1", "c-1"} {
		if !exists(d.dir+"/"+stem+warmupSuffix) || !exists(d.dir+"/"+stem+tailSuffix) {
			t.Fatalf("%s was evicted although the room could not be made", stem)
		}
	}
	if s.fetchCount() != 0 {
		t.Fatal("fetched without room")
	}
}

func TestFillStopsBetweenChunks(t *testing.T) {
	s := newSwarm(fillFile).allArrive()
	d := fillEnv(t, s)
	req := fillReq()
	// Stop is polled before every chunk and while one waits; it turns true once the
	// first chunk is on disk.
	req.Stop = func() bool { return exists(d.filePath(fillKey.Hash, fillKey.FileIdx)) }
	res, err := d.FillWindows(req)
	if !errors.Is(err, ErrStopped) {
		t.Fatalf("err = %v, want ErrStopped", err)
	}
	if res.HeadBytes != 4*mb || int64(len(onDisk(t, d))) != 4*mb {
		t.Fatalf("result %+v: the first chunk should be written and no more", res)
	}
}

func TestFillEvictedUnderUsNeverRecreatesTheHead(t *testing.T) {
	s := newSwarm(fillFile).allArrive()
	d := fillEnv(t, s)
	s.onRequest = func(call int) {
		if call == 3 { // third head chunk: 8 MiB were written, now the files go
			d.removeHashFiles(fillKey.Hash)
		}
	}
	res, err := d.FillWindows(fillReq())
	if !errors.Is(err, ErrEvicted) {
		t.Fatalf("err = %v, want ErrEvicted", err)
	}
	if res.HeadBytes != 8*mb {
		t.Fatalf("HeadBytes = %d, want what this fill wrote", res.HeadBytes)
	}
	if exists(d.filePath(fillKey.Hash, fillKey.FileIdx)) || exists(d.tailPath(fillKey.Hash, fillKey.FileIdx)) {
		t.Fatal("the fill re-created a file that was evicted under it")
	}
}

func TestFillEvictedUnderUsNeverRecreatesTheTail(t *testing.T) {
	s := newSwarm(fillFile).allArrive()
	d := fillEnv(t, s)
	s.onRequest = func(call int) {
		if call == 5 { // second tail chunk: 4 MiB of tail were written
			d.removeHashFiles(fillKey.Hash)
		}
	}
	if _, err := d.FillWindows(fillReq()); !errors.Is(err, ErrEvicted) {
		t.Fatalf("err = %v, want ErrEvicted", err)
	}
	if exists(d.tailPath(fillKey.Hash, fillKey.FileIdx)) {
		t.Fatal("the fill re-created a tail that was evicted under it")
	}
}

func TestFillNeedsVerificationAndFetchWired(t *testing.T) {
	s := newSwarm(fillFile).allArrive()
	d := fillEnv(t, s)
	RangeVerified = nil
	if _, err := d.FillWindows(fillReq()); !errors.Is(err, ErrNotWired) {
		t.Fatalf("err = %v, want ErrNotWired: never write what cannot be verified", err)
	}
	if s.fetchCount() != 0 || exists(d.filePath(fillKey.Hash, fillKey.FileIdx)) {
		t.Fatal("something was fetched or written")
	}
}

func TestFillAsksAgainWhenTheReaderClosedWithoutVerifying(t *testing.T) {
	s := newSwarm(fillFile).allArrive()
	s.slow, s.dropAsked = true, true // each piece needs a request that survives two reads
	d := fillEnv(t, s)
	res, err := d.FillWindows(fillReq())
	if err != nil {
		t.Fatalf("err = %v: the request was not repeated after the first read", err)
	}
	if res.HeadBytes != 9*mb || res.TailBytes != 5*mb || !bytes.Equal(onDisk(t, d), s.data[:9*mb]) {
		t.Fatalf("result = %+v", res)
	}
}

func TestFillEvictedDuringTheFirstTailChunk(t *testing.T) {
	s := newSwarm(fillFile).allArrive()
	d := fillEnv(t, s)
	s.onRequest = func(call int) {
		if call == 4 { // first tail chunk: the tail has no floor yet
			d.removeHashFiles(fillKey.Hash)
		}
	}
	res, err := d.FillWindows(fillReq())
	if !errors.Is(err, ErrEvicted) {
		t.Fatalf("err = %v, want ErrEvicted (result %+v)", err, res)
	}
	if exists(d.tailPath(fillKey.Hash, fillKey.FileIdx)) || exists(d.filePath(fillKey.Hash, fillKey.FileIdx)) {
		t.Fatal("the fill re-created a file that was evicted under it")
	}
}

func TestFillRefusesEvictAnyAndAnEmptyHash(t *testing.T) {
	s := newSwarm(fillFile).allArrive()
	d := fillEnv(t, s)
	req := fillReq()
	req.Policy = EvictAny
	if _, err := d.FillWindows(req); err == nil {
		t.Fatal("EvictAny accepted: it would evict played files")
	}
	req = fillReq()
	req.Key.Hash = ""
	if _, err := d.FillWindows(req); err == nil {
		t.Fatal("empty hash accepted")
	}
	if s.fetchCount() != 0 {
		t.Fatal("fetched for a refused request")
	}
}

func TestFillStopsWhileWaitingForPieces(t *testing.T) {
	s := newSwarm(fillFile).allArrive()
	delete(s.arrive, 0) // the first chunk never verifies
	d := fillEnv(t, s)
	req := fillReq()
	req.Stop = func() bool { return s.fetchCount() >= 3 }
	if _, err := d.FillWindows(req); !errors.Is(err, ErrStopped) {
		t.Fatalf("err = %v, want ErrStopped long before the chunk limit", err)
	}
}

func TestFillTimeoutKeepsTheLastFetchError(t *testing.T) {
	s := newSwarm(fillFile) // nothing arrives
	d := fillEnv(t, s)
	fillChunkTimeout = 30 * time.Millisecond
	TailFetch = func(string, int, int64, []byte) (int, error) { return 0, errors.New("torrent dropped") }
	_, err := d.FillWindows(fillReq())
	if !errors.Is(err, ErrTimeout) || !strings.Contains(err.Error(), "torrent dropped") {
		t.Fatalf("err = %v, want ErrTimeout carrying the fetch error", err)
	}
}

// A removal on another goroutine while the fill runs: -race checks the lock order, and
// whatever the interleaving the fill ends with success or one of its own errors.
func TestFillBesideRemovals(t *testing.T) {
	s := newSwarm(fillFile).allArrive()
	d := fillEnv(t, s)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				d.removeHashFiles(fillKey.Hash)
				time.Sleep(time.Millisecond)
			}
		}
	}()
	_, err := d.FillWindows(fillReq())
	close(stop)
	<-done
	if err != nil && !errors.Is(err, ErrEvicted) {
		t.Fatalf("err = %v", err)
	}
}

// RoomOnly is the pre-flight of catch-up (Readiness step 3): it answers "already warm"
// and "is there room" and does nothing else.
func TestFillRoomOnlyNeverFetchesOrRequests(t *testing.T) {
	s := newSwarm(fillFile).allArrive()
	d := fillEnv(t, s)
	req := fillReq()
	req.Policy, req.RoomOnly = EvictNone, true
	res, err := d.FillWindows(req)
	if err != nil || res.AlreadyWarm || res.HeadBytes != 0 || res.TailBytes != 0 || res.DeliveredPieces != 0 {
		t.Fatalf("room available: res=%+v err=%v, want a zero result and nil", res, err)
	}
	if s.fetchCount() != 0 || s.requestCount() != 0 {
		t.Fatalf("%d fetches, %d requests: RoomOnly must not touch the swarm", s.fetchCount(), s.requestCount())
	}
	if len(onDisk(t, d)) != 0 {
		t.Fatal("RoomOnly wrote a head")
	}
}

func TestFillRoomOnlyReportsAWarmFileAndFetchesNothing(t *testing.T) {
	s := newSwarm(fillFile).allArrive()
	d := fillEnv(t, s)
	if _, err := d.FillWindows(fillReq()); err != nil {
		t.Fatal(err)
	}
	fetches, reqs := s.fetchCount(), s.requestCount()
	req := fillReq()
	req.Policy, req.RoomOnly = EvictNone, true
	res, err := d.FillWindows(req)
	if err != nil || !res.AlreadyWarm {
		t.Fatalf("res=%+v err=%v, want AlreadyWarm", res, err)
	}
	if s.fetchCount() != fetches || s.requestCount() != reqs {
		t.Fatal("RoomOnly fetched for a warm file")
	}
}

func TestFillRoomOnlyWithEvictNoneReportsNoRoomAndEvictsNothing(t *testing.T) {
	s := newSwarm(fillFile).allArrive()
	d := fillEnv(t, s)
	// Nothing is played, so an evicting policy could make room; EvictNone must not.
	crowded(t, d, map[string]time.Duration{"a-1": 3 * time.Hour, "b-1": 2 * time.Hour, "c-1": time.Hour})
	var evicted int
	OnEvicted = func(ev []Eviction) { evicted += len(ev) }
	req := fillReq()
	req.Policy, req.RoomOnly = EvictNone, true
	res, err := d.FillWindows(req)
	if !errors.Is(err, ErrNoRoom) {
		t.Fatalf("err = %v, want ErrNoRoom", err)
	}
	if res.Evicted != 0 || evicted != 0 || s.fetchCount() != 0 || s.requestCount() != 0 {
		t.Fatalf("res=%+v evicted=%d fetches=%d requests=%d", res, evicted, s.fetchCount(), s.requestCount())
	}
	for _, stem := range []string{"a-1", "b-1", "c-1"} {
		if !exists(d.dir+"/"+stem+warmupSuffix) || !exists(d.dir+"/"+stem+tailSuffix) {
			t.Fatalf("%s was evicted", stem)
		}
	}
}
