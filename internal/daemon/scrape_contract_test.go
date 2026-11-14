package daemon

import (
	"encoding/json"
	"testing"
)

// The fields of the experimental scrape objects that a consumer reads. They are stable:
// renaming one, dropping it, or making it omitempty breaks callers, so every key must be
// present in every object, null included, with the JSON type listed here. Fields not
// listed (leechers, scraped_at, unscrapable, finished_at) stay experimental.
var stableScrapeKeys = map[string]string{
	"seeders":  "number|null",
	"answered": "number",
	"failed":   "number",
	"unknown":  "number",
	"source":   "string|null",
	"error":    "string|null",
	"run_id":   "number",
}

var stableScrapePassKeys = map[string]string{"run_id": "number"}

func jsonKind(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case float64:
		return "number"
	case string:
		return "string"
	default:
		return "other"
	}
}

func checkStableKeys(t *testing.T, what string, obj any, want map[string]string) {
	t.Helper()
	data, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	for key, kinds := range want {
		v, ok := m[key]
		if !ok {
			t.Errorf("%s: stable key %q missing from %s", what, key, data)
			continue
		}
		k := jsonKind(v)
		if k != kinds && !(kinds == "number|null" && (k == "number" || k == "null")) &&
			!(kinds == "string|null" && (k == "string" || k == "null")) {
			t.Errorf("%s: key %q is %s, want %s", what, key, k, kinds)
		}
	}
}

func TestScrapeStableFieldsAlwaysPresent(t *testing.T) {
	five, msg, src := 5, "unknown_to_tracker", "http"
	// An entry with numbers, one with an error and null numbers, and a zero value.
	checkStableKeys(t, "answered", scrapeHealth{Seeders: &five, Source: &src, Answered: 1, RunID: 3}, stableScrapeKeys)
	checkStableKeys(t, "error", scrapeHealth{Error: &msg, Unknown: 1, RunID: 3}, stableScrapeKeys)
	checkStableKeys(t, "zero", scrapeHealth{}, stableScrapeKeys)
	checkStableKeys(t, "scrape_pass", scrapePassHealth{RunID: 3, FinishedAt: "2026-11-14T00:00:00Z"}, stableScrapePassKeys)
}
