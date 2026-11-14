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
	"strings"
	"syscall"
	"time"

	"github.com/anacrolix/torrent/metainfo"
)

// The removal scenario files four single-file movies at once and keeps
// seeding them. run.sh reads the first, opens the second and removes the
// second, third and fourth (two through the library API, one with rm through
// the mount), so the seeder only publishes what run.sh needs and waits.
const (
	removalVideoSize = 64<<20 + 4321 // above the stub floor, past the windows
	// A is the title read while the others are removed: long enough to still be
	// reading when they go.
	removalReadSize = 192<<20 + 777
)

var removalTitles = []string{"A", "B", "C", "D"}

type removalTitle struct {
	letter string
	sum    string
	mi     *metainfo.MetaInfo
	hash   string
}

func runRemoval(out, flodni string, peerPort, trackerPort int) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	dataDir, err := os.MkdirTemp("", "seeder-data")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dataDir) }() // scratch space

	var titles []*removalTitle
	for i, letter := range removalTitles {
		t := &removalTitle{letter: letter}
		path := filepath.Join(dataDir, t.releaseName()+".mkv")
		h := sha256.New()
		size := int64(removalVideoSize)
		if i == 0 {
			size = removalReadSize
		}
		if err := writeRandom(path, byte(21+i), size, h); err != nil {
			return fmt.Errorf("%s: %w", letter, err)
		}
		t.sum = hex.EncodeToString(h.Sum(nil))
		if t.mi, err = buildMetaInfo(path); err != nil {
			return fmt.Errorf("%s: %w", letter, err)
		}
		t.hash = t.mi.HashInfoBytes().HexString()
		log.Printf("%s: info hash %s, sha256 %s", letter, t.hash, t.sum)
		titles = append(titles, t)
	}

	ip, err := ownIPv4()
	if err != nil {
		return err
	}
	stopTracker := startTracker(trackerPort, announceHandler(ip, peerPort, nil))
	defer stopTracker()

	cl, err := newClient(dataDir, peerPort)
	if err != nil {
		return err
	}
	defer cl.Close()
	for _, t := range titles {
		tt, err := cl.AddTorrent(t.mi)
		if err != nil {
			return fmt.Errorf("add torrent %s: %w", t.letter, err)
		}
		<-tt.GotInfo()
		tt.VerifyData()
		if err := waitComplete(ctx, tt, 2*time.Minute); err != nil {
			return fmt.Errorf("%s: %w", t.letter, err)
		}
		log.Printf("seeding %s: all %d bytes verified", t.letter, tt.Length())
	}

	for _, u := range []string{":8090/echo", ":9080/control"} {
		if err := waitHTTP(ctx, "http://"+flodni+u, 2*time.Minute); err != nil {
			return err
		}
	}

	files := map[string]string{"a.sha256": titles[0].sum}
	for _, t := range titles {
		path, err := step("add "+t.letter, func() (string, error) { return t.add(ctx, flodni) })
		if err != nil {
			return err
		}
		l := strings.ToLower(t.letter)
		files[l+".hash"], files[l+".path"] = t.hash, path
	}
	// d.path comes last: run.sh waits on it.
	for _, name := range []string{"a.sha256", "a.hash", "b.hash", "c.hash", "d.hash", "a.path", "b.path", "c.path", "d.path"} {
		if err := writeAtomic(filepath.Join(out, name), files[name]); err != nil {
			return err
		}
	}

	<-ctx.Done()
	log.Printf("stopping")
	return nil
}

func (t *removalTitle) releaseName() string {
	return "Flodni.E2E.Removal." + t.letter + ".2026.1080p.WEB-DL"
}

func (t *removalTitle) add(ctx context.Context, flodni string) (string, error) {
	var file bytes.Buffer
	if err := t.mi.Write(&file); err != nil {
		return "", fmt.Errorf("write metainfo: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	return addAccepted(ctx, flodni, map[string]any{
		"type":          "movie",
		"hash":          t.hash,
		"torrent_file":  file.Bytes(),
		"title":         "Flodni E2E Removal " + t.letter,
		"year":          2026,
		"release_title": t.releaseName(),
		"metadata_wait": 60,
	})
}
