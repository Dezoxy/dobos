package library

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAddRejectsAudioTypes(t *testing.T) {
	h := NewHandler(New(Config{}))
	for _, typ := range []string{"music", "audiobook", "audiobooks", "album"} {
		t.Run(typ, func(t *testing.T) {
			body := `{"type":"` + typ + `","title":"x","hash":"0123456789abcdef0123456789abcdef01234567"}`
			rec := httptest.NewRecorder()
			h.Add(rec, httptest.NewRequest(http.MethodPost, "/api/library/add", strings.NewReader(body)))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body %s", rec.Code, rec.Body)
			}
			var resp map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(resp["error"], "use movie or tv") {
				t.Fatalf("error %q does not name the supported types", resp["error"])
			}
		})
	}
}

func TestRemoveRejectsAudioTypes(t *testing.T) {
	h := NewHandler(New(Config{}))
	rec := httptest.NewRecorder()
	h.Remove(rec, httptest.NewRequest(http.MethodPost, "/api/library/remove",
		strings.NewReader(`{"type":"music","path":"music/a/b.flac"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body %s", rec.Code, rec.Body)
	}
}
