package scrape

import (
	"errors"
	"strconv"
)

// A minimal bencode reader for scrape answers. It is bounded on purpose: the
// input is already capped by the caller, and nesting and element counts are
// capped here, so a hostile tracker cannot make it recurse or allocate much.
const (
	maxDepth = 6
	maxItems = 20000
)

var errBencode = errors.New("malformed bencode")

type benDecoder struct {
	b     []byte
	pos   int
	items int
}

// decodeBencode returns int64, []byte, []any or map[string]any. Trailing bytes
// after the value are an error.
func decodeBencode(b []byte) (any, error) {
	d := &benDecoder{b: b}
	v, err := d.value(0)
	if err != nil {
		return nil, err
	}
	if d.pos != len(d.b) {
		return nil, errBencode
	}
	return v, nil
}

func (d *benDecoder) value(depth int) (any, error) {
	if depth > maxDepth || d.pos >= len(d.b) {
		return nil, errBencode
	}
	d.items++
	if d.items > maxItems {
		return nil, errBencode
	}
	switch c := d.b[d.pos]; {
	case c == 'i':
		end := indexByte(d.b, d.pos, 'e')
		if end < 0 {
			return nil, errBencode
		}
		n, err := strconv.ParseInt(string(d.b[d.pos+1:end]), 10, 64)
		if err != nil {
			return nil, errBencode
		}
		d.pos = end + 1
		return n, nil
	case c == 'l':
		d.pos++
		list := []any{}
		for {
			if d.pos >= len(d.b) {
				return nil, errBencode
			}
			if d.b[d.pos] == 'e' {
				d.pos++
				return list, nil
			}
			v, err := d.value(depth + 1)
			if err != nil {
				return nil, err
			}
			list = append(list, v)
		}
	case c == 'd':
		d.pos++
		m := map[string]any{}
		for {
			if d.pos >= len(d.b) {
				return nil, errBencode
			}
			if d.b[d.pos] == 'e' {
				d.pos++
				return m, nil
			}
			k, err := d.str()
			if err != nil {
				return nil, err
			}
			v, err := d.value(depth + 1)
			if err != nil {
				return nil, err
			}
			m[string(k)] = v
		}
	case c >= '0' && c <= '9':
		return d.str()
	}
	return nil, errBencode
}

func (d *benDecoder) str() ([]byte, error) {
	colon := indexByte(d.b, d.pos, ':')
	if colon < 0 || colon-d.pos > 9 {
		return nil, errBencode
	}
	n, err := strconv.Atoi(string(d.b[d.pos:colon]))
	if err != nil || n < 0 || colon+1+n > len(d.b) {
		return nil, errBencode
	}
	s := d.b[colon+1 : colon+1+n]
	d.pos = colon + 1 + n
	return s, nil
}

func indexByte(b []byte, from int, c byte) int {
	for i := from; i < len(b); i++ {
		if b[i] == c {
			return i
		}
	}
	return -1
}
