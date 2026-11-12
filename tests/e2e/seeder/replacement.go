package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/anacrolix/torrent/metainfo"
)

// The replacement scenario files three releases of one episode, one after the
// other. A and B are seeded and announced. C has a valid .torrent but no data
// anywhere and the tracker returns no peer for it, so the library's readability
// check on a replacement can never succeed. run.sh drives the pace through
// handshake files in the shared directory: after each add the seeder publishes
// its result and waits for run.sh to check the mount and say go.
const (
	episodeVideoSize = 30<<20 + 4321 // above the 20 MiB stub floor
	releaseA         = "Flodni.E2E.S01E01.A.1080p.WEB-DL"
	releaseB         = "Flodni.E2E.S01E01.B.1080p.WEB-DL"
	releaseC         = "Flodni.E2E.S01E01.C.1080p.WEB-DL"
	// Longer than the product's 60 s readability check plus its metadata step.
	refusedAddLimit = 100 * time.Second
	handshakeLimit  = 3 * time.Minute
)

type episodeRelease struct {
	name   string
	seed   byte
	seeded bool

	sum  string
	mi   *metainfo.MetaInfo
	hash string
}

func runReplacement(out, flodni string, peerPort, trackerPort int) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	dataDir, err := os.MkdirTemp("", "seeder-data")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dataDir) }() // scratch space
	// C's file is written here only to hash it into a .torrent, then deleted.
	scratch, err := os.MkdirTemp("", "seeder-unseeded")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(scratch) }() // scratch space

	rels := []*episodeRelease{
		{name: releaseA, seed: 11, seeded: true},
		{name: releaseB, seed: 12, seeded: true},
		{name: releaseC, seed: 13},
	}
	known := map[string]bool{}
	for _, r := range rels {
		dir := scratch
		if r.seeded {
			dir = dataDir
		}
		if err := r.build(dir); err != nil {
			return fmt.Errorf("%s: %w", r.name, err)
		}
		if r.seeded {
			known[r.hash] = true
		}
		log.Printf("%s: info hash %s, sha256 %s, seeded=%t", r.name, r.hash, r.sum, r.seeded)
	}
	a, b, c := rels[0], rels[1], rels[2]

	ip, err := ownIPv4()
	if err != nil {
		return err
	}
	stopTracker := startTracker(trackerPort, announceHandler(ip, peerPort, known))
	defer stopTracker()

	cl, err := newClient(dataDir, peerPort)
	if err != nil {
		return err
	}
	defer cl.Close()
	for _, r := range []*episodeRelease{a, b} {
		t, err := cl.AddTorrent(r.mi)
		if err != nil {
			return fmt.Errorf("add torrent %s: %w", r.name, err)
		}
		<-t.GotInfo()
		t.VerifyData()
		if err := waitComplete(ctx, t, 2*time.Minute); err != nil {
			return fmt.Errorf("%s: %w", r.name, err)
		}
		log.Printf("seeding %s: all %d bytes verified", r.name, t.Length())
	}

	for _, u := range []string{":8090/echo", ":9080/control"} {
		if err := waitHTTP(ctx, "http://"+flodni+u, 2*time.Minute); err != nil {
			return err
		}
	}

	// Step 2: A is the first release of the episode, so there is nothing to replace.
	pathA, err := step("add A", func() (string, error) { return a.add(ctx, flodni) })
	if err != nil {
		return err
	}
	if err := publish(out, map[string]string{
		"a.sha256": a.sum, "a.hash": a.hash, "b.hash": b.hash, "c.hash": c.hash, "a.path": pathA,
	}); err != nil {
		return err
	}
	if err := waitFile(ctx, filepath.Join(out, "go-b"), handshakeLimit); err != nil {
		return err
	}

	// Step 3: B replaces A once the readability check reads from this seeder.
	pathB, err := step("add B", func() (string, error) { return b.add(ctx, flodni) })
	if err != nil {
		return err
	}
	if err := publish(out, map[string]string{"b.sha256": b.sum, "b.path": pathB}); err != nil {
		return err
	}
	if err := waitFile(ctx, filepath.Join(out, "go-c"), handshakeLimit); err != nil {
		return err
	}

	// Step 4: C cannot deliver, so the add must be refused. The status is recorded,
	// not judged here, so run.sh owns every verdict.
	cctx, cancel := context.WithTimeout(ctx, refusedAddLimit)
	defer cancel()
	start := time.Now()
	status, body, err := postAdd(cctx, flodni, c.request())
	if err != nil {
		return fmt.Errorf("add C: %w", err)
	}
	elapsed := time.Since(start).Round(time.Second)
	log.Printf("step add C: status %d after %s: %s", status, elapsed, body)
	if err := publish(out, map[string]string{
		"c.body":    string(body),
		"c.seconds": strconv.Itoa(int(elapsed.Seconds())),
		"c.status":  strconv.Itoa(status), // last: run.sh waits for it
	}); err != nil {
		return err
	}

	<-ctx.Done()
	log.Printf("stopping")
	return nil
}

// build writes the release's video under dir and its private metainfo.
func (r *episodeRelease) build(dir string) error {
	path := filepath.Join(dir, r.name+".mkv")
	h := sha256.New()
	if err := writeRandom(path, r.seed, episodeVideoSize, h); err != nil {
		return err
	}
	r.sum = hex.EncodeToString(h.Sum(nil))
	mi, err := buildMetaInfo(path)
	if err != nil {
		return err
	}
	r.mi = mi
	r.hash = mi.HashInfoBytes().HexString()
	if !r.seeded {
		return os.Remove(path)
	}
	return nil
}

func (r *episodeRelease) request() map[string]any {
	var file bytes.Buffer
	if err := r.mi.Write(&file); err != nil {
		log.Fatalf("FAIL: %s: write metainfo: %v", r.name, err)
	}
	return map[string]any{
		"type":           "tv",
		"hash":           r.hash,
		"torrent_file":   file.Bytes(),
		"title":          "Flodni E2E",
		"first_air_date": "2026-01-01",
		"season":         1,
		"episode":        1,
		"release_title":  r.name,
		"metadata_wait":  60,
	}
}

// add files the release and returns the fuse_path of the episode's stub.
func (r *episodeRelease) add(ctx context.Context, flodni string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	return addAccepted(ctx, flodni, r.request())
}

func step(name string, fn func() (string, error)) (string, error) {
	start := time.Now()
	v, err := fn()
	log.Printf("step %s: %s", name, time.Since(start).Round(time.Millisecond))
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return v, nil
}

// publish writes the given files atomically. The names run.sh waits on (a.path,
// b.path, c.status) come last, so it never reads a half-published batch.
func publish(dir string, files map[string]string) error {
	for _, name := range []string{"a.sha256", "b.sha256", "a.hash", "b.hash", "c.hash", "c.body", "c.seconds", "a.path", "b.path", "c.status"} {
		if v, ok := files[name]; ok {
			if err := writeAtomic(filepath.Join(dir, name), v); err != nil {
				return err
			}
		}
	}
	return nil
}

func waitFile(ctx context.Context, path string, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return fmt.Errorf("%s not there after %s", filepath.Base(path), limit)
}
