package library

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"flodni/internal/apiauth"
)

// POST /api/library/confirm (ADR 3, step 5): a manual play mark that fails closed.

type confirmCall struct {
	hash string
	idx  int
}

func confirmEnv(t *testing.T, tokenSet bool) (*charEnv, *[]confirmCall) {
	t.Helper()
	e := newCharEnv(t, FileStat{ID: 3, Path: "Show.S01E02.mkv", Length: 1234})
	if _, err := e.add(AddRequest{Type: "tv", Title: "Show", Hash: charPackHash, Season: 1, Episode: 2}); err != nil {
		t.Fatal(err)
	}
	var calls []confirmCall
	e.m.cfg.Confirm = func(hash string, idx int) { calls = append(calls, confirmCall{hash, idx}) }
	e.m.cfg.TokenSet = func() bool { return tokenSet }
	return e, &calls
}

func postConfirm(e *charEnv, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	NewHandler(e.m).Confirm(rec, httptest.NewRequest(http.MethodPost, "/api/library/confirm", strings.NewReader(body)))
	return rec
}

var confirmBody = `{"hash":"` + charPackHash + `","file_index":3}`

// With no api_token the control port is open to anyone, so confirm answers 403 and
// records nothing, for a valid request and for a malformed one alike.
func TestConfirmFailsClosedWithoutAToken(t *testing.T) {
	for _, tokenSet := range []func() bool{nil, func() bool { return false }} {
		e, calls := confirmEnv(t, false)
		e.m.cfg.TokenSet = tokenSet
		for _, body := range []string{confirmBody, `{}`, `not json`, `{"hash":"` + charPackHash + `","file_index":99}`} {
			rec := postConfirm(e, body)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("body %q: status %d, want 403; %s", body, rec.Code, rec.Body)
			}
		}
		if len(*calls) != 0 {
			t.Fatalf("a refused confirm recorded a play: %v", *calls)
		}
	}
}

func TestConfirmRecordsThePlayAndAnswersTheState(t *testing.T) {
	e, calls := confirmEnv(t, true)
	played := false
	e.m.cfg.FileState = func(string, int) FileFacts {
		if played {
			return FileFacts{Protected: true}
		}
		return FileFacts{}
	}
	e.m.cfg.Confirm = func(hash string, idx int) { played = true; *calls = append(*calls, confirmCall{hash, idx}) }

	// The hash is accepted in any case and arrives canonical.
	rec := postConfirm(e, `{"hash":"`+strings.ToUpper(charPackHash)+`","file_index":3}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"state":"played"`) {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if len(*calls) != 1 || (*calls)[0] != (confirmCall{charPackHash, 3}) {
		t.Fatalf("calls = %v", *calls)
	}
	// A repeat is a 200 again: state unchanged, the played time renewed by the daemon.
	if rec = postConfirm(e, confirmBody); rec.Code != http.StatusOK || len(*calls) != 2 {
		t.Fatalf("repeat: status %d, calls %d", rec.Code, len(*calls))
	}
}

func TestConfirmOfAnUnknownFileIs404AndRecordsNothing(t *testing.T) {
	e, calls := confirmEnv(t, true)
	for _, body := range []string{
		`{"hash":"` + charPackHash + `","file_index":4}`,
		`{"hash":"` + charOtherHash + `","file_index":3}`,
	} {
		if rec := postConfirm(e, body); rec.Code != http.StatusNotFound {
			t.Fatalf("%s: status %d, want 404", body, rec.Code)
		}
	}
	if len(*calls) != 0 {
		t.Fatalf("recorded %v", *calls)
	}
}

func TestConfirmRefusesBadRequests(t *testing.T) {
	e, calls := confirmEnv(t, true)
	for _, body := range []string{`{}`, `not json`, `{"hash":"` + charPackHash + `"}`,
		`{"file_index":3}`, `{"hash":"zz","file_index":3}`, `{"hash":"` + charPackHash + `","file_index":-2}`} {
		if rec := postConfirm(e, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%q: status %d, want 400", body, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	NewHandler(e.m).Confirm(rec, httptest.NewRequest(http.MethodGet, "/api/library/confirm", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: status %d, want 405", rec.Code)
	}
	if len(*calls) != 0 {
		t.Fatalf("recorded %v", *calls)
	}
}

// With a token set the bearer gate sits in front of the route: no token, no play mark.
func TestConfirmNeedsTheBearerToken(t *testing.T) {
	e, calls := confirmEnv(t, true)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/library/confirm", NewHandler(e.m).Confirm)
	srv := apiauth.Middleware("secret", nil, mux)
	for _, c := range []struct {
		auth string
		want int
	}{{"", http.StatusUnauthorized}, {"Bearer nope", http.StatusUnauthorized}, {"Bearer secret", http.StatusOK}} {
		req := httptest.NewRequest(http.MethodPost, "/api/library/confirm", strings.NewReader(confirmBody))
		if c.auth != "" {
			req.Header.Set("Authorization", c.auth)
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("auth %q: status %d, want %d", c.auth, rec.Code, c.want)
		}
	}
	if len(*calls) != 1 {
		t.Fatalf("recorded %d plays, want only the authorized one", len(*calls))
	}
}

func TestConfirmWithoutARecorderIs503(t *testing.T) {
	e, _ := confirmEnv(t, true)
	e.m.cfg.Confirm = nil
	if rec := postConfirm(e, confirmBody); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", rec.Code)
	}
}
