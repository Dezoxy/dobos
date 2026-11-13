package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// warm_catchup_per_hour is the pace of catch-up (Readiness step 3): files started per
// hour, 20 by default, 0 off, and a negative value falls back to the default.

func loadWith(t *testing.T, body string) Config {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MKV_PROXY_CONFIG_PATH", p)
	return LoadConfig()
}

func TestWarmCatchupPerHourDefaultsToTwenty(t *testing.T) {
	if got := LoadConfig().WarmCatchupPerHour; got != 20 {
		t.Fatalf("warm_catchup_per_hour = %d, want 20", got)
	}
}

func TestWarmCatchupPerHourZeroStaysOffAndNegativeFallsBack(t *testing.T) {
	if got := loadWith(t, `{"warm_catchup_per_hour": 0}`).WarmCatchupPerHour; got != 0 {
		t.Fatalf("an explicit 0 became %d: 0 turns catch-up off", got)
	}
	if got := loadWith(t, `{"warm_catchup_per_hour": 7}`).WarmCatchupPerHour; got != 7 {
		t.Fatalf("7 became %d", got)
	}
	if got := loadWith(t, `{"warm_catchup_per_hour": -3}`).WarmCatchupPerHour; got != 20 {
		t.Fatalf("a negative value became %d, want the default 20", got)
	}
}

func TestWarmCatchupPerHourSurvivesThePanelRoundTrip(t *testing.T) {
	in := Config{WarmCatchupPerHour: 5}
	b, err := json.Marshal(in.Redacted())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"warm_catchup_per_hour":5`) {
		t.Fatalf("GET /api/config would not carry the key: %s", b)
	}
	var out Config
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	b, _ = json.Marshal(out)
	if out.WarmCatchupPerHour != 5 || !strings.Contains(string(b), `"warm_catchup_per_hour":5`) {
		t.Fatalf("lost in the round trip: %d, %s", out.WarmCatchupPerHour, b)
	}
}

func TestConfigExampleCarriesTheCatchupPace(t *testing.T) {
	raw, err := os.ReadFile("../../config.json.example")
	if err != nil {
		t.Fatal(err)
	}
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"warm_catchup_per_hour": 20`) || c.WarmCatchupPerHour != 20 {
		t.Fatal(`config.json.example must carry "warm_catchup_per_hour": 20`)
	}
}
