package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"flodni/internal/warmup"
)

func serveEvict(t *testing.T, method, body string, evict func(int64) (warmup.EvictResult, bool)) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, "/api/warmup/evict", strings.NewReader(body))
	newWarmupEvictHandler(evict)(rec, req)
	return rec
}

func TestWarmupEvictHandler(t *testing.T) {
	const gib = int64(1) << 30
	var got []int64
	on := func(target int64) (warmup.EvictResult, bool) {
		got = append(got, target)
		return warmup.EvictResult{Files: 2, Bytes: 5, TotalAfter: 7}, true
	}

	rec := serveEvict(t, http.MethodPost, `{"target_gb": 1.5}`, on)
	var out map[string]int64
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != 200 {
		t.Fatalf("%d %q %v", rec.Code, rec.Body.String(), err)
	}
	want := map[string]int64{"evicted_files": 2, "evicted_bytes": 5, "total_bytes_after": 7, "target_bytes": gib * 3 / 2}
	for k, v := range want {
		if out[k] != v {
			t.Fatalf("%s = %d, want %d (%v)", k, out[k], v, out)
		}
	}
	if rec := serveEvict(t, http.MethodPost, `{"target_bytes": 4096}`, on); rec.Code != 200 || got[len(got)-1] != 4096 {
		t.Fatalf("target_bytes: %d %v", rec.Code, got)
	}
	if rec := serveEvict(t, http.MethodPost, `{"target_gb": 0}`, on); rec.Code != 200 || got[len(got)-1] != 0 {
		t.Fatalf("a target of 0 is allowed: %d %v", rec.Code, got)
	}

	n := len(got)
	for name, body := range map[string]string{
		"empty":         ``,
		"not json":      `target_gb=1`,
		"no target":     `{}`,
		"both targets":  `{"target_gb":1,"target_bytes":1}`,
		"negative gb":   `{"target_gb":-1}`,
		"negative byte": `{"target_bytes":-1}`,
		"string":        `{"target_gb":"1"}`,
		"too large":     `{"target_gb":1e30}`,
		"fractional":    `{"target_bytes":1.5}`,
	} {
		if rec := serveEvict(t, http.MethodPost, body, on); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: code %d, want 400", name, rec.Code)
		}
	}
	if len(got) != n {
		t.Fatal("a bad target must not reach the cache")
	}

	off := func(int64) (warmup.EvictResult, bool) { return warmup.EvictResult{}, false }
	if rec := serveEvict(t, http.MethodPost, `{"target_gb":1}`, off); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("cache off: %d, want 503", rec.Code)
	}
	if rec := serveEvict(t, http.MethodGet, ``, on); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %d, want 405", rec.Code)
	}
}

func TestEvictWarmupToWithTheCacheOff(t *testing.T) {
	old := warmup.DiskWarmup
	warmup.DiskWarmup = nil
	t.Cleanup(func() { warmup.DiskWarmup = old })
	if _, on := evictWarmupTo(0); on {
		t.Fatal("no cache: must report off")
	}
}
