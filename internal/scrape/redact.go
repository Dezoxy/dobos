package scrape

import (
	"net/url"
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

// clean keeps printable ASCII, caps the length and replaces every secret.
func clean(msg string, secrets []string) string {
	for _, s := range secrets {
		msg = strings.ReplaceAll(msg, s, "***")
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
