package main

// Dezoxy fork: POST /api/index/seed fills a streamed file's index windows from the
// library's own copy of it, once its piece hashes prove the copy identical.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"tiramisu/internal/gostorm/torr"
	"tiramisu/internal/vfs"
	"tiramisu/internal/warmup"
)

// maxSeedFiles bounds one request's open files and hashing: a season pack is a few dozen.
const maxSeedFiles = 512

type seedFile struct {
	Virtual string `json:"virtual"` // path under the FUSE mount, e.g. tv/Show/Season.1/x.mkv
	Local   string `json:"local"`   // the library's copy, under index_seed_root
}

// seedRequest lists files of one torrent. Pass every file you have a copy of: a piece
// that straddles two files can be hashed only with both copies.
type seedRequest struct {
	Files []seedFile `json:"files"`
}

type seedResult struct {
	Virtual      string `json:"virtual"`
	FileID       int    `json:"file_id"`
	Result       string `json:"result"` // seeded, different, error
	PiecesHashed int    `json:"pieces_hashed"`
	HeadBytes    int64  `json:"head_bytes"`
	TailBytes    int64  `json:"tail_bytes"`
	Error        string `json:"error,omitempty"`
}

type seedResponse struct {
	Hash  string       `json:"hash"`
	Files []seedResult `json:"files"`
}

// openUnder opens p for reading through an os.Root at root: the kernel-side walk refuses
// any path, ".." or symlink included, that leaves the root.
func openUnder(root, p string) (*os.File, error) {
	if root == "" || !filepath.IsAbs(p) {
		return nil, errors.New("want an absolute path under index_seed_root")
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(p))
	if err != nil || rel == "." {
		return nil, errors.New("path is not under index_seed_root")
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return r.Open(rel)
}

// virtualStub maps a path under the FUSE mount to its stub under the source tree.
func virtualStub(source, virtual string) (string, bool) {
	rel := filepath.Clean("/" + virtual) // anchor, so ".." cannot climb out
	if rel == "/" {
		return "", false
	}
	return filepath.Join(source, rel), true
}

func handleIndexSeed(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if warmup.DiskWarmup == nil || gc().WarmupDir == "" || gc().IndexSeedRoot == "" {
		http.Error(w, "index seeding is not enabled (warmup_dir, index_seed_root)", http.StatusNotFound)
		return
	}
	var req seedRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil || len(req.Files) == 0 {
		http.Error(w, "bad request: want {\"files\": [{\"virtual\", \"local\"}]}", http.StatusBadRequest)
		return
	}

	if len(req.Files) > maxSeedFiles {
		http.Error(w, fmt.Sprintf("at most %d files per request", maxSeedFiles), http.StatusRequestEntityTooLarge)
		return
	}

	hash := ""
	copies := map[int]torr.LocalCopy{}
	sizes := map[int]int64{}
	results := make([]seedResult, len(req.Files))
	for i, sf := range req.Files {
		results[i].Virtual = sf.Virtual
		stub, ok := virtualStub(physicalSourcePath, sf.Virtual)
		if !ok {
			http.Error(w, "bad virtual path: "+sf.Virtual, http.StatusBadRequest)
			return
		}
		meta, err := stubMeta(stub)
		if err != nil {
			http.Error(w, "no streamed file at "+sf.Virtual, http.StatusNotFound)
			return
		}
		h, id := vfs.ExtractHashAndIndex(meta.URL)
		if h == "" || id <= 0 || (hash != "" && h != hash) {
			http.Error(w, "files must belong to one torrent: "+sf.Virtual, http.StatusBadRequest)
			return
		}
		if _, dup := copies[id]; dup {
			http.Error(w, "file listed twice: "+sf.Virtual, http.StatusBadRequest)
			return
		}
		hash = h
		results[i].FileID = id
		f, err := openUnder(gc().IndexSeedRoot, sf.Local)
		if err != nil {
			http.Error(w, "local path must be a file under index_seed_root: "+sf.Local, http.StatusForbidden)
			return
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil || !fi.Mode().IsRegular() {
			http.Error(w, sf.Local+" is not a regular file", http.StatusBadRequest)
			return
		}
		copies[id] = torr.LocalCopy{R: f, Size: fi.Size()}
		sizes[id] = fi.Size()
	}

	// The metainfo comes from the state DB through the normal wake: no data is fetched.
	if nativeBridge == nil {
		http.Error(w, "engine not ready", http.StatusServiceUnavailable)
		return
	}
	wctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if err := nativeBridge.Wake(wctx, "magnet:?xt=urn:btih:"+hash, results[0].FileID); err != nil {
		http.Error(w, "wake torrent: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	t := torr.PeekTorrent(hash)
	for i := range results {
		res := &results[i]
		size := sizes[res.FileID]
		windows := [][2]int64{{0, min(warmup.FileSize, size)}, {max(size-warmup.TailWarmupSize, 0), size}}
		spans, hashed, err := t.VerifiedWindows(copies, res.FileID, windows)
		res.PiecesHashed = hashed
		switch {
		case errors.Is(err, torr.ErrNotIdentical):
			res.Result, res.Error = "different", err.Error()
			continue
		case err != nil:
			res.Result, res.Error = "error", err.Error()
			continue
		}
		seeds := make([]warmup.Seed, len(spans))
		for j, sp := range spans {
			seeds[j] = warmup.Seed{Off: sp.Off, Data: sp.Data}
		}
		res.HeadBytes, res.TailBytes, err = warmup.DiskWarmup.SeedSpans(hash, res.FileID, size, seeds)
		if err != nil {
			res.Result, res.Error = "error", err.Error()
			continue
		}
		res.Result = "seeded"
		logger.Printf("[IndexSeed] %s: %d pieces hashed, head %dKB, tail %dKB",
			filepath.Base(res.Virtual), hashed, res.HeadBytes>>10, res.TailBytes>>10)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(seedResponse{Hash: hash, Files: results})
}
