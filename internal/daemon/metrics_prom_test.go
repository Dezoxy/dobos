package daemon

import (
	"errors"
	"flodni/internal/flog"
	"flodni/internal/gostorm/native"
	"flodni/internal/promtext"
	"flodni/internal/warmup"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPromHistogramConversion(t *testing.T) {
	var h TTFFHistogram
	for _, ms := range []int64{0, 1, 2, 10000, 11000} {
		h.Add(time.Duration(ms) * time.Millisecond)
	}
	var w promtext.Writer
	w.Histogram("t_seconds", "t", promHistogram(&h))
	if err := w.Err(); err != nil {
		t.Fatal(err)
	}
	if err := promtext.Lint(w.Bytes()); err != nil {
		t.Fatal(err)
	}
	out := string(w.Bytes())
	for _, want := range []string{
		`t_seconds_bucket{le="0.001"} 2`,
		`t_seconds_bucket{le="0.005"} 3`,
		`t_seconds_bucket{le="3"} 3`,
		`t_seconds_bucket{le="10"} 4`,
		`t_seconds_bucket{le="+Inf"} 5`,
		`t_seconds_sum 21.003`,
		`t_seconds_count 5`,
	} {
		if !strings.Contains(out, want+"\n") {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func scrape(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	handlePrometheus(rec, httptest.NewRequest(http.MethodGet, "/metrics/prometheus", nil))
	return rec
}

func TestHandlePrometheus(t *testing.T) {
	rec := scrape(t)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/plain; version=0.0.4; charset=utf-8" {
		t.Fatalf("content type %q", got)
	}
	if err := promtext.Lint(rec.Body.Bytes()); err != nil {
		t.Fatalf("scrape fails Lint: %v\n%s", err, rec.Body)
	}
	body := rec.Body.String()
	for _, fam := range []string{
		"flodni_build_info", "process_start_time_seconds",
		"flodni_session_first_byte_seconds", "flodni_session_first_deep_read_seconds",
		"flodni_seek_read_duration_seconds", "flodni_read_duration_seconds", "flodni_read_bytes_total",
		"flodni_sessions_closed_total", "flodni_dead_swarm_verdicts_total",
		"flodni_warmup_evictions_total", "flodni_warmup_evicted_bytes_total",
		"flodni_playback_starts_total", "flodni_warmup_cold_reopen_total",
		"flodni_warmup_unverified_drops_total", "flodni_warmup_write_queue_drops_total",
		"flodni_readahead_cache_bytes", "flodni_readahead_cache_active_bytes", "flodni_readahead_cache_entries",
		"flodni_fuse_short_reads_total", "flodni_fuse_short_read_results_total", "flodni_fetch_dedup_total",
		"flodni_unconfirmed_read_bytes_total", "flodni_unconfirmed_read_sessions_total",
		"flodni_unconfirmed_read_refused_reads_total", "flodni_unconfirmed_read_holds_total",
		"flodni_unconfirmed_read_confirmed_after_refusal_total",
		"flodni_playback_webhooks_received_total", "flodni_playback_webhook_match_rule_total", "flodni_warm_fills_total",
		"flodni_playback_webhook_last_timestamp_seconds", "flodni_unconfirmed_read_last_session_timestamp_seconds", "flodni_warning",
		"flodni_torrent_idle_closes_total", "flodni_torrent_reopens_total", "flodni_read_stalls_total",
		"flodni_keepalive_releases_total", "flodni_keepalive_evicted_total", "flodni_keepalive_held_torrents",
		"flodni_wake_refused_total",
		"flodni_warm_fill_bytes_total", "flodni_warm_cache_failures_total", "flodni_warm_queue_files", "flodni_warm_paused",
		"flodni_stub_index_mismatch_total",
		"flodni_engine_short_reads_total", "flodni_engine_short_read_repairs_total", "flodni_sessions_open",
		"go_goroutines", "go_memstats_heap_inuse_bytes", "go_memstats_sys_bytes", "go_gc_cycles_total",
	} {
		if !strings.Contains(body, "# TYPE "+fam+" ") {
			t.Errorf("family %s missing", fam)
		}
	}
	// Every label value is present from the start.
	for _, want := range []string{
		`flodni_sessions_closed_total{outcome="dead_swarm"}`,
		`flodni_read_bytes_total{source="warmup_tail"}`,
		`flodni_dead_swarm_verdicts_total{reason="not_active",source="mount"}`,
		`flodni_warmup_evictions_total{window="tail"}`,
		`flodni_warmup_cold_reopen_total{previously_played="true"}`,
		`flodni_warmup_cold_reopen_total{previously_played="false"}`,
		`flodni_playback_starts_total`,
		`flodni_stub_index_mismatch_total`,
		`flodni_torrent_idle_closes_total{confirmed="true"}`,
		`flodni_torrent_idle_closes_total{confirmed="false"}`,
		`flodni_torrent_reopens_total{confirmed="true"}`,
		`flodni_torrent_reopens_total{confirmed="false"}`,
		`flodni_read_stalls_total{confirmed="true"}`,
		`flodni_keepalive_releases_total{reason="stop"}`,
		`flodni_keepalive_releases_total{reason="ceiling"}`,
		`flodni_keepalive_evicted_total`,
		`flodni_keepalive_held_torrents`,
		`flodni_read_stalls_total{confirmed="false"}`,
		`flodni_unconfirmed_read_bytes_total`,
		`flodni_unconfirmed_read_sessions_total{outcome="unconfirmed"}`,
		`flodni_unconfirmed_read_sessions_total{outcome="confirmed_late"}`,
		`flodni_unconfirmed_read_refused_reads_total`,
		`flodni_unconfirmed_read_holds_total{result="lifted"}`,
		`flodni_unconfirmed_read_holds_total{result="refused"}`,
		`flodni_unconfirmed_read_confirmed_after_refusal_total`,
		`flodni_playback_webhooks_received_total{matched="true"}`,
		`flodni_playback_webhooks_received_total{matched="false"}`,
		`flodni_playback_webhook_match_rule_total{rule="basename"}`,
		`flodni_playback_webhook_match_rule_total{rule="path_map"}`,
		`flodni_playback_webhook_match_rule_total{rule="imdb"}`,
		`flodni_playback_webhook_match_rule_total{rule="fuzzy"}`,
		`flodni_playback_webhook_match_rule_total{rule="none"}`,
		`flodni_playback_webhook_last_timestamp_seconds{matched="true"}`,
		`flodni_playback_webhook_last_timestamp_seconds{matched="false"}`,
		`flodni_unconfirmed_read_last_session_timestamp_seconds`,
		`flodni_warning{name="no_play_webhook"}`,
		`flodni_warm_fills_total{trigger="mount",outcome="warmed"}`,
		`flodni_warm_fills_total{trigger="mount",outcome="cached"}`,
		`flodni_engine_short_reads_total{path="stream"}`,
		`flodni_engine_short_reads_total{path="fetch"}`,
		`flodni_engine_short_read_repairs_total{result="repaired"}`,
		`flodni_engine_short_read_repairs_total{result="unfilled"}`,
		`flodni_sessions_open`,
	} {
		if !strings.Contains(body, want+" ") {
			t.Errorf("series %s missing", want)
		}
	}

	post := httptest.NewRecorder()
	handlePrometheus(post, httptest.NewRequest(http.MethodPost, "/metrics/prometheus", nil))
	if post.Code != http.StatusMethodNotAllowed || post.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("POST: status %d, Allow %q", post.Code, post.Header().Get("Allow"))
	}
}

func TestSessionClosedCountedPerOutcome(t *testing.T) {
	for _, tt := range []struct {
		outcome string
		setup   func(s *TTFFSession)
	}{
		{outcomeServed, func(s *TTFFSession) { s.recordRead(srcFetchBlock, time.Millisecond, 2<<20, 0) }},
		{outcomeProbe, func(s *TTFFSession) { s.recordRead(srcWarmupHead, time.Millisecond, 4096, 0) }},
		{outcomeUnserved, func(*TTFFSession) {}},
		{outcomeDeadSwarm, func(s *TTFFSession) { s.readFailed.Store(true) }},
	} {
		t.Run(tt.outcome, func(t *testing.T) {
			readabilityEnv(t, 0, false)
			before := closedCounts()
			s := newTestSession(time.Minute)
			tt.setup(s)
			s.closeSession()
			after := closedCounts()
			for _, o := range sessionOutcomes {
				want := before[o]
				if o == tt.outcome {
					want++
				}
				if after[o] != want {
					t.Errorf("%s: %d, want %d", o, after[o], want)
				}
			}
		})
	}
}

func closedCounts() map[string]int64 {
	out := map[string]int64{}
	for i, o := range sessionOutcomes {
		out[o] = sessionsClosedByOutcome[i].Load()
	}
	return out
}

func TestReadBytesCountedWithoutSession(t *testing.T) {
	resetSessionState(t)
	before := ttffStats.readBytes[srcWarmupTail].Load()
	ttffRead("/m/none.mkv", srcWarmupTail, time.Millisecond, 4096, 0)
	ttffRead("/m/none.mkv", srcWarmupTail, time.Millisecond, 0, 0) // a read of nothing adds none
	if got := ttffStats.readBytes[srcWarmupTail].Load() - before; got != 4096 {
		t.Fatalf("bytes +%d, want 4096", got)
	}
}

type failingStore struct{}

func (failingStore) RecordMetadataFailure(string, string, string, time.Time) error {
	return errors.New("disk full")
}
func (failingStore) RecordSwarmRead(string, time.Time) error         { return nil }
func (failingStore) ResetMetadataFailures(string) error              { return nil }
func (failingStore) RecordNotStored(string, string, time.Time) error { return nil }

func TestVerdictCountedWhenTheStoreFails(t *testing.T) {
	rec := newReachabilityRecorder(failingStore{}, flog.NewText(io.Discard))
	key := [2]string{failReasonNoPeers, failSourceMount}
	before := deadSwarmVerdictCounts()[key]
	rec(native.Outcome{Hash: rHash, Kind: native.OutcomeFailed, Reason: failReasonNoPeers, Source: failSourceMount})
	rec(native.Outcome{Hash: rHash, Kind: native.OutcomeSwarmRead}) // not a verdict
	if got := deadSwarmVerdictCounts()[key] - before; got != 1 {
		t.Fatalf("verdicts +%d, want 1", got)
	}
}

func TestWarmupFilesGaugeNeedsTheCache(t *testing.T) {
	prev := warmup.DiskWarmup
	t.Cleanup(func() { warmup.DiskWarmup = prev })

	warmup.DiskWarmup = nil
	if strings.Contains(scrape(t).Body.String(), "flodni_warmup_files") {
		t.Fatal("flodni_warmup_files present with the cache off")
	}
	warmup.DiskWarmup = &warmup.DiskWarmupCache{}
	body := scrape(t).Body.String()
	for _, want := range []string{`flodni_warmup_files{window="head"} 0`, `flodni_warmup_files{window="tail"} 0`} {
		if !strings.Contains(body, want+"\n") {
			t.Errorf("missing %q", want)
		}
	}
}

// TestRuntimeAndProcessFamilyTypes pins the type line of each new family, the open
// sessions gauge against the same registry /api/sessions reads, and the descriptor
// gauge where /proc exists.
func TestRuntimeAndProcessFamilyTypes(t *testing.T) {
	body := scrape(t).Body.String()
	for fam, typ := range map[string]string{
		"flodni_engine_short_reads_total":        "counter",
		"flodni_engine_short_read_repairs_total": "counter",
		"flodni_sessions_open":                   "gauge",
		"go_goroutines":                          "gauge",
		"go_memstats_heap_inuse_bytes":           "gauge",
		"go_memstats_sys_bytes":                  "gauge",
		"go_gc_cycles_total":                     "counter",
	} {
		if !strings.Contains(body, "# TYPE "+fam+" "+typ+"\n") {
			t.Errorf("%s is not declared as a %s", fam, typ)
		}
	}
	want := openSessionCount()
	if got := len(openSessionRecords()); got != want {
		t.Fatalf("open sessions: gauge counts %d, /api/sessions lists %d", want, got)
	}
	if _, err := os.Stat("/proc/self/fd"); err == nil {
		if !strings.Contains(body, "# TYPE process_open_fds gauge\n") {
			t.Error("process_open_fds missing where /proc exists")
		}
	} else if strings.Contains(body, "process_open_fds") {
		t.Error("process_open_fds exported without /proc")
	}
}
