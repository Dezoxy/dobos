package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func okCheck() checkResult   { return checkResult{OK: true, Reason: "ok"} }
func downCheck() checkResult { return checkResult{Reason: "unreachable"} }

func serveHealth(t *testing.T, h healthChecks, method string) (*httptest.ResponseRecorder, healthReport) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.handler(rec, httptest.NewRequest(method, "/api/health", nil))
	var rep healthReport
	if method == http.MethodGet {
		if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil {
			t.Fatalf("body is not JSON: %v: %s", err, rec.Body.String())
		}
	}
	return rec, rep
}

func TestHealthHandler(t *testing.T) {
	tests := []struct {
		name              string
		mount, eng, db    func() checkResult
		wantCode          int
		wantStatus        string
		wantFailing       string
		wantFailingReason string
	}{
		{"all ok", okCheck, okCheck, okCheck, 200, "ok", "", ""},
		{"engine down", okCheck, downCheck, okCheck, 503, "degraded", "engine", "unreachable"},
		{"db down", okCheck, okCheck, downCheck, 503, "degraded", "state_db", "unreachable"},
		{"mount down", downCheck, okCheck, okCheck, 503, "down", "mount", "unreachable"},
		{"mount and engine down", downCheck, downCheck, okCheck, 503, "down", "mount", "unreachable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := healthChecks{mount: tt.mount, engine: tt.eng, stateDB: tt.db, timeout: time.Second}
			rec, rep := serveHealth(t, h, http.MethodGet)
			if rec.Code != tt.wantCode || rep.Status != tt.wantStatus {
				t.Fatalf("got %d %q, want %d %q", rec.Code, rep.Status, tt.wantCode, tt.wantStatus)
			}
			if len(rep.Checks) != 3 {
				t.Fatalf("want 3 checks, got %v", rep.Checks)
			}
			if tt.wantFailing != "" && rep.Checks[tt.wantFailing].Reason != tt.wantFailingReason {
				t.Fatalf("%s: %+v", tt.wantFailing, rep.Checks[tt.wantFailing])
			}
		})
	}
}

func TestHealthMountHangs(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	h := healthChecks{
		mount:   func() checkResult { <-release; return okCheck() },
		engine:  okCheck,
		stateDB: okCheck,
		timeout: healthCheckTimeout,
	}
	start := time.Now()
	rec, rep := serveHealth(t, h, http.MethodGet)
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("handler took %v", d)
	}
	m := rep.Checks["mount"]
	if rec.Code != 503 || rep.Status != "down" || m.OK || m.Reason != "timeout" {
		t.Fatalf("got %d %q %+v", rec.Code, rep.Status, m)
	}
}

func TestHealthReasonsCarryNoPaths(t *testing.T) {
	virtualMountPath = "/definitely/not/a/mount"
	for name, res := range map[string]checkResult{"mount": checkMount()} {
		if res.OK || strings.ContainsAny(res.Reason, "/\\") {
			t.Fatalf("%s: %+v", name, res)
		}
	}
	_, rep := serveHealth(t, healthChecks{mount: checkMount, engine: downCheck, stateDB: okCheck, timeout: time.Second}, http.MethodGet)
	for name, c := range rep.Checks {
		if strings.Contains(c.Reason, "/") {
			t.Fatalf("%s reason has a path: %q", name, c.Reason)
		}
	}
}

func TestHealthMethodNotAllowed(t *testing.T) {
	h := healthChecks{mount: okCheck, engine: okCheck, stateDB: okCheck, timeout: time.Second}
	rec, _ := serveHealth(t, h, http.MethodPost)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestRunHealthcheck(t *testing.T) {
	for _, tt := range []struct {
		code, want int
	}{{200, 0}, {503, 1}, {500, 1}} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tt.code)
			_, _ = w.Write([]byte(`{"status":"x"}`))
		}))
		if got := runHealthcheck(srv.URL); got != tt.want {
			t.Errorf("HTTP %d: exit %d, want %d", tt.code, got, tt.want)
		}
		srv.Close()
	}
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	if got := runHealthcheck(url); got != 1 {
		t.Errorf("closed port: exit %d, want 1", got)
	}
}

func TestHealthStatus(t *testing.T) {
	ok, bad := okCheck(), downCheck()
	for _, c := range []struct {
		m, e, d checkResult
		want    string
	}{{ok, ok, ok, "ok"}, {ok, bad, ok, "degraded"}, {ok, ok, bad, "degraded"}, {bad, ok, ok, "down"}, {bad, bad, bad, "down"}} {
		if got := healthStatus(c.m, c.e, c.d); got != c.want {
			t.Errorf("%+v: got %q want %q", c, got, c.want)
		}
	}
}
