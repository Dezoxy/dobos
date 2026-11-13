package main

import (
	"encoding/json"
	"math"
	"net/http"

	"flodni/internal/warmup"
)

// warmupEvictRequest is the body of POST /api/warmup/evict: exactly one target.
type warmupEvictRequest struct {
	TargetGB    *float64 `json:"target_gb"`
	TargetBytes *int64   `json:"target_bytes"`
}

// target returns the size to evict down to, or false when the body names none or
// more than one, or a size that is negative, not a number or too large.
func (r warmupEvictRequest) target() (int64, bool) {
	if (r.TargetGB == nil) == (r.TargetBytes == nil) {
		return 0, false
	}
	if r.TargetBytes != nil {
		return *r.TargetBytes, *r.TargetBytes >= 0
	}
	b := *r.TargetGB * (1 << 30)
	if math.IsNaN(b) || b < 0 || b > 1<<60 {
		return 0, false
	}
	return int64(b), true
}

// newWarmupEvictHandler serves POST /api/warmup/evict. It deletes cache files: it
// evicts down to the requested size by the quota's own order and records each
// eviction as a quota eviction does. evict reports false when the cache is off.
func newWarmupEvictHandler(evict func(target int64) (warmup.EvictResult, bool)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req warmupEvictRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil {
			http.Error(w, "bad request: body must be JSON with target_gb or target_bytes", http.StatusBadRequest)
			return
		}
		target, ok := req.target()
		if !ok {
			http.Error(w, "bad request: give exactly one of target_gb or target_bytes, not negative", http.StatusBadRequest)
			return
		}
		res, on := evict(target)
		if !on {
			http.Error(w, "warmup cache is off", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]int64{
			"evicted_files":     int64(res.Files),
			"evicted_bytes":     res.Bytes,
			"total_bytes_after": res.TotalAfter,
			"target_bytes":      target,
		})
	}
}

// evictWarmupTo is the handler's link to the cache that is running now.
func evictWarmupTo(target int64) (warmup.EvictResult, bool) {
	d := warmup.DiskWarmup
	if d == nil {
		return warmup.EvictResult{}, false
	}
	return d.EvictTo(target), true
}
