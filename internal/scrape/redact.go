package scrape

import (
	"net/url"
	"sort"
	"strings"
)

const maxMsg = 80

// secretsOf lists the tokens of an announce URL that must never leave the
// process: userinfo, path segments and query values of at least 4 characters
// (the fixed words "announce" and "scrape" excepted), raw and unescaped.
func secretsOf(announce string) []string {
	u, err := url.Parse(announce)
	if err != nil {
		return nil
	}
	var out []string
	add := func(s string) {
		if len(s) >= 4 && !strings.HasPrefix(s, "announce") && !strings.HasPrefix(s, "scrape") {
			out = append(out, s)
		}
	}
	both := func(raw string) {
		add(raw)
		if un, err := url.PathUnescape(raw); err == nil && un != raw {
			add(un)
		}
	}
	if u.User != nil {
		both(u.User.Username())
		if p, ok := u.User.Password(); ok {
			both(p)
		}
	}
	for _, seg := range strings.Split(u.EscapedPath(), "/") {
		both(seg)
	}
	for _, kv := range strings.Split(u.RawQuery, "&") {
		if _, v, ok := strings.Cut(kv, "="); ok {
			both(v)
		} else {
			both(kv)
		}
	}
	return out
}

// variants adds, for a long token, its first and last 8 characters: a tracker that
// echoes a shortened passkey must not get it through.
func variants(secrets []string) []string {
	out := append([]string(nil), secrets...)
	for _, t := range secrets {
		if len(t) >= 12 {
			out = append(out, t[:8], t[len(t)-8:])
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

// asciiOnly maps every byte outside 0x20..0x7e to '?', byte for byte, so the result
// has the length of the input and every offset means the same in both.
func asciiOnly(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c < 0x20 || c > 0x7e {
			b[i] = '?'
		}
	}
	return string(b)
}

// lowerASCII lowers A-Z only: one byte in, one byte out, unlike strings.ToLower,
// which can change the length of non-ASCII text.
func lowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// clean reduces msg to printable ASCII first, then replaces every secret,
// case-insensitively and longest first, then caps the length. Every offset is
// taken from the string it is used on: nothing can go out of range, whatever
// bytes a tracker sends. The secrets get the same reduction, so a passkey with
// odd bytes matches its own echo.
func clean(msg string, secrets []string) string {
	msg = asciiOnly(msg)
	for _, sec := range variants(secrets) {
		sec = lowerASCII(asciiOnly(sec))
		if len(sec) < 4 {
			continue
		}
		var b strings.Builder
		for {
			i := strings.Index(lowerASCII(msg), sec)
			if i < 0 {
				break
			}
			b.WriteString(msg[:i])
			b.WriteString("***")
			msg = msg[i+len(sec):]
		}
		b.WriteString(msg)
		msg = b.String()
	}
	if len(msg) > maxMsg {
		msg = msg[:maxMsg]
	}
	return msg
}
