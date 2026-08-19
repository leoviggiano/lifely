package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
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

	_, founder := get(t, h, "/api/pendencies?who=founder")
	if founder["count"].(float64) != 2 {
		t.Errorf("who=founder gave %v, want 2", founder["count"])
	}
	_, ai := get(t, h, "/api/pendencies?who=ai")
	if ai["count"].(float64) != 0 {
		t.Errorf("who=ai gave %v, want 0", ai["count"])
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
	// RFC 7807's own field, not a custom "code": fuego answers Problem Details.
	if body["type"] != "gone_from_source" {
		t.Errorf("type = %v, want gone_from_source", body["type"])
	}
}

// A source that could not be read travels with the rest, marked -- absence of
// a source is a finding, never silence (spec NFR6).
func TestSourcesReportTheUnreadable(t *testing.T) {
	p := fixture(t)
	// UNREADABLE, not absent: absence is normal and reports nothing (the
	// SourceState contract). This test removed the file and still demanded a
	// finding -- the same drift its sibling in internal/scan already paid for.
	if err := os.Chmod(filepath.Join(p.Root, "FOUNDER.md"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(p.Root, "FOUNDER.md"), 0o644) })
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

// Neither queue nor stampede: concurrent callers share ONE sweep.
//
// Holding the lock across the scan made them queue; releasing it entirely made
// them all shell out at once. The first caller sweeps and the rest wait on the
// same result.
func TestConcurrentSweepsShareOneScan(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "FOUNDER.md"), []byte("## Faixa 1\n\n- [ ] **algo**\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var scans int64
	p := NewPanel(root, func(args ...string) ([]byte, error) {
		atomic.AddInt64(&scans, 1)
		time.Sleep(80 * time.Millisecond)
		return []byte(`{"tickets":[]}`), nil
	})
	p.TTL = 0 // the cache must not be what saves us here

	const callers = 5
	start := time.Now()
	var wg sync.WaitGroup
	results := make([]*snapshot, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); results[i] = p.sweep() }(i)
	}
	wg.Wait()

	if got := atomic.LoadInt64(&scans); got != 1 {
		t.Errorf("%d callers caused %d scans, want 1", callers, got)
	}
	if elapsed := time.Since(start); elapsed > 300*time.Millisecond {
		t.Errorf("the callers queued instead of sharing: %v", elapsed)
	}
	for i, r := range results {
		if r != results[0] {
			t.Errorf("caller %d got a different snapshot than caller 0", i)
		}
	}
}

// A source that changes WHILE a sweep is running must not be described by that
// sweep's result. Clearing the cache alone did not reach a sweep already in
// flight: it returned and overwrote the cleared cache with pre-change data,
// which then served for a full TTL after the change that called Invalidate.
func TestInvalidateDuringASweepDoesNotCacheTheStaleResult(t *testing.T) {
	p := fixture(t)
	release := make(chan struct{})
	swept := 0
	p.Run = func(args ...string) ([]byte, error) {
		swept++
		if swept == 1 {
			<-release // hold the first sweep open
		}
		return []byte(`{"tickets":[]}`), nil
	}

	done := make(chan struct{})
	go func() { p.sweep(); close(done) }()

	// Let the first sweep get inside scan.All, then change the world.
	for p.inflightForTest() == nil {
		runtime.Gosched()
	}
	p.Invalidate()
	close(release)
	<-done

	// The next read must sweep again rather than serve what the first one saw.
	before := swept
	p.sweep()
	if swept == before {
		t.Error("the stale in-flight result became the cache: a change during a sweep is invisible for a whole TTL")
	}
}
