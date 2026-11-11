package library

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// authRecorder answers every route with just enough for the client to succeed
// and records the Authorization header per path.
func authRecorder(t *testing.T) (*httptest.Server, func() map[string]string) {
	t.Helper()
	var mu sync.Mutex
	seen := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.URL.Path] = r.Header.Get("Authorization")
		mu.Unlock()
		switch r.URL.Path {
		case "/torrents":
			_, _ = w.Write([]byte(`{"hash":"` + testHash + `"}`))
		case "/torrent/upload":
			_, _ = w.Write([]byte(`{"hash":"` + testHash + `"}`))
		case "/stream":
			_, _ = w.Write([]byte("x"))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() map[string]string {
		mu.Lock()
		defer mu.Unlock()
		out := map[string]string{}
		for k, v := range seen {
			out[k] = v
		}
		return out
	}
}

const testHash = "0123456789abcdef0123456789abcdef01234567"

func callAll(t *testing.T, c *GoStormClient) {
	t.Helper()
	ctx := context.Background()
	if _, err := c.AddTorrent(ctx, "magnet:?xt=urn:btih:"+testHash, "t"); err != nil {
		t.Fatalf("AddTorrent: %v", err)
	}
	if _, err := c.UploadTorrent(ctx, []byte("d4:infod4:name1:xee"), "t"); err != nil {
		t.Fatalf("UploadTorrent: %v", err)
	}
	if err := c.RemoveTorrent(ctx, testHash); err != nil {
		t.Fatalf("RemoveTorrent: %v", err)
	}
	if err := c.ReadFileHead(ctx, testHash, 1, 1); err != nil {
		t.Fatalf("ReadFileHead: %v", err)
	}
}

func TestGoStormClientSendsToken(t *testing.T) {
	srv, seen := authRecorder(t)
	callAll(t, NewGoStormClient(srv.URL, "s3cret"))
	got := seen()
	for _, path := range []string{"/torrents", "/torrent/upload", "/stream"} {
		if got[path] != "Bearer s3cret" {
			t.Errorf("%s: Authorization = %q, want %q", path, got[path], "Bearer s3cret")
		}
	}
}

func TestGoStormClientNoTokenNoHeader(t *testing.T) {
	srv, seen := authRecorder(t)
	callAll(t, NewGoStormClient(srv.URL, ""))
	got := seen()
	if len(got) != 3 {
		t.Fatalf("paths seen = %v, want /torrents, /torrent/upload, /stream", got)
	}
	for path, h := range got {
		if h != "" {
			t.Errorf("%s: Authorization = %q, want none", path, h)
		}
	}
}
