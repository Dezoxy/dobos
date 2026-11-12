// Package promtext writes the Prometheus text exposition format, version 0.0.4,
// and checks output against it. It exists so the daemon can serve a scrape
// endpoint without a client library: every histogram in the daemon is already
// a set of atomic counters, so the only missing piece is the encoder.
//
// Each Writer method emits one whole metric family, so a family is contiguous
// by construction. Mistakes (a bad name, a counter without _total, a duplicate
// family or label set, unsorted bounds, a caller-supplied le) are recorded and
// returned by Err; the bytes are not served when Err is set.
package promtext

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// ContentType is the Content-Type of a text format 0.0.4 response.
const ContentType = "text/plain; version=0.0.4; charset=utf-8"

var (
	metricNameRE = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)
	labelNameRE  = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
)

// Label is one name and value pair of a series.
type Label struct{ Name, Value string }

// Sample is one series of a counter or gauge.
type Sample struct {
	Labels []Label
	Value  float64
}

// Histogram is one labelled histogram of a family. Counts holds the count of
// each bucket on its own (not cumulative), one per bound plus a final one for
// everything above the last bound, so len(Counts) == len(Bounds)+1. The writer
// accumulates, and takes _count from the same counts so it always equals the
// +Inf bucket.
type Histogram struct {
	Labels []Label
	Bounds []float64
	Counts []int64
	Sum    float64
}

// Writer builds one exposition. The zero value is ready to use.
type Writer struct {
	buf      bytes.Buffer
	err      error
	families map[string]bool
}

// Bytes returns what has been written so far.
func (w *Writer) Bytes() []byte { return w.buf.Bytes() }

// Err returns the first mistake recorded, or nil.
func (w *Writer) Err() error { return w.err }

func (w *Writer) fail(format string, args ...any) {
	if w.err == nil {
		w.err = fmt.Errorf(format, args...)
	}
}

// Counter writes a counter family. The name must end in _total.
func (w *Writer) Counter(name, help string, s ...Sample) {
	if !strings.HasSuffix(name, "_total") {
		w.fail("promtext: counter %q does not end in _total", name)
		return
	}
	w.scalar(name, help, "counter", s)
}

// Gauge writes a gauge family.
func (w *Writer) Gauge(name, help string, s ...Sample) {
	w.scalar(name, help, "gauge", s)
}

func (w *Writer) scalar(name, help, typ string, samples []Sample) {
	if !w.header(name, help, typ) {
		return
	}
	seen := map[string]bool{}
	for _, s := range samples {
		key, ok := w.labelKey(name, s.Labels, false)
		if !ok {
			return
		}
		if seen[key] {
			w.fail("promtext: %s: duplicate label set %s", name, key)
			return
		}
		seen[key] = true
		w.sample(name, s.Labels, "", s.Value)
	}
}

// Histogram writes a histogram family. The seconds, buckets and sum are the
// caller's units; this package only encodes them.
func (w *Writer) Histogram(name, help string, hs ...Histogram) {
	if !w.header(name, help, "histogram") {
		return
	}
	seen := map[string]bool{}
	for _, h := range hs {
		key, ok := w.labelKey(name, h.Labels, true)
		if !ok {
			return
		}
		if seen[key] {
			w.fail("promtext: %s: duplicate label set %s", name, key)
			return
		}
		seen[key] = true
		if len(h.Counts) != len(h.Bounds)+1 {
			w.fail("promtext: %s: %d counts for %d bounds", name, len(h.Counts), len(h.Bounds))
			return
		}
		for i, b := range h.Bounds {
			if math.IsNaN(b) || math.IsInf(b, 0) || (i > 0 && b <= h.Bounds[i-1]) {
				w.fail("promtext: %s: bounds are not finite and strictly ascending", name)
				return
			}
		}
		var cum int64
		for i, c := range h.Counts {
			if c < 0 {
				w.fail("promtext: %s: negative bucket count", name)
				return
			}
			cum += c
			le := "+Inf"
			if i < len(h.Bounds) {
				le = formatValue(h.Bounds[i])
			}
			w.sample(name+"_bucket", h.Labels, le, float64(cum))
		}
		w.sample(name+"_sum", h.Labels, "", h.Sum)
		w.sample(name+"_count", h.Labels, "", float64(cum))
	}
}

// header writes HELP and TYPE and registers the family name.
func (w *Writer) header(name, help, typ string) bool {
	if w.err != nil {
		return false
	}
	if !metricNameRE.MatchString(name) {
		w.fail("promtext: invalid metric name %q", name)
		return false
	}
	if w.families == nil {
		w.families = map[string]bool{}
	}
	if w.families[name] {
		w.fail("promtext: family %q written twice", name)
		return false
	}
	w.families[name] = true
	w.buf.WriteString("# HELP " + name + " " + escapeHelp(help) + "\n")
	w.buf.WriteString("# TYPE " + name + " " + typ + "\n")
	return true
}

// labelKey validates one label set and returns a key that identifies it.
func (w *Writer) labelKey(family string, ls []Label, histogram bool) (string, bool) {
	var key strings.Builder
	names := map[string]bool{}
	for _, l := range ls {
		switch {
		case !labelNameRE.MatchString(l.Name):
			w.fail("promtext: %s: invalid label name %q", family, l.Name)
			return "", false
		case strings.HasPrefix(l.Name, "__"):
			w.fail("promtext: %s: label name %q is reserved", family, l.Name)
			return "", false
		case histogram && l.Name == "le":
			w.fail("promtext: %s: le is reserved on a histogram", family)
			return "", false
		case names[l.Name]:
			w.fail("promtext: %s: label %q repeated", family, l.Name)
			return "", false
		}
		names[l.Name] = true
		key.WriteString(l.Name + "=" + strconv.Quote(l.Value) + ",")
	}
	return key.String(), true
}

// sample writes one line. A non-empty le is appended as the last label.
func (w *Writer) sample(name string, ls []Label, le string, v float64) {
	w.buf.WriteString(name)
	if len(ls) > 0 || le != "" {
		w.buf.WriteByte('{')
		for i, l := range ls {
			if i > 0 {
				w.buf.WriteByte(',')
			}
			w.buf.WriteString(l.Name + `="` + escapeLabel(l.Value) + `"`)
		}
		if le != "" {
			if len(ls) > 0 {
				w.buf.WriteByte(',')
			}
			w.buf.WriteString(`le="` + le + `"`)
		}
		w.buf.WriteByte('}')
	}
	w.buf.WriteByte(' ')
	w.buf.WriteString(formatValue(v))
	w.buf.WriteByte('\n')
}

// formatValue prints a whole number as an integer (so 1000000 is not 1e+06)
// and anything else as the shortest float that round-trips.
func formatValue(v float64) string {
	switch {
	case math.IsNaN(v):
		return "NaN"
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	case v == math.Trunc(v) && math.Abs(v) < 1e15:
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

var (
	helpEscaper  = strings.NewReplacer(`\`, `\\`, "\n", `\n`)
	labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
)

func escapeHelp(s string) string  { return helpEscaper.Replace(s) }
func escapeLabel(s string) string { return labelEscaper.Replace(s) }

// errEmpty is returned by Lint for an empty exposition.
var errEmpty = errors.New("promtext: empty exposition")
