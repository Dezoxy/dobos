package promtext

import (
	"strings"
	"testing"
)

func TestWriterGolden(t *testing.T) {
	var w Writer
	w.Gauge("app_info", "Line one\nline \\ two.", Sample{Labels: []Label{{"version", `v"1\x`}}, Value: 1})
	w.Counter("app_reads_total", "Reads.",
		Sample{Labels: []Label{{"source", "a"}}, Value: 1000000},
		Sample{Labels: []Label{{"source", "b"}}, Value: 0})
	w.Histogram("app_wait_seconds", "Wait.",
		Histogram{Labels: []Label{{"source", "a"}, {"kind", "x\ny"}}, Bounds: []float64{0.001, 0.5, 1}, Counts: []int64{2, 0, 1, 2}, Sum: 21.003},
		Histogram{Bounds: []float64{0.001, 0.5, 1}, Counts: []int64{0, 0, 0, 0}})
	if err := w.Err(); err != nil {
		t.Fatal(err)
	}
	want := `# HELP app_info Line one\nline \\ two.
# TYPE app_info gauge
app_info{version="v\"1\\x"} 1
# HELP app_reads_total Reads.
# TYPE app_reads_total counter
app_reads_total{source="a"} 1000000
app_reads_total{source="b"} 0
# HELP app_wait_seconds Wait.
# TYPE app_wait_seconds histogram
app_wait_seconds_bucket{source="a",kind="x\ny",le="0.001"} 2
app_wait_seconds_bucket{source="a",kind="x\ny",le="0.5"} 2
app_wait_seconds_bucket{source="a",kind="x\ny",le="1"} 3
app_wait_seconds_bucket{source="a",kind="x\ny",le="+Inf"} 5
app_wait_seconds_sum{source="a",kind="x\ny"} 21.003
app_wait_seconds_count{source="a",kind="x\ny"} 5
app_wait_seconds_bucket{le="0.001"} 0
app_wait_seconds_bucket{le="0.5"} 0
app_wait_seconds_bucket{le="1"} 0
app_wait_seconds_bucket{le="+Inf"} 0
app_wait_seconds_sum 0
app_wait_seconds_count 0
`
	if got := string(w.Bytes()); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if err := Lint(w.Bytes()); err != nil {
		t.Fatalf("the writer's own output fails Lint: %v", err)
	}
}

func TestWriterRecordsMistakes(t *testing.T) {
	ok := Histogram{Bounds: []float64{1}, Counts: []int64{0, 0}}
	tests := []struct {
		name string
		do   func(w *Writer)
	}{
		{"bad metric name", func(w *Writer) { w.Gauge("1bad", "h") }},
		{"counter without _total", func(w *Writer) { w.Counter("reads", "h") }},
		{"duplicate family", func(w *Writer) { w.Gauge("g", "h"); w.Gauge("g", "h") }},
		{"duplicate label set", func(w *Writer) {
			w.Gauge("g", "h", Sample{Labels: []Label{{"a", "1"}}}, Sample{Labels: []Label{{"a", "1"}}})
		}},
		{"bad label name", func(w *Writer) { w.Gauge("g", "h", Sample{Labels: []Label{{"a-b", "1"}}}) }},
		{"reserved label prefix", func(w *Writer) { w.Gauge("g", "h", Sample{Labels: []Label{{"__x", "1"}}}) }},
		{"repeated label", func(w *Writer) { w.Gauge("g", "h", Sample{Labels: []Label{{"a", "1"}, {"a", "2"}}}) }},
		{"le on a histogram", func(w *Writer) {
			h := ok
			h.Labels = []Label{{"le", "1"}}
			w.Histogram("h_seconds", "h", h)
		}},
		{"unsorted bounds", func(w *Writer) {
			w.Histogram("h_seconds", "h", Histogram{Bounds: []float64{2, 1}, Counts: []int64{0, 0, 0}})
		}},
		{"equal bounds", func(w *Writer) {
			w.Histogram("h_seconds", "h", Histogram{Bounds: []float64{1, 1}, Counts: []int64{0, 0, 0}})
		}},
		{"counts do not match bounds", func(w *Writer) {
			w.Histogram("h_seconds", "h", Histogram{Bounds: []float64{1}, Counts: []int64{0}})
		}},
		{"negative count", func(w *Writer) {
			w.Histogram("h_seconds", "h", Histogram{Bounds: []float64{1}, Counts: []int64{-1, 0}})
		}},
		{"duplicate histogram label set", func(w *Writer) { w.Histogram("h_seconds", "h", ok, ok) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var w Writer
			tt.do(&w)
			if w.Err() == nil {
				t.Fatalf("no error recorded; output:\n%s", w.Bytes())
			}
		})
	}
}

func TestLintAccepts(t *testing.T) {
	good := "# HELP a_total x\n# TYPE a_total counter\na_total 1\n" +
		"# HELP g y\n# TYPE g gauge\ng{l=\"v\\\"\\\\\\n\"} -1.5e3\ng{l=\"w\"} NaN\n"
	if err := Lint([]byte(good)); err != nil {
		t.Fatal(err)
	}
}

func TestLintRejects(t *testing.T) {
	hist := func(body string) string {
		return "# HELP h_seconds x\n# TYPE h_seconds histogram\n" + body
	}
	tests := []struct{ name, in string }{
		{"empty", ""},
		{"no trailing newline", "# HELP g x\n# TYPE g gauge\ng 1"},
		{"blank line", "# HELP g x\n\n# TYPE g gauge\ng 1\n"},
		{"sample before TYPE", "g 1\n# HELP g x\n# TYPE g gauge\n"},
		{"TYPE without HELP", "# TYPE g gauge\ng 1\n"},
		{"unknown comment", "# EOF\n"},
		{"counter without _total", "# HELP c x\n# TYPE c counter\nc 1\n"},
		{"negative counter", "# HELP c_total x\n# TYPE c_total counter\nc_total -1\n"},
		{"timestamp", "# HELP g x\n# TYPE g gauge\ng 1 1700000000\n"},
		{"bad value", "# HELP g x\n# TYPE g gauge\ng one\n"},
		{"bad name", "# HELP g x\n# TYPE g gauge\ng-x 1\n"},
		{"unquoted label", "# HELP g x\n# TYPE g gauge\ng{a=1} 1\n"},
		{"bad escape", "# HELP g x\n# TYPE g gauge\ng{a=\"\\t\"} 1\n"},
		{"unterminated label", "# HELP g x\n# TYPE g gauge\ng{a=\"x} 1\n"},
		{"duplicate series", "# HELP g x\n# TYPE g gauge\ng{a=\"1\"} 1\ng{a=\"1\"} 2\n"},
		{"duplicate series, labels reordered", "# HELP g x\n# TYPE g gauge\ng{a=\"1\",b=\"2\"} 1\ng{b=\"2\",a=\"1\"} 2\n"},
		{"interleaved families", "# HELP a x\n# TYPE a gauge\n# HELP b x\n# TYPE b gauge\na 1\nb 1\n"},
		{"family repeated", "# HELP a x\n# TYPE a gauge\na 1\n# HELP b x\n# TYPE b gauge\nb 1\n# TYPE a gauge\n"},
		{"unsupported type", "# HELP s x\n# TYPE s summary\n"},
		{"buckets not ascending", hist("h_seconds_bucket{le=\"1\"} 1\nh_seconds_bucket{le=\"0.5\"} 1\nh_seconds_bucket{le=\"+Inf\"} 1\nh_seconds_sum 1\nh_seconds_count 1\n")},
		{"buckets not cumulative", hist("h_seconds_bucket{le=\"1\"} 2\nh_seconds_bucket{le=\"+Inf\"} 1\nh_seconds_sum 1\nh_seconds_count 1\n")},
		{"no +Inf", hist("h_seconds_bucket{le=\"1\"} 1\nh_seconds_sum 1\nh_seconds_count 1\n")},
		{"count differs from +Inf", hist("h_seconds_bucket{le=\"+Inf\"} 2\nh_seconds_sum 1\nh_seconds_count 1\n")},
		{"no count", hist("h_seconds_bucket{le=\"+Inf\"} 1\nh_seconds_sum 1\n")},
		{"bucket after sum", hist("h_seconds_bucket{le=\"1\"} 1\nh_seconds_bucket{le=\"+Inf\"} 1\nh_seconds_sum 1\nh_seconds_bucket{le=\"2\"} 1\nh_seconds_count 1\n")},
		{"bucket without le", hist("h_seconds_bucket 1\n")},
		{"bare histogram sample", hist("h_seconds 1\n")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := Lint([]byte(tt.in)); err == nil {
				t.Fatalf("Lint accepted:\n%s", tt.in)
			}
		})
	}
}

func TestFormatValue(t *testing.T) {
	for in, want := range map[float64]string{0: "0", 1000000: "1000000", 0.001: "0.001", 21.003: "21.003", -2: "-2", 1e20: "1e+20"} {
		if got := formatValue(in); got != want {
			t.Errorf("formatValue(%v) = %q, want %q", in, got, want)
		}
	}
	if !strings.Contains(ContentType, "version=0.0.4") {
		t.Fatal("content type lost its version")
	}
}
