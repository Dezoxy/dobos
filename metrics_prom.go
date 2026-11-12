package main

import (
	"net/http"
	"sort"

	"flodni/internal/promtext"
	"flodni/internal/warmup"
)

// handlePrometheus serves GET /metrics/prometheus: the text exposition of the
// counters and histograms the daemon already keeps. It reads counters the daemon
// keeps in memory and never calls the engine, so a scrape takes no engine lock. It sits behind the
// control token like every route but /control and /api/health.
func handlePrometheus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var pw promtext.Writer
	collectProm(&pw)
	if err := pw.Err(); err != nil {
		logger.Printf("WARNING: metrics/prometheus not served: %v", err)
		http.Error(w, "metrics unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", promtext.ContentType)
	_, _ = w.Write(pw.Bytes())
}

// ttffBoundsSeconds are the histogram bounds in seconds. The histograms keep whole
// milliseconds, so a read under 1 ms lands in le="0.001" with 0 added to _sum.
var ttffBoundsSeconds = func() []float64 {
	out := make([]float64, len(ttffBucketsMS))
	for i, ms := range ttffBucketsMS {
		out[i] = float64(ms) / 1000
	}
	return out
}()

func promHistogram(h *TTFFHistogram, labels ...promtext.Label) promtext.Histogram {
	counts, sumMS := h.snapshot()
	return promtext.Histogram{
		Labels: labels,
		Bounds: ttffBoundsSeconds,
		Counts: counts[:],
		Sum:    float64(sumMS) / 1000,
	}
}

func promSample(v int64, labels ...promtext.Label) promtext.Sample {
	return promtext.Sample{Labels: labels, Value: float64(v)}
}

func lbl(name, value string) promtext.Label { return promtext.Label{Name: name, Value: value} }

// collectProm writes every series. Label values are emitted at 0 from the start so
// a rate() has a series to start from.
func collectProm(w *promtext.Writer) {
	w.Gauge("flodni_build_info", "Always 1; the version is in the label.",
		promSample(1, lbl("version", AppVersion)))
	w.Gauge("process_start_time_seconds", "Start time of the process, seconds since the Unix epoch.",
		promSample(startTime.Unix()))

	w.Histogram("flodni_session_first_byte_seconds",
		"Open to the first byte served, per session classed as real playback. Whole milliseconds.",
		promHistogram(&ttffStats.openToHeader))
	w.Histogram("flodni_session_first_deep_read_seconds",
		"Open to the first read at 8 MiB or later or in the tail, per session classed as real playback. Whole milliseconds.",
		promHistogram(&ttffStats.openToDeepRead))
	w.Histogram("flodni_seek_read_duration_seconds",
		"Duration of the read that followed a seek. Whole milliseconds.",
		promHistogram(&ttffStats.seekLatency))

	sources := []struct {
		src  ttffSource
		hist *TTFFHistogram
	}{
		{srcWarmupHead, &ttffStats.warmupHead},
		{srcWarmupTail, &ttffStats.warmupTail},
		{srcRACacheHit, &ttffStats.raCacheHit},
		{srcFetchBlock, &ttffStats.fetchBlock},
	}
	hists := make([]promtext.Histogram, 0, len(sources))
	bytesBySource := make([]promtext.Sample, 0, len(sources))
	for _, s := range sources {
		hists = append(hists, promHistogram(s.hist, lbl("source", s.src.String())))
		bytesBySource = append(bytesBySource, promSample(ttffStats.readBytes[s.src].Load(), lbl("source", s.src.String())))
	}
	w.Histogram("flodni_read_duration_seconds",
		"Duration of each served read by the path that answered it, not by where the bytes came from. Whole milliseconds.",
		hists...)
	w.Counter("flodni_read_bytes_total",
		"Bytes handed to the kernel by the path that answered. Counts reads outside any session too, so it differs from the [Session] lines.",
		bytesBySource...)

	closed := make([]promtext.Sample, 0, len(sessionOutcomes))
	for i, o := range sessionOutcomes {
		closed = append(closed, promSample(sessionsClosedByOutcome[i].Load(), lbl("outcome", o)))
	}
	w.Counter("flodni_sessions_closed_total", "Closed sessions by outcome.", closed...)

	verdicts := deadSwarmVerdictCounts()
	keys := make([][2]string, 0, len(verdicts))
	for k := range verdicts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][1] != keys[j][1] {
			return keys[i][1] < keys[j][1]
		}
		return keys[i][0] < keys[j][0]
	})
	verdictSamples := make([]promtext.Sample, 0, len(keys))
	for _, k := range keys {
		verdictSamples = append(verdictSamples, promSample(verdicts[k], lbl("reason", k[0]), lbl("source", k[1])))
	}
	w.Counter("flodni_dead_swarm_verdicts_total",
		"Dead-swarm verdicts by reason and source. Stays 0 when the state database is off.",
		verdictSamples...)

	collectWarmupProm(w)
	collectPlaybackProm(w)
	collectReadAheadProm(w)

	w.Counter("flodni_fuse_short_reads_total",
		"Reads served with fewer bytes than requested away from EOF.",
		promSample(fuseShortReadCount.Load()))
	w.Counter("flodni_fuse_short_read_results_total",
		"Outcome of the refill after a short read.",
		promSample(fuseShortReadRepaired.Load(), lbl("result", "repaired")),
		promSample(fuseShortReadFailed.Load(), lbl("result", "failed")))
	w.Counter("flodni_fetch_dedup_total",
		"Concurrent fetches of the same chunk that shared one fetch.",
		promSample(fetchFlightDedupCount.Load()))
}

func collectWarmupProm(w *promtext.Writer) {
	st := warmup.CounterStats()
	if d := warmup.DiskWarmup; d != nil {
		w.Gauge("flodni_warmup_cache_bytes",
			"Approximate size of the warmup cache on disk: a running total, resynced from the disk on each eviction walk, that can drift.",
			promSample(d.TrackedBytes()))
		w.Gauge("flodni_warmup_cache_quota_bytes", "Size limit of the warmup cache.", promSample(st.QuotaBytes))
	}
	w.Counter("flodni_warmup_evictions_total", "Files removed by quota eviction, by window.",
		promSample(st.EvictedHeads, lbl("window", "head")),
		promSample(st.EvictedTails, lbl("window", "tail")))
	w.Counter("flodni_warmup_evicted_bytes_total", "Bytes (apparent size) removed by quota eviction, by window.",
		promSample(st.EvictedHeadBytes, lbl("window", "head")),
		promSample(st.EvictedTailBytes, lbl("window", "tail")))
	w.Counter("flodni_warmup_unverified_drops_total",
		"Warmup blocks, head or tail, never written because their pieces did not verify in time.",
		promSample(warmup.UnverifiedDrops()))
	w.Counter("flodni_warmup_write_queue_drops_total",
		"Head chunks dropped because the warmup write queue was full.",
		promSample(st.WriteQueueDrops))
}

func collectPlaybackProm(w *promtext.Writer) {
	w.Counter("flodni_playback_starts_total",
		"Playbacks the media server webhook confirmed (not playing to playing).",
		promSample(playbackStarts.Load()))
	w.Counter("flodni_warmup_cold_reopen_total",
		"Confirmed plays that opened with no ready head on a file quota eviction had removed, counted once per eviction. previously_played says whether the file had been played before.",
		promSample(coldReopensPrevPlay.Load(), lbl("previously_played", "true")),
		promSample(coldReopensNotPlayed.Load(), lbl("previously_played", "false")))
}

func collectReadAheadProm(w *promtext.Writer) {
	total, active, entries := raCache.Stats()
	w.Gauge("flodni_readahead_cache_bytes", "Bytes held in the read-ahead cache.", promSample(total))
	w.Gauge("flodni_readahead_cache_active_bytes", "Bytes in the read-ahead cache that are not stale.", promSample(active))
	w.Gauge("flodni_readahead_cache_entries", "Entries in the read-ahead cache.", promSample(int64(entries)))
	if b := gc(); b != nil {
		w.Gauge("flodni_readahead_cache_budget_bytes", "Size budget of the read-ahead cache.", promSample(b.ReadAheadBudget))
	}
}
