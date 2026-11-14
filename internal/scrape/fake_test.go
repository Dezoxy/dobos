package scrape

import (
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func h20(n byte) [20]byte {
	var h [20]byte
	for i := range h {
		h[i] = n + byte(i)
	}
	return h
}

func benFiles(m map[[20]byte]Counts) []byte {
	var b strings.Builder
	b.WriteString("d5:filesd")
	for h, c := range m {
		fmt.Fprintf(&b, "20:%sd8:completei%de10:incompletei%dee", h[:], c.Seeders, c.Leechers)
	}
	b.WriteString("ee")
	return []byte(b.String())
}

type httpReq struct {
	at     time.Time
	hashes [][20]byte
	path   string
}

// fakeHTTP is a local tracker. answer decides the reply for one request.
type fakeHTTP struct {
	srv    *httptest.Server
	mu     sync.Mutex
	reqs   []httpReq
	counts map[[20]byte]Counts
	answer func(w http.ResponseWriter, r *http.Request, hashes [][20]byte)
}

func newFakeHTTP(t *testing.T, counts map[[20]byte]Counts) *fakeHTTP {
	t.Helper()
	f := &fakeHTTP{counts: counts}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var hs [][20]byte
		for _, kv := range strings.Split(r.URL.RawQuery, "&") {
			if v, ok := strings.CutPrefix(kv, "info_hash="); ok {
				raw, err := url.QueryUnescape(v)
				if err != nil || len(raw) != 20 {
					http.Error(w, "bad", 400)
					return
				}
				var h [20]byte
				copy(h[:], raw)
				hs = append(hs, h)
			}
		}
		f.mu.Lock()
		f.reqs = append(f.reqs, httpReq{at: time.Now(), hashes: hs, path: r.URL.Path})
		f.mu.Unlock()
		if f.answer != nil {
			f.answer(w, r, hs)
			return
		}
		got := map[[20]byte]Counts{}
		for _, h := range hs {
			if c, ok := f.counts[h]; ok {
				got[h] = c
			}
		}
		_, _ = w.Write(benFiles(got))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeHTTP) announce() string { return f.srv.URL + "/announce" }
func (f *fakeHTTP) host() string     { return f.srv.URL }
func (f *fakeHTTP) requests() []httpReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]httpReq(nil), f.reqs...)
}

// fakeUDP speaks BEP 15. mutate can break the scrape answer.
type fakeUDP struct {
	conn   net.PacketConn
	counts map[[20]byte]Counts
	mode   string // "", "error", "badtx", "big"
	mu     sync.Mutex
	scrape int
}

func newFakeUDP(t *testing.T, counts map[[20]byte]Counts) *fakeUDP {
	t.Helper()
	c, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeUDP{conn: c, counts: counts}
	t.Cleanup(func() { _ = c.Close() })
	go func() {
		buf := make([]byte, 2048)
		for {
			n, from, err := c.ReadFrom(buf)
			if err != nil {
				return
			}
			p := buf[:n]
			if n < 16 {
				continue
			}
			tx := binary.BigEndian.Uint32(p[12:16])
			switch binary.BigEndian.Uint32(p[8:12]) {
			case 0:
				r := make([]byte, 16)
				binary.BigEndian.PutUint32(r[4:8], tx)
				binary.BigEndian.PutUint64(r[8:16], 0x1122334455667788)
				_, _ = c.WriteTo(r, from)
			case 2:
				f.mu.Lock()
				f.scrape++
				mode := f.mode
				f.mu.Unlock()
				if binary.BigEndian.Uint64(p[0:8]) != 0x1122334455667788 {
					continue
				}
				hs := (n - 16) / 20
				r := make([]byte, 8)
				binary.BigEndian.PutUint32(r[0:4], 2)
				binary.BigEndian.PutUint32(r[4:8], tx)
				switch mode {
				case "error":
					r = append(r[:0], make([]byte, 8)...)
					binary.BigEndian.PutUint32(r[0:4], 3)
					binary.BigEndian.PutUint32(r[4:8], tx)
					r = append(r, "unregistered torrent"...)
				case "badtx":
					binary.BigEndian.PutUint32(r[4:8], tx+1)
				case "big":
					r = append(r, make([]byte, 12*(hs+3))...)
				default:
					for i := 0; i < hs; i++ {
						var h [20]byte
						copy(h[:], p[16+20*i:])
						cn, ok := f.counts[h]
						if !ok {
							// a tracker answers zeros for what it does not know,
							// except that this fake stops early to model a partial answer
							break
						}
						e := make([]byte, 12)
						binary.BigEndian.PutUint32(e[0:4], uint32(cn.Seeders))
						binary.BigEndian.PutUint32(e[8:12], uint32(cn.Leechers))
						r = append(r, e...)
					}
				}
				_, _ = c.WriteTo(r, from)
			}
		}
	}()
	return f
}

func (f *fakeUDP) announce() string { return "udp://" + f.conn.LocalAddr().String() + "/announce" }
func (f *fakeUDP) host() string     { return "udp://" + f.conn.LocalAddr().String() }

func ip(n int) *int { return &n }
