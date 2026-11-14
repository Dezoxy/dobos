// Package scrape is a small tracker scrape client and scheduler (BEP 48 for
// HTTP, BEP 15 for UDP). It reports a torrent's seeders and leechers as its own
// trackers see them, and nothing else.
//
// Announce URLs carry a passkey. Nothing here logs or returns an announce URL,
// a scrape URL, a path or a query: errors name a failure class and the host
// ("scheme://host:port") only, and text a tracker sends back is scrubbed of
// every token that appears in the announce URL before it is kept.
package scrape

import (
	"errors"
	"net/url"
	"strings"
)

// Endpoint is where one tracker is scraped.
type Endpoint struct {
	// Kind is "http" (also https) or "udp".
	Kind string
	// Host is "scheme://host:port", lower-case. It is the only form of the
	// tracker that may be logged or reported, and the key of its worker.
	Host string
	// Key groups the hashes of one request: the scrape URL for HTTP, host:port
	// for UDP. It can hold a passkey: never log or return it.
	Key string
}

var (
	// ErrNoScrape: the announce URL has no "announce" last path segment (BEP 48),
	// so there is no scrape URL to derive.
	ErrNoScrape = errors.New("no scrape URL")
	// ErrUnsupported: a scheme other than http, https or udp.
	ErrUnsupported = errors.New("unsupported tracker scheme")
)

// Derive turns an announce URL into an Endpoint. HTTP follows BEP 48: when the
// last path segment starts with "announce", it becomes "scrape" ("/a/announce.php"
// gives "/a/scrape.php"), the query is kept, and any other path has no scrape
// URL. UDP needs host and port only. The returned errors never contain the URL.
func Derive(announce string) (Endpoint, error) {
	u, err := url.Parse(announce)
	if err != nil || u.Host == "" {
		return Endpoint{}, ErrUnsupported
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Host)
	switch scheme {
	case "udp":
		if u.Port() == "" {
			return Endpoint{}, ErrUnsupported
		}
		return Endpoint{Kind: "udp", Host: "udp://" + host, Key: host}, nil
	case "http", "https":
	default:
		return Endpoint{}, ErrUnsupported
	}
	// Edit the raw string so the encoding of a passkey survives untouched.
	rest := announce[strings.Index(announce, "://")+3:]
	if i := strings.IndexByte(rest, '#'); i >= 0 {
		rest = rest[:i]
	}
	query := ""
	if i := strings.IndexByte(rest, '?'); i >= 0 {
		rest, query = rest[:i], rest[i:]
	}
	slash := strings.IndexByte(rest, '/')
	if slash < 0 {
		return Endpoint{}, ErrNoScrape
	}
	authority, path := rest[:slash], rest[slash:]
	last := strings.LastIndexByte(path, '/')
	seg := path[last+1:]
	if !strings.HasPrefix(seg, "announce") {
		return Endpoint{}, ErrNoScrape
	}
	scrapeURL := scheme + "://" + authority + path[:last+1] + "scrape" + seg[len("announce"):] + query
	return Endpoint{Kind: "http", Host: scheme + "://" + host, Key: scrapeURL}, nil
}
