package apiauth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

const tok = "s3cret-token"

func serve(token, method, target, auth string) *httptest.ResponseRecorder {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest(method, target, nil)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	Middleware(token, nil, next).ServeHTTP(rec, req)
	return rec
}

func TestMiddleware(t *testing.T) {
	tests := []struct {
		name, token, method, target, auth string
		want                              int
	}{
		{"off passes api", "", "POST", "/api/restart", "", 200},
		{"off passes bad header", "", "GET", "/metrics", "Bearer nope", 200},
		{"valid header", tok, "GET", "/api/config", "Bearer " + tok, 200},
		{"missing header", tok, "GET", "/api/config", "", 401},
		{"wrong header", tok, "GET", "/api/config", "Bearer wrong", 401},
		{"wrong length header", tok, "GET", "/api/config", "Bearer " + tok + "x", 401},
		{"basic scheme", tok, "GET", "/api/config", "Basic " + tok, 401},
		{"control page open", tok, "GET", "/control", "", 200},
		{"control POST not open", tok, "POST", "/control", "", 401},
		{"metrics needs token", tok, "GET", "/metrics", "", 401},
		{"metrics subroute needs token", tok, "GET", "/metrics/ttff", "", 401},
		{"restart needs token", tok, "POST", "/api/restart", "", 401},
		{"plex webhook query", tok, "POST", "/plex/webhook?token=" + tok, "", 200},
		{"plex webhook wrong query", tok, "POST", "/plex/webhook?token=bad", "", 401},
		{"plex webhook no token", tok, "POST", "/plex/webhook", "", 401},
		{"webhook query", tok, "POST", "/webhook?token=" + tok, "", 200},
		{"webhook header", tok, "POST", "/webhook", "Bearer " + tok, 200},
		{"query token rejected on api", tok, "GET", "/api/sessions?token=" + tok, "", 401},
		{"query token rejected on metrics", tok, "GET", "/metrics?token=" + tok, "", 401},
		{"query token rejected on library", tok, "POST", "/api/library/add?token=" + tok, "", 401},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serve(tt.token, tt.method, tt.target, tt.auth)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
			if tt.want == 401 && rec.Header().Get("WWW-Authenticate") != "Bearer" {
				t.Errorf("WWW-Authenticate = %q, want Bearer", rec.Header().Get("WWW-Authenticate"))
			}
		})
	}
}
