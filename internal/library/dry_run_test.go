package library

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"flodni/internal/metadb"
)

// dry_run (ADR 3, step 3): the registry's decision, with nothing done.

func snapshotDir(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			b, _ := os.ReadFile(p)
			out[p] = string(b)
		}
		return nil
	})
	return out
}

// The dry run's decision, per policy and prior, equals the decision the real add then
// makes, and the dry run writes nothing, announces nothing and asks the engine nothing.
func TestDryRunDecisionEqualsTheRealAddsDecision(t *testing.T) {
	type c struct {
		name   string
		policy string
		prior  map[int]int
		score  int
	}
	for _, c := range []c{
		{"nothing filed", "always", nil, 0},
		{"never over a prior", "never", map[int]int{1: 5}, 9},
		{"always over a prior", "always", map[int]int{1: 5}, 0},
		{"if-better, better", "if-better", map[int]int{1: 5}, 7},
		{"if-better, equal", "if-better", map[int]int{1: 5}, 5},
		{"if-better, score 0", "if-better", map[int]int{1: 5}, 0},
		{"if-better, nothing filed", "if-better", nil, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			dry := newPolicyEnv(t, c.prior)
			real := newPolicyEnv(t, c.prior)
			dry.m.cfg.MediaServer = fakeMediaServer{}
			before := snapshotDir(t, dry.tv)
			rowsBefore := map[string]metadb.EpisodeEntry{}
			for k, v := range dry.reg.rows {
				rowsBefore[k] = v
			}

			d, err := dry.m.Add(context.Background(), AddRequest{
				Type: "tv", Title: "Show", Hash: newHash, Season: 1, Episode: 1,
				Replace: c.policy, QualityScore: c.score, DryRun: true, MetadataWait: 1,
			})
			if err != nil {
				t.Fatal(err)
			}
			r, err := real.add(1, c.policy, c.score)
			if err != nil {
				t.Fatal(err)
			}

			// The two environments sit in different temp directories: compare below them.
			rel := func(root, p string) string { r, _ := filepath.Rel(root, p); return r }
			relReplaced := func(root string, in []ReplacedFile) []ReplacedFile {
				var out []ReplacedFile
				for _, f := range in {
					f.Path = rel(root, f.Path)
					out = append(out, f)
				}
				return out
			}
			if d.Decision != r.Decision || !reflect.DeepEqual(d.Skipped, r.Skipped) ||
				!reflect.DeepEqual(relReplaced(dry.tv, d.Replaced), relReplaced(real.tv, r.Replaced)) {
				t.Fatalf("dry run %+v\nreal    %+v", d, r)
			}
			// The planned files are the ones the real add wrote, without a fuse_path.
			if len(d.Files) != len(r.Files) {
				t.Fatalf("dry files %+v, real files %+v", d.Files, r.Files)
			}
			for i := range d.Files {
				if rel(dry.tv, d.Files[i].Path) != rel(real.tv, r.Files[i].Path) || d.Files[i].FusePath != "" {
					t.Fatalf("dry file %+v, real file %+v", d.Files[i], r.Files[i])
				}
			}
			if !d.DryRun || d.Readability != ReadabilityUnchecked || r.DryRun || r.Readability != "" {
				t.Fatalf("dry %+v real %+v", d, r)
			}

			if !reflect.DeepEqual(snapshotDir(t, dry.tv), before) || !reflect.DeepEqual(dry.reg.rows, rowsBefore) {
				t.Fatal("a dry run wrote a stub or a registry row")
			}
			if len(*dry.added) != 0 || dry.m.refreshPending != nil {
				t.Fatalf("a dry run fired OnAdded (%d) or scheduled a scan", len(*dry.added))
			}
			if calls := dry.eng.seen(); len(calls) != 0 {
				t.Fatalf("a dry run called the engine: %v", calls)
			}
		})
	}
}

type fakeMediaServer struct{}

func (fakeMediaServer) RefreshLibrary(context.Context, int) error { return nil }

// A dry run entry never carries a fuse_path, in the JSON either, and the answer is
// 200: nothing was written.
func TestDryRunAnswerHasNoFusePath(t *testing.T) {
	e := newPolicyEnv(t, map[int]int{1: 5})
	rec := httptest.NewRecorder()
	body := `{"type":"tv","title":"Show","hash":"` + newHash + `","season":1,"episode":1,"replace":"always","dry_run":true}`
	NewHandler(e.m).Add(rec, httptest.NewRequest(http.MethodPost, "/api/library/add", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "fuse_path") {
		t.Fatalf("a dry run answered a fuse_path: %s", rec.Body)
	}
	var got struct {
		DryRun      bool   `json:"dry_run"`
		Readability string `json:"readability"`
		Decision    string `json:"decision"`
		Files       []struct {
			Path string `json:"path"`
		} `json:"files"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.DryRun || got.Readability != "unchecked" || got.Decision != "replaced" || len(got.Files) != 1 {
		t.Fatalf("body %s", rec.Body)
	}
}

// An already filed release answers already_present from the stubs, still without a
// fuse_path and without the engine.
func TestDryRunOfAFiledReleaseIsAlreadyPresent(t *testing.T) {
	e := newPolicyEnv(t, nil)
	if _, err := e.add(1, "", 0); err != nil {
		t.Fatal(err)
	}
	calls := len(e.eng.seen())
	resp, err := e.m.Add(context.Background(), AddRequest{
		Type: "tv", Title: "Show", Hash: newHash, Season: 1, Episode: 1, DryRun: true})
	if err != nil || !resp.AlreadyPresent || resp.Decision != DecisionAlreadyPresent || !resp.DryRun {
		t.Fatalf("resp = %+v, %v", resp, err)
	}
	for _, f := range resp.Files {
		if f.FusePath != "" {
			t.Fatalf("fuse_path in a dry run: %+v", f)
		}
	}
	if len(e.eng.seen()) != calls {
		t.Fatal("the dry run called the engine")
	}
}

// A movie plans its stub; a second release is a second stub, so it is added.
func TestDryRunOfAMovie(t *testing.T) {
	e := newCharEnv(t, movieTorrent...)
	resp, err := e.m.Add(context.Background(), AddRequest{Title: "Inception", Hash: charPackHash, Year: 2010, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Decision != DecisionAdded || len(resp.Files) != 1 || resp.Files[0].FusePath != "" {
		t.Fatalf("resp = %+v", resp)
	}
	if got := mkvs(t, e.movies); len(got) != 0 {
		t.Fatalf("a dry run wrote %v", got)
	}
}

// A pack's episodes are only known from the file list, which a dry run never fetches.
func TestDryRunOfAPackIsABadRequest(t *testing.T) {
	e := newPolicyEnv(t, nil)
	_, err := e.m.Add(context.Background(), AddRequest{
		Type: "tv", Title: "Show", Hash: newHash, Season: 1, DryRun: true})
	if statusOf(t, err) != http.StatusBadRequest || !strings.Contains(err.Error(), "dry_run needs an episode") {
		t.Fatalf("err = %v", err)
	}
	if calls := e.eng.seen(); len(calls) != 0 {
		t.Fatalf("engine calls = %v", calls)
	}
}
