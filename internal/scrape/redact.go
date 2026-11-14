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

// clean replaces every secret, case-insensitively and longest first, keeps
// printable ASCII and caps the length.
func clean(msg string, secrets []string) string {
	for _, sec := range variants(secrets) {
		lower, ls := strings.ToLower(msg), strings.ToLower(sec)
		var b strings.Builder
		for {
			i := strings.Index(lower, ls)
			if i < 0 {
				break
			}
			b.WriteString(msg[:i])
			b.WriteString("***")
			msg, lower = msg[i+len(sec):], lower[i+len(sec):]
		}
		b.WriteString(msg)
		msg = b.String()
	}
	b := make([]byte, 0, len(msg))
	for i := 0; i < len(msg) && len(b) < maxMsg; i++ {
		c := msg[i]
		if c < 0x20 || c > 0x7e {
			c = '?'
		}
		b = append(b, c)
	}
	return string(b)
}
