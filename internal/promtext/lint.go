package promtext

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Lint checks an exposition against the text format 0.0.4 strictly enough to
// catch what a scraper would reject or misread: grammar, a trailing newline,
// HELP then TYPE before the samples of a family, families that are not
// contiguous, duplicate series, counters without _total or below zero, and
// histograms whose buckets are not ascending, cumulative and closed by +Inf
// with a _count equal to it. It does not parse timestamps: the writer emits
// none, so a sample with one is an error.
func Lint(b []byte) error {
	if len(b) == 0 {
		return errEmpty
	}
	if b[len(b)-1] != '\n' {
		return fmt.Errorf("promtext: no trailing newline")
	}
	l := &linter{
		types:  map[string]string{},
		helped: map[string]bool{},
		series: map[string]bool{},
		hists:  map[string]*histState{},
	}
	for i, line := range strings.Split(string(b[:len(b)-1]), "\n") {
		if err := l.line(line); err != nil {
			return fmt.Errorf("promtext: line %d: %w (%q)", i+1, err, line)
		}
	}
	keys := make([]string, 0, len(l.hists))
	for k := range l.hists {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		h := l.hists[k]
		switch {
		case !h.infSeen:
			return fmt.Errorf("promtext: histogram %s has no +Inf bucket", k)
		case !h.sumSeen || !h.countSeen:
			return fmt.Errorf("promtext: histogram %s lacks _sum or _count", k)
		case h.count != h.inf:
			return fmt.Errorf("promtext: histogram %s: _count %v differs from +Inf bucket %v", k, h.count, h.inf)
		}
	}
	return nil
}

type histState struct {
	lastLE, lastCum  float64
	buckets          int
	infSeen, sumSeen bool
	countSeen        bool
	inf, count       float64
	afterBuckets     bool // a _sum or _count was seen
}

type linter struct {
	types   map[string]string // family -> type
	helped  map[string]bool
	current string // family of the last TYPE line
	series  map[string]bool
	hists   map[string]*histState
}

func (l *linter) line(s string) error {
	if s == "" {
		return fmt.Errorf("empty line")
	}
	if strings.HasPrefix(s, "#") {
		return l.comment(s)
	}
	return l.sample(s)
}

func (l *linter) comment(s string) error {
	parts := strings.SplitN(s, " ", 4)
	if len(parts) < 3 || parts[0] != "#" {
		return fmt.Errorf("malformed comment")
	}
	name := parts[2]
	if !metricNameRE.MatchString(name) {
		return fmt.Errorf("invalid metric name")
	}
	switch parts[1] {
	case "HELP":
		if len(parts) != 4 {
			return fmt.Errorf("HELP without text")
		}
		if l.helped[name] {
			return fmt.Errorf("second HELP for %s", name)
		}
		if err := checkEscapes(parts[3], false); err != nil {
			return err
		}
		l.helped[name] = true
	case "TYPE":
		if len(parts) != 4 {
			return fmt.Errorf("TYPE without a type")
		}
		if _, dup := l.types[name]; dup {
			return fmt.Errorf("second TYPE for %s: family is not contiguous", name)
		}
		if !l.helped[name] {
			return fmt.Errorf("TYPE for %s without a HELP before it", name)
		}
		switch parts[3] {
		case "counter":
			if !strings.HasSuffix(name, "_total") {
				return fmt.Errorf("counter %s does not end in _total", name)
			}
		case "gauge", "histogram":
		default:
			return fmt.Errorf("unsupported type %q", parts[3])
		}
		l.types[name] = parts[3]
		l.current = name
	default:
		return fmt.Errorf("comment is neither HELP nor TYPE")
	}
	return nil
}

func (l *linter) sample(s string) error {
	name, labels, rest, err := splitSample(s)
	if err != nil {
		return err
	}
	fields := strings.Fields(rest)
	if len(fields) != 1 {
		return fmt.Errorf("want exactly a value after the labels (no timestamp)")
	}
	v, err := parseValue(fields[0])
	if err != nil {
		return err
	}
	fam, suffix := l.family(name)
	if fam == "" {
		return fmt.Errorf("sample %s has no TYPE before it", name)
	}
	if fam != l.current {
		return fmt.Errorf("sample of %s outside its family: family is not contiguous", fam)
	}
	key := name + "{" + canonical(labels, "") + "}"
	if l.series[key] {
		return fmt.Errorf("duplicate series %s", key)
	}
	l.series[key] = true
	switch l.types[fam] {
	case "counter":
		if v < 0 {
			return fmt.Errorf("counter below zero")
		}
	case "histogram":
		return l.histSample(fam, suffix, labels, v)
	}
	return nil
}

// family maps a sample name to its declared family and the histogram suffix.
func (l *linter) family(name string) (string, string) {
	if _, ok := l.types[name]; ok && l.types[name] != "histogram" {
		return name, ""
	}
	for _, suf := range []string{"_bucket", "_sum", "_count"} {
		if base, ok := strings.CutSuffix(name, suf); ok && l.types[base] == "histogram" {
			return base, suf
		}
	}
	return "", ""
}

func (l *linter) histSample(fam, suffix string, labels [][2]string, v float64) error {
	if suffix == "" {
		return fmt.Errorf("histogram %s sample without _bucket, _sum or _count", fam)
	}
	group := fam + "{" + canonical(labels, "le") + "}"
	h := l.hists[group]
	if h == nil {
		h = &histState{lastLE: math.Inf(-1)}
		l.hists[group] = h
	}
	switch suffix {
	case "_bucket":
		if h.afterBuckets {
			return fmt.Errorf("bucket after _sum or _count")
		}
		if h.infSeen {
			return fmt.Errorf("bucket after +Inf")
		}
		le := ""
		for _, kv := range labels {
			if kv[0] == "le" {
				le = kv[1]
			}
		}
		if le == "" {
			return fmt.Errorf("bucket without le")
		}
		bound, err := parseValue(le)
		if err != nil {
			return fmt.Errorf("le: %w", err)
		}
		if bound <= h.lastLE {
			return fmt.Errorf("le %s is not ascending", le)
		}
		if v < h.lastCum {
			return fmt.Errorf("bucket count falls from %v to %v", h.lastCum, v)
		}
		h.lastLE, h.lastCum = bound, v
		h.buckets++
		if math.IsInf(bound, 1) {
			h.infSeen, h.inf = true, v
		}
	case "_sum":
		if !h.infSeen {
			return fmt.Errorf("_sum before the +Inf bucket")
		}
		h.sumSeen, h.afterBuckets = true, true
	case "_count":
		if !h.infSeen {
			return fmt.Errorf("_count before the +Inf bucket")
		}
		h.countSeen, h.afterBuckets, h.count = true, true, v
	}
	return nil
}

// canonical renders labels sorted by name, leaving out skip, as a series key.
func canonical(labels [][2]string, skip string) string {
	kept := make([]string, 0, len(labels))
	for _, kv := range labels {
		if kv[0] != skip {
			kept = append(kept, kv[0]+"="+strconv.Quote(kv[1]))
		}
	}
	sort.Strings(kept)
	return strings.Join(kept, ",")
}

func parseValue(s string) (float64, error) {
	switch s {
	case "NaN":
		return math.NaN(), nil
	case "+Inf":
		return math.Inf(1), nil
	case "-Inf":
		return math.Inf(-1), nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("bad value %q", s)
	}
	return v, nil
}

// splitSample parses name, the optional {labels} and the text after them.
func splitSample(s string) (name string, labels [][2]string, rest string, err error) {
	i := strings.IndexAny(s, "{ ")
	if i <= 0 {
		return "", nil, "", fmt.Errorf("malformed sample")
	}
	name = s[:i]
	if !metricNameRE.MatchString(name) {
		return "", nil, "", fmt.Errorf("invalid metric name %q", name)
	}
	if s[i] == ' ' {
		return name, nil, s[i+1:], nil
	}
	i++ // past '{'
	seen := map[string]bool{}
	for {
		if i < len(s) && s[i] == '}' {
			i++
			break
		}
		eq := strings.IndexByte(s[i:], '=')
		if eq <= 0 {
			return "", nil, "", fmt.Errorf("malformed label")
		}
		ln := s[i : i+eq]
		if !labelNameRE.MatchString(ln) || strings.HasPrefix(ln, "__") {
			return "", nil, "", fmt.Errorf("invalid label name %q", ln)
		}
		if seen[ln] {
			return "", nil, "", fmt.Errorf("label %q repeated", ln)
		}
		seen[ln] = true
		i += eq + 1
		if i >= len(s) || s[i] != '"' {
			return "", nil, "", fmt.Errorf("label value is not quoted")
		}
		i++
		var val strings.Builder
		closed := false
		for i < len(s) && !closed {
			switch c := s[i]; c {
			case '\\':
				if i+1 >= len(s) {
					return "", nil, "", fmt.Errorf("dangling backslash")
				}
				switch s[i+1] {
				case '\\':
					val.WriteByte('\\')
				case '"':
					val.WriteByte('"')
				case 'n':
					val.WriteByte('\n')
				default:
					return "", nil, "", fmt.Errorf("bad escape \\%c", s[i+1])
				}
				i += 2
			case '"':
				closed = true
				i++
			default:
				val.WriteByte(c)
				i++
			}
		}
		if !closed {
			return "", nil, "", fmt.Errorf("unterminated label value")
		}
		labels = append(labels, [2]string{ln, val.String()})
		if i < len(s) && s[i] == ',' {
			i++
		} else if i >= len(s) || s[i] != '}' {
			return "", nil, "", fmt.Errorf("malformed label list")
		}
	}
	if i >= len(s) || s[i] != ' ' {
		return "", nil, "", fmt.Errorf("no value after the labels")
	}
	return name, labels, s[i+1:], nil
}

// checkEscapes rejects a backslash that is not followed by an escape the
// format defines (\\ and \n in HELP; also \" in label values).
func checkEscapes(s string, quote bool) error {
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			continue
		}
		if i+1 >= len(s) || !(s[i+1] == '\\' || s[i+1] == 'n' || (quote && s[i+1] == '"')) {
			return fmt.Errorf("bad escape in text")
		}
		i++
	}
	return nil
}
