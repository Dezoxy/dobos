// Package apiauth is the optional bearer-token gate on the control server and
// on the engine's (GoStorm) server.
package apiauth

import (
	"crypto/subtle"
	"log"
	"net/http"
	"strings"
)

// Control profile: everything needs the token except the static panel page,
// which holds no data, and GET /api/health, which reports status words only. The Plex webhook cannot send headers, so only
// those two routes also take the token as ?token=; a query token elsewhere
// would leak into logs and browser history.
func controlOpen(r *http.Request) bool {
	return r.Method == http.MethodGet && (r.URL.Path == "/control" || r.URL.Path == "/api/health")
}

func controlWebhook(r *http.Request) bool {
	return r.URL.Path == "/plex/webhook" || r.URL.Path == "/webhook"
}

// GoStorm profile: everything needs the header except the readiness probe
// GET /echo, which returns only the version. No route takes a query token.
func gostormOpen(r *http.Request) bool {
	return r.Method == http.MethodGet && r.URL.Path == "/echo"
}

func never(*http.Request) bool { return false }

// Middleware gates the control server. It returns next unchanged when token is
// empty (auth off). Otherwise it answers 401 unless the request carries the
// token. The token and the credential offered are never logged.
func Middleware(token string, logger *log.Logger, next http.Handler) http.Handler {
	return gate("Control", token, logger, next, controlOpen, controlWebhook)
}

// GoStorm gates the engine's server on port 8090 the same way, with only
// GET /echo open and no query token anywhere. A nil logger logs nothing.
func GoStorm(token string, logger *log.Logger, next http.Handler) http.Handler {
	return gate("GoStorm", token, logger, next, gostormOpen, never)
}

func gate(name, token string, logger *log.Logger, next http.Handler, open, queryToken func(*http.Request) bool) http.Handler {
	if token == "" {
		return next
	}
	want := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if open(r) || authorized(r, want, queryToken) {
			next.ServeHTTP(w, r)
			return
		}
		if logger != nil {
			logger.Printf("[%s] 401 %s %s from %s", name, r.Method, r.URL.Path, r.RemoteAddr)
		}
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
}

func authorized(r *http.Request, want []byte, queryToken func(*http.Request) bool) bool {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		if equal([]byte(h[len("Bearer "):]), want) {
			return true
		}
	}
	if queryToken(r) {
		return equal([]byte(r.URL.Query().Get("token")), want)
	}
	return false
}

func equal(got, want []byte) bool {
	return len(got) == len(want) && subtle.ConstantTimeCompare(got, want) == 1
}
