package daemon

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flodni/internal/apiauth"
	"flodni/internal/config"
	"flodni/internal/flog"
)

// controlAPIServer serves the real /api/config and /api/restart handlers behind the
// real bearer middleware, with the control token fixed at start-up the way main fixes
// it, over a config.json holding fileToken. restarts counts the stubbed restart.
func controlAPIServer(t *testing.T, startToken, fileToken string) (h http.Handler, cfgPath string, restarts *int) {
	t.Helper()
	cfgPath = filepath.Join(t.TempDir(), "config.json")
	body := `{"log_level":"info"}`
	if fileToken != "" {
		body = `{"log_level":"info","api_token":"` + fileToken + `"}`
	}
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MKV_PROXY_CONFIG_PATH", cfgPath)
	t.Setenv("FLODNI_API_TOKEN", "")

	prevCfg, prevLog, prevTok, prevRestart := globalConfig.Load(), logger, controlToken, restartSelf
	cfg := config.LoadConfig()
	globalConfig.Store(&cfg)
	logger = flog.NewText(io.Discard)
	controlToken = startToken
	n := 0
	restarts = &n
	restartSelf = func() { n++ }
	t.Cleanup(func() {
		globalConfig.Store(prevCfg)
		logger, controlToken, restartSelf = prevLog, prevTok, prevRestart
	})

	mux := http.NewServeMux()
	mux.HandleFunc("/api/config", handleConfig)
	mux.HandleFunc("/api/restart", handleRestart)
	return apiauth.Middleware(controlToken, nil, mux), cfgPath, restarts
}

func doControl(h http.Handler, path, body, bearer string) (int, string) {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func fileToken(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		APIToken string `json:"api_token"`
	}
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	return c.APIToken
}

func TestOpenPortRefusesSettingToken(t *testing.T) {
	h, path, _ := controlAPIServer(t, "", "")
	before, _ := os.ReadFile(path)
	for _, body := range []string{
		`{"log_level":"debug","api_token":"attacker"}`,
		`{"api_token":"attacker"}`,
	} {
		code, resp := doControl(h, "/api/config", body, "")
		if code != http.StatusForbidden {
			t.Fatalf("open port, token in body %s: status %d, want 403", body, code)
		}
		if !strings.Contains(resp, "FLODNI_API_TOKEN") || !strings.Contains(resp, "config.json") || !json.Valid([]byte(resp)) {
			t.Fatalf("refusal must be JSON naming FLODNI_API_TOKEN and config.json, got %q", resp)
		}
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatalf("file changed on refusal:\n%s\n->\n%s", before, after)
	}
	if got := gc().ResolveAPIToken(""); got != "" {
		t.Fatalf("in-memory token changed: %q", got)
	}
}

func TestOpenPortAcceptsSaveWithoutNewToken(t *testing.T) {
	h, path, _ := controlAPIServer(t, "", "")
	for _, body := range []string{
		`{"log_level":"debug"}`,                // absent
		`{"log_level":"debug","api_token":""}`, // echoes the empty value
	} {
		if code, resp := doControl(h, "/api/config", body, ""); code != http.StatusOK {
			t.Fatalf("open port save %s: status %d (%s), want 200", body, code, resp)
		}
	}
	if tok := fileToken(t, path); tok != "" {
		t.Fatalf("token written by an open save: %q", tok)
	}
}

func TestOpenPortRefusesRestart(t *testing.T) {
	h, _, restarts := controlAPIServer(t, "", "")
	code, resp := doControl(h, "/api/restart", "", "")
	if code != http.StatusForbidden {
		t.Fatalf("open port restart: status %d, want 403", code)
	}
	if !json.Valid([]byte(resp)) || !strings.Contains(resp, "FLODNI_API_TOKEN") {
		t.Fatalf("refusal must be JSON naming FLODNI_API_TOKEN, got %q", resp)
	}
	if *restarts != 0 {
		t.Fatal("restart action ran on a refused request")
	}
}

func TestTokenModeAuthenticatedTokenChange(t *testing.T) {
	h, path, _ := controlAPIServer(t, "old-token", "old-token")
	if code, _ := doControl(h, "/api/config", `{"api_token":"new-token"}`, ""); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated change: status %d, want 401", code)
	}
	if code, resp := doControl(h, "/api/config", `{"api_token":"new-token"}`, "old-token"); code != http.StatusOK {
		t.Fatalf("authenticated change: status %d (%s), want 200", code, resp)
	}
	if tok := fileToken(t, path); tok != "new-token" {
		t.Fatalf("file token = %q, want new-token", tok)
	}
}

func TestTokenModeAuthenticatedRestart(t *testing.T) {
	h, _, restarts := controlAPIServer(t, "tok", "tok")
	if code, _ := doControl(h, "/api/restart", "", ""); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated restart: status %d, want 401", code)
	}
	if *restarts != 0 {
		t.Fatal("restart ran without a token")
	}
	if code, _ := doControl(h, "/api/restart", "", "tok"); code != http.StatusOK {
		t.Fatalf("authenticated restart: status %d, want 200", code)
	}
	if *restarts != 1 {
		t.Fatalf("restart action ran %d times, want 1", *restarts)
	}
}
