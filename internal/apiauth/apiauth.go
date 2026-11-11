// Package apiauth is the optional bearer-token gate on the control server.
package apiauth

import (
	"crypto/subtle"
	"log"
	"net/http"
	"strings"
)

// Everything needs the token except the static panel page, which holds no data.
// Plex and Jellyfin webhooks cannot send headers, so only those two routes also
// take the token as ?token=; a query token elsewhere would leak into logs and
// browser history.
func open(r *http.Request) bool {
	return r.Method == http.MethodGet && r.URL.Path == "/control"
}

func webhook(r *http.Request) bool {
	return r.URL.Path == "/plex/webhook" || r.URL.Path == "/webhook"
}

// Middleware returns next unchanged when token is empty (auth off). Otherwise
// it answers 401 unless the request carries the token. The token and the
// credential offered are never logged.
func Middleware(token string, logger *log.Logger, next http.Handler) http.Handler {
	if token == "" {
		return next
	}
	want := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if open(r) || authorized(r, want) {
			next.ServeHTTP(w, r)
			return
		}
		if logger != nil {
			logger.Printf("[Control] 401 %s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
		}
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
}

func authorized(r *http.Request, want []byte) bool {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		if equal([]byte(h[len("Bearer "):]), want) {
			return true
		}
	}
	if webhook(r) {
		return equal([]byte(r.URL.Query().Get("token")), want)
	}
	return false
}

func equal(got, want []byte) bool {
	return len(got) == len(want) && subtle.ConstantTimeCompare(got, want) == 1
}
