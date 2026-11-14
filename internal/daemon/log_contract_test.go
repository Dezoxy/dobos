package daemon

import (
	"bytes"
	"encoding/json"
	"flodni/internal/flog"
	"regexp"
	"strings"
	"testing"
)

// The documented log lines are a contract (README, "Logging"): their message
// text and JSON payload do not change with the log format. These tests render
// the real emitters in both formats and compare against the text the
// emitter built by hand before the move to slog.

var defaultPrefix = regexp.MustCompile(`^\[GoProxy\] \d{4}/\d\d/\d\d \d\d:\d\d:\d\d `)

// emitBoth runs emit once per format against a production-shaped logger and
// returns the text line (prefix and time stripped) and the JSON object.
func emitBoth(t *testing.T, emit func()) (textMsg string, obj map[string]any) {
	t.Helper()
	prev := logger
	t.Cleanup(func() {
		logger = prev
		_ = flog.Configure("text", "INFO")
	})
	for _, format := range []string{"text", "json"} {
		var buf bytes.Buffer
		logger = flog.New(&buf, "GoProxy", true)
		if err := flog.Configure(format, "INFO"); err != nil {
			t.Fatal(err)
		}
		emit()
		out := strings.TrimSuffix(buf.String(), "\n")
		if strings.Contains(out, "\n") {
			t.Fatalf("%s: more than one line: %q", format, out)
		}
		if format == "text" {
			if !defaultPrefix.MatchString(out) {
				t.Fatalf("text line has not the default [GoProxy] <time> shape: %q", out)
			}
			textMsg = defaultPrefix.ReplaceAllString(out, "")
			continue
		}
		if err := json.Unmarshal([]byte(out), &obj); err != nil {
			t.Fatalf("json line is not valid JSON: %v: %s", err, out)
		}
	}
	return textMsg, obj
}

func TestSessionLineIsByteIdenticalInBothFormats(t *testing.T) {
	rec := sessionRecord{Path: "/m/x&y.mkv", Hash: "0123456789abcdef0123456789abcdef01234567", Size: 7, OpenedAt: "2026-11-12T09:00:05Z", Outcome: "served", Reads: 3}
	payload, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	want := "[Session] " + string(payload)
	text, obj := emitBoth(t, func() { logSessionRecord(rec) })
	if text != want {
		t.Fatalf("text line:\n got %s\nwant %s", text, want)
	}
	if obj["msg"] != want || obj["level"] != "INFO" || obj["logger"] != "GoProxy" {
		t.Fatalf("json line: %v", obj)
	}
}

func TestWebhookWarningKeepsItsTextAndIsAWarning(t *testing.T) {
	line := webhookWarningLine(7, true, false)
	m := newWebhookMarks()
	text, obj := emitBoth(t, func() { m.logf("%s", line) })
	if text != line || !strings.HasPrefix(line, "WARNING: [Webhook] ") {
		t.Fatalf("text line %q, want %q", text, line)
	}
	if obj["msg"] != line || obj["level"] != "WARN" {
		t.Fatalf("json line: %v", obj)
	}
}

func TestWarmLineKeepsItsTextInBothFormats(t *testing.T) {
	// the shape warm_add hands to the logger: "[Warm] " and one JSON object
	line := `[Warm] {"hash":"0123456789abcdef0123456789abcdef01234567","file":1,"trigger":"add","outcome":"warmed"}`
	text, obj := emitBoth(t, func() { flog.Printf(logger, "%s", line) })
	if text != line || obj["msg"] != line || obj["level"] != "INFO" {
		t.Fatalf("text %q json %v", text, obj)
	}
}

func TestLogFormatAndLevelFromConfig(t *testing.T) {
	useTestGlobals(t)
	var buf bytes.Buffer
	logger = flog.New(&buf, "GoProxy", true)
	t.Cleanup(func() { _ = flog.Configure("text", "INFO") })

	cfg := *gc()
	cfg.LogFormat, cfg.LogLevel = "json", "WARN"
	globalConfig.Store(&cfg)
	applyLogConfig()
	logger.Debug("d")
	logger.Info("i")
	logger.Warn("WARNING: w")
	logger.Error("ERROR: e")
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("log_level WARN: want the warning and the error only, got %q", buf.String())
	}
	for i, level := range []string{"WARN", "ERROR"} {
		var obj map[string]any
		if err := json.Unmarshal([]byte(lines[i]), &obj); err != nil || obj["level"] != level {
			t.Fatalf("line %d: %v %v", i, err, obj)
		}
	}

	buf.Reset()
	cfg.LogFormat, cfg.LogLevel = "text", "DEBUG"
	globalConfig.Store(&cfg)
	applyLogConfig()
	logger.Debug("[DEBUG] shown at DEBUG")
	if !strings.Contains(buf.String(), "[DEBUG] shown at DEBUG") || strings.HasPrefix(buf.String(), "{") {
		t.Fatalf("log_level DEBUG, text: %q", buf.String())
	}
}
