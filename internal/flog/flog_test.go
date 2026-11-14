package flog

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func reset(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { _ = Configure("text", "INFO") })
	if err := Configure("text", "INFO"); err != nil {
		t.Fatal(err)
	}
}

func TestTextKeepsTheOldShape(t *testing.T) {
	reset(t)
	var buf bytes.Buffer
	l := New(&buf, "GoProxy", false)
	l.Info("[Session] {\"a\":1}")
	l.Warn("WARNING: x failed: boom", "err", errors.New("boom"), "path", "/a b")
	want := "[GoProxy] [Session] {\"a\":1}\n[GoProxy] WARNING: x failed: boom path=\"/a b\"\n"
	if buf.String() != want {
		t.Fatalf("got %q want %q", buf.String(), want)
	}
	buf.Reset()
	NewText(&buf).Info("plain")
	if buf.String() != "plain\n" {
		t.Fatalf("got %q", buf.String())
	}
}

func TestBytesFieldIsComparedAsText(t *testing.T) {
	reset(t)
	var buf bytes.Buffer
	l := NewText(&buf)
	payload := []byte(`{"a":1}`)
	l.Info("[Session] "+string(payload), "line", payload)
	if buf.String() != "[Session] {\"a\":1}\n" {
		t.Fatalf("got %q", buf.String())
	}
}

func TestTextTimeStamp(t *testing.T) {
	reset(t)
	var buf bytes.Buffer
	New(&buf, "DiskWarmup", true).Info("hello")
	if !strings.HasPrefix(buf.String(), "[DiskWarmup] 20") || !strings.HasSuffix(buf.String(), " hello\n") {
		t.Fatalf("got %q", buf.String())
	}
}

func TestJSONIsOneValidObjectPerLine(t *testing.T) {
	reset(t)
	if err := SetFormat("json"); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	l := New(&buf, "GoProxy", true)
	l.Info("[Session] {\"path\":\"/x\"}", "path", "/x")
	l.Warn("WARNING: multi\nline", "n", 3)
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d: %q", len(lines), buf.String())
	}
	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("not JSON: %v: %s", err, lines[0])
	}
	if first["msg"] != "[Session] {\"path\":\"/x\"}" || first["level"] != "INFO" || first["logger"] != "GoProxy" || first["path"] != "/x" {
		t.Fatalf("fields: %v", first)
	}
	var second map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil || second["level"] != "WARN" {
		t.Fatalf("second: %v %v", err, second)
	}
}

func TestLevelsAreHonoured(t *testing.T) {
	reset(t)
	var buf bytes.Buffer
	l := NewText(&buf)
	emit := func() {
		buf.Reset()
		l.Debug("d")
		l.Info("i")
		l.Warn("w")
		l.Error("e")
	}
	cases := map[string]string{"INFO": "i\nw\ne\n", "": "i\nw\ne\n", "bogus": "i\nw\ne\n", "DEBUG": "d\ni\nw\ne\n", "WARN": "w\ne\n", "WARNING": "w\ne\n", "ERROR": "e\n"}
	for level, want := range cases {
		SetLevel(level)
		emit()
		if buf.String() != want {
			t.Errorf("level %q: got %q want %q", level, buf.String(), want)
		}
	}
}

func TestClassify(t *testing.T) {
	cases := map[string]slog.Level{
		"[Config] WARNING: Failed to parse %s: %v": slog.LevelWarn,
		"WARNING: a":                           slog.LevelWarn,
		"[StateDB] Warning: failed to rename":  slog.LevelWarn,
		"BLACKLIST WARNING: %s was unreadable": slog.LevelWarn,
		"[Registry] ERROR: could not read":     slog.LevelError,
		"READDIR ERROR: %v":                    slog.LevelError,
		"[PANIC] wake goroutine recovered":     slog.LevelError,
		"[DEBUG] Webhook received: %s":         slog.LevelDebug,
		"[Session] {\"path\":\"/x\"}":          slog.LevelInfo,
		"[Pump] reader is nil at startup":      slog.LevelInfo,
		"Failed to open StateDB":               slog.LevelInfo,
	}
	for in, want := range cases {
		if got := Classify(in); got != want {
			t.Errorf("Classify(%q) = %v, want %v", in, got, want)
		}
	}
	long := strings.Repeat("x", 80) + " WARNING"
	if Classify(long) != slog.LevelInfo {
		t.Error("a word past the window must not change the level")
	}
}

func TestPrintfClassifiesTheRenderedText(t *testing.T) {
	reset(t)
	var buf bytes.Buffer
	l := NewText(&buf)
	SetLevel("WARN")
	Printf(l, "info %d", 1)
	Printf(l, "WARNING: %s", "x")
	Logf(l)("ERROR: %v", errors.New("e"))
	if buf.String() != "WARNING: x\nERROR: e\n" {
		t.Fatalf("got %q", buf.String())
	}
}

func TestSetFormatRejectsUnknown(t *testing.T) {
	reset(t)
	if err := SetFormat("xml"); err == nil || Format() != "text" {
		t.Fatalf("err=%v format=%s", err, Format())
	}
	if err := SetFormat("JSON"); err != nil || Format() != "json" {
		t.Fatalf("err=%v format=%s", err, Format())
	}
}
