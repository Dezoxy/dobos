package main

import (
	"strings"
	"testing"
)

func conv(t *testing.T, body string) (string, []string) {
	t.Helper()
	src := "package p\n\nimport (\n\t\"log\"\n\t\"os\"\n)\n\nvar _ = os.Stdout\n\nvar logger = log.New(os.Stdout, \"[GoProxy] \", log.LstdFlags)\n\nfunc f(hash string, err error, n int, path string) {\n" + body + "\n}\n"
	out, _, _, manual, err := convert("p.go", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	return string(out), manual
}

func TestConvert(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"info with fields", `logger.Printf("[X] added %s: %v", hash, err)`, `logger.Info(fmt.Sprintf("[X] added %s: %v", hash, err), "hash", hash, "err", err)`},
		{"warning level", `logger.Printf("WARNING: bad %d", n)`, `logger.Warn(fmt.Sprintf("WARNING: bad %d", n), "n", n)`},
		{"error level", `logger.Printf("[R] ERROR: bad")`, `logger.Error("[R] ERROR: bad")`},
		{"debug level", `logger.Printf("[DEBUG] x %s", hash)`, `logger.Debug(fmt.Sprintf("[DEBUG] x %s", hash), "hash", hash)`},
		{"float verb gets no field", `logger.Printf("took %.1f", float64(n))`, `logger.Info(fmt.Sprintf("took %.1f", float64(n)))`},
		{"base and slice", `logger.Printf("x %s %s", filepath.Base(path), hash[:8])`, `"file", filepath.Base(path), "hash", hash`},
		{"payload line gets no fields", `logger.Printf("%s%s", "[Session] ", hash)`, `logger.Info(fmt.Sprintf("%s%s", "[Session] ", hash))`},
		{"percent literal", `logger.Printf("100%%")`, `logger.Info(fmt.Sprintf("100%%"))`},
		{"dynamic format", `logger.Printf(path, n)`, `flog.Printf(logger, path, n)`},
		{"print string", `logger.Print(hash)`, `logger.Info(fmt.Sprint(hash))`},
		{"println literal", `logger.Println("hello")`, `logger.Info("hello")`},
		{"std log", `log.Printf("[Config] WARNING: x %s", path)`, `flog.Std.Warn(fmt.Sprintf("[Config] WARNING: x %s", path), "path", path)`},
		{"method value", `g := logger.Printf; g("x")`, `flog.Logf(logger)`},
		{"new logger", `_ = log.New(os.Stdout, "", 0)`, `flog.NewText(os.Stdout)`},
		{"named logger", `_ = log.New(os.Stdout, "[DiskWarmup] ", log.LstdFlags)`, `flog.New(os.Stdout, "DiskWarmup", true)`},
	}
	for _, c := range cases {
		out, _ := conv(t, c.in)
		if !strings.Contains(out, c.want) {
			t.Errorf("%s: want %q in\n%s", c.name, c.want, out)
		}
	}
}

func TestImportsAndFixedPoint(t *testing.T) {
	out, _ := conv(t, `logger.Printf("a %s", hash)`)
	for _, imp := range []string{`"fmt"`, `"flodni/internal/flog"`} {
		if !strings.Contains(out, imp) {
			t.Errorf("missing import %s:\n%s", imp, out)
		}
	}
	if strings.Contains(out, "\t\"log\"\n") {
		t.Errorf("unused log import kept:\n%s", out)
	}
	again, _, _, _, err := convert("p.go", []byte(out))
	if err != nil || string(again) != out {
		t.Errorf("not a fixed point (err=%v)", err)
	}
}

func TestManualList(t *testing.T) {
	_, manual := conv(t, `log.Fatal(err)`)
	if len(manual) != 1 || !strings.Contains(manual[0], "log.Fatal") {
		t.Errorf("manual = %v", manual)
	}
}

func TestRerunOnConvertedCodeIsQuiet(t *testing.T) {
	out, _ := conv(t, `logger.Printf("a %s", hash)`)
	_, _, _, manual, err := convert("p.go", []byte(out))
	if err != nil || len(manual) != 0 {
		t.Errorf("err=%v manual=%v", err, manual)
	}
}
