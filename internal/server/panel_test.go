package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func fixture(t *testing.T) *Panel {
	t.Helper()
	root := t.TempDir()
	must := func(name, body string) {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("FOUNDER.md", "## Faixa 1\n\n- [ ] **Decidir E18** — a ordem caixa-first\n")
	must("ideias/index.tsv", "# id\ttitulo\tstatus\n3\tmonitor\tem-refino\n")

	run := func(args ...string) ([]byte, error) { return []byte(`{"tickets":[]}`), nil }
	return NewPanel(root, run)
}

func get(t *testing.T, h http.Handler, path string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Host = "127.0.0.1:7777"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("%s did not return JSON: %v (%s)", path, err, rec.Body.String())
	}
	return rec, body
}

func TestListAndFilters(t *testing.T) {
	h := New(7777, "manual", fixture(t))

	_, all := get(t, h, "/api/pendencies")
	if all["count"].(float64) != 2 {
		t.Fatalf("count = %v, want 2", all["count"])
	}
	if all["swept_at"] == nil {
		t.Error("the answer does not say when it was swept")
	}

	_, founder := get(t, h, "/api/pendencies?who=fundador")
	if founder["count"].(float64) != 2 {
		t.Errorf("who=fundador gave %v, want 2", founder["count"])
	}
	_, ai := get(t, h, "/api/pendencies?who=ia")
	if ai["count"].(float64) != 0 {
		t.Errorf("who=ia gave %v, want 0", ai["count"])
	}
	_, byClass := get(t, h, "/api/pendencies?class=A2")
	if byClass["count"].(float64) != 1 {
		t.Errorf("class=A2 gave %v, want 1", byClass["count"])
	}
	_, bySource := get(t, h, "/api/pendencies?source=ideias")
	if bySource["count"].(float64) != 1 {
		t.Errorf("source=ideias gave %v, want 1", bySource["count"])
	}
}

// Every pendency carries the UUID its conversation is keyed by, so the UI
// never has to derive it (spec FR6.1).
func TestListCarriesTheConversationID(t *testing.T) {
	_, body := get(t, New(7777, "manual", fixture(t)), "/api/pendencies")
	items := body["pendencies"].([]any)
	first := items[0].(map[string]any)
	if first["uuid"] == nil || first["uuid"].(string) == "" {
		t.Error("a pendency travelled without its conversation uuid")
	}
}

// A pendency that has been decided between the list and the click is gone,
// not an error: saying so is the honest answer.
func TestUnknownPendencyIsGoneNotBroken(t *testing.T) {
	rec, body := get(t, New(7777, "manual", fixture(t)), "/api/pendencies/nao-existe")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if body["code"] != "gone_from_source" {
		t.Errorf("code = %v, want gone_from_source", body["code"])
	}
}

// A source that could not be read travels with the rest, marked -- absence of
// a source is a finding, never silence (spec NFR6).
func TestSourcesReportTheUnreadable(t *testing.T) {
	p := fixture(t)
	if err := os.Remove(filepath.Join(p.Root, "FOUNDER.md")); err != nil {
		t.Fatal(err)
	}
	_, body := get(t, New(7777, "manual", p), "/api/sources")

	var marked bool
	for _, raw := range body["sources"].([]any) {
		s := raw.(map[string]any)
		if s["name"] == "FOUNDER.md" && s["error"] != nil && s["error"] != "" {
			marked = true
		}
	}
	if !marked {
		t.Error("the unreadable source was not reported")
	}
}

// The cache exists so one page does not sweep the disk five times, but it must
// never make a count survive a change (spec NFR2).
func TestCacheIsShortAndDroppable(t *testing.T) {
	p := fixture(t)
	fake := time.Now()
	p.now = func() time.Time { return fake }

	first := p.sweep()
	if second := p.sweep(); second != first {
		t.Error("two sweeps in the same instant did not share a result")
	}

	p.Invalidate()
	if third := p.sweep(); third == first {
		t.Error("Invalidate did not force a fresh sweep")
	}

	fresh := p.sweep()
	fake = fake.Add(10 * time.Second)
	if aged := p.sweep(); aged == fresh {
		t.Error("the cache outlived its TTL")
	}
}

// The lock guards the cache, never the scan.
//
// Holding it across the whole sweep made every concurrent request queue behind
// a multi-second walk of the record repo -- one slow source and the panel
// stops answering everything.
func TestConcurrentSweepsDoNotSerialize(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "FOUNDER.md"), []byte("## Faixa 1\n\n- [ ] **algo**\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	const slow = 150 * time.Millisecond
	p := NewPanel(root, func(args ...string) ([]byte, error) {
		time.Sleep(slow)
		return []byte(`{"tickets":[]}`), nil
	})
	p.TTL = 0 // never serve from cache: every call really sweeps

	const callers = 4
	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); p.sweep() }()
	}
	wg.Wait()

	// Serialised, this would cost callers*slow. In parallel it costs about one.
	if elapsed := time.Since(start); elapsed > slow*time.Duration(callers-1) {
		t.Errorf("%d concurrent sweeps took %v: they queued behind the lock", callers, elapsed)
	}
}
