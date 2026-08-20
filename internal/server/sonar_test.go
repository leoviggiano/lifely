package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leoviggiano/lifely/internal/sonar"
)

// sonarLog is a slice of the tribunal's real log: two gate lines that declare
// their project structurally, one prose dispatch that names its projects only
// in running text, and one line with no stamp at all.
const sonarLog = `2026-08-18T17:30:52 portao ject:  cancelled ject-070-emendas-e14 d626c8f6 2026-08-18 17:17
2026-08-18T17:33:56 frota runs=5 socks=6
uma linha sem carimbo, escrita a mao no log
2026-08-19T15:56:00 portao(lifely) 01M0DMHBF5 COMPLETED passed: origin/main avancou
2026-08-20T07:14:00 DESPACHO (2.5.31): ject-096 -> executor opus · piloto armado, frente A=lifely-018, frente B=lifely-038
`

// sonarFixture is a panel over a root whose only source is the sonar log,
// with the clock pinned so the feed's age is a fact and not a race.
func sonarFixture(t *testing.T, body string) (*Panel, time.Time) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, sonar.LogRelPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if body != "" {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Five minutes after the newest line of sonarLog: inside the cadence, so
	// a feed that reports stale here has a real bug rather than a slow test.
	now := time.Date(2026, 8, 20, 7, 19, 0, 0, time.Local)
	p := NewPanel(root, func(args ...string) ([]byte, error) { return []byte(`{"tickets":[]}`), nil })
	p.now = func() time.Time { return now }
	return p, now
}

func html(t *testing.T, h http.Handler, path string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Host = "127.0.0.1:7777"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec, rec.Body.String()
}

func TestSonarAPIReturnsTheEventsNewestFirst(t *testing.T) {
	p, _ := sonarFixture(t, sonarLog)
	_, body := get(t, New(7777, "manual", p), "/api/sonar")

	events, ok := body["events"].([]any)
	if !ok {
		t.Fatalf("events is %T, want a list", body["events"])
	}
	if len(events) != 5 {
		t.Fatalf("len(events) = %d, want 5 -- every line travels, unparsed included", len(events))
	}
	first := events[0].(map[string]any)
	if !strings.Contains(first["raw"].(string), "DESPACHO") {
		t.Errorf("first event is %v, want the newest line", first["raw"])
	}
	if body["stale"].(bool) {
		t.Error("stale = true five minutes after the newest event")
	}
	if body["missing"].(bool) {
		t.Error("missing = true for a log that is right there")
	}
	if body["path"].(string) == "" {
		t.Error("path is empty; a wrong --root has to be visible on the screen")
	}
}

func TestSonarAPIMarksTheUnparsedLineWithoutDroppingIt(t *testing.T) {
	p, _ := sonarFixture(t, sonarLog)
	_, body := get(t, New(7777, "manual", p), "/api/sonar")

	for _, raw := range body["events"].([]any) {
		ev := raw.(map[string]any)
		if !strings.Contains(ev["raw"].(string), "sem carimbo") {
			continue
		}
		if ev["parsed"].(bool) {
			t.Error("the unstamped line came back parsed")
		}
		if ev["theme"].(string) != string(sonar.ThemeBroken) {
			t.Errorf("theme = %v, want %q", ev["theme"], sonar.ThemeBroken)
		}
		return
	}
	t.Fatal("the unstamped line is not in the response -- it was dropped in silence")
}

func TestSonarAPIFilterByProject(t *testing.T) {
	h := New(7777, "manual", mustFixture(t))

	_, lifely := get(t, h, "/api/sonar?project=lifely")
	// The gate line declares lifely; the DESPACHO only mentions it in prose.
	// Both have to come back, which is what makes the filter useful at all.
	raws := rawsOf(t, lifely)
	if len(raws) != 2 {
		t.Fatalf("project=lifely returned %d events (%v), want 2", len(raws), raws)
	}
	if !strings.Contains(strings.Join(raws, "\n"), "DESPACHO") {
		t.Error("the prose dispatch that names lifely was filtered out")
	}
	if lifely["total"].(float64) != 2 {
		t.Errorf("total = %v, want 2 matches", lifely["total"])
	}

	_, ject := get(t, h, "/api/sonar?project=ject")
	if got := len(rawsOf(t, ject)); got != 2 {
		t.Fatalf("project=ject returned %d events, want 2", got)
	}

	_, none := get(t, h, "/api/sonar?project=lorekeeper")
	if got := len(rawsOf(t, none)); got != 0 {
		t.Errorf("project=lorekeeper returned %d events, want 0", got)
	}

	// The options offered come from what the log DECLARES, never from the
	// filtered feed -- filtering to one project must not remove the way back.
	projects := none["projects"].([]any)
	if len(projects) != 2 || projects[0].(string) != "ject" || projects[1].(string) != "lifely" {
		t.Errorf("projects = %v, want [ject lifely] even under a filter that matched nothing", projects)
	}
}

func TestSonarAPILimitCutsTheFeedAndSaysSo(t *testing.T) {
	p, _ := sonarFixture(t, sonarLog)
	_, body := get(t, New(7777, "manual", p), "/api/sonar?limit=2")
	if got := len(body["events"].([]any)); got != 2 {
		t.Fatalf("count = %d, want 2", got)
	}
	if body["total"].(float64) != 5 {
		t.Errorf("total = %v, want 5: a limit hides events, it does not unmake them", body["total"])
	}
}

func TestSonarAPIOnAMissingLogIs200AndEmpty(t *testing.T) {
	p, _ := sonarFixture(t, "")
	rec, body := get(t, New(7777, "manual", p), "/api/sonar")
	// Spec FR3: a source that cannot be read is a marked entry, never a 500.
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !body["missing"].(bool) {
		t.Error("missing = false for a log that is not there")
	}
	if body["error"] != nil {
		t.Errorf("error = %v -- an absent log is an empty state, not a finding", body["error"])
	}
	if len(body["events"].([]any)) != 0 {
		t.Error("events is not empty")
	}
}

func TestSonarAPIReportsAColdLog(t *testing.T) {
	// One second past twice the sonar's cadence. The charter makes an old
	// stamp an alarm by itself, so the feed has to say it rather than let the
	// reader compute it.
	old := time.Date(2026, 8, 20, 7, 19, 0, 0, time.Local).Add(-sonar.StaleAfter - time.Second)
	p, _ := sonarFixture(t, old.Format("2006-01-02T15:04:05")+" frota runs=0 socks=0\n")
	_, body := get(t, New(7777, "manual", p), "/api/sonar")
	if !body["stale"].(bool) {
		t.Error("stale = false for an event older than StaleAfter")
	}
}

func TestSonarPageRendersTheFeed(t *testing.T) {
	h := New(7777, "manual", mustFixture(t))
	rec, page := html(t, h, "/sonar")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	// The page is the whole document, not just the list.
	if !strings.Contains(page, "<!doctype html>") {
		t.Error("the page is not a document")
	}
	if !strings.Contains(page, `href="/static/lifely.css"`) {
		t.Error("the page does not load the panel's stylesheet")
	}
	// Newest first, on the screen and not only in JSON.
	despacho := strings.Index(page, "piloto armado")
	cancelled := strings.Index(page, "ject-070-emendas-e14")
	if despacho < 0 || cancelled < 0 {
		t.Fatalf("events missing from the page (despacho=%d cancelled=%d)", despacho, cancelled)
	}
	if despacho > cancelled {
		t.Error("the oldest event is rendered above the newest")
	}
	// The unparsed line shows raw and marked.
	if !strings.Contains(page, "uma linha sem carimbo") {
		t.Error("the unstamped line is not on the page")
	}
	if !strings.Contains(page, "unparsed") {
		t.Error("the unstamped line is on the page but not marked as unparsed")
	}
	// The palette carries the meaning (FR4.10): a gate event is amber, a
	// dispatch is the founder's sage, an unreadable line is red.
	for _, class := range []string{"theme-gate", "theme-founder", "theme-broken"} {
		if !strings.Contains(page, class) {
			t.Errorf("no element carries %s; the colour semantics are not on the screen", class)
		}
	}
	// The filter offers the real slugs.
	if !strings.Contains(page, `<option value="lifely"`) {
		t.Error("the project filter does not offer lifely")
	}
}

func TestSonarPageFilterAndFragmentAgree(t *testing.T) {
	h := New(7777, "manual", mustFixture(t))

	_, page := html(t, h, "/sonar?project=ject")
	if strings.Contains(page, "COMPLETED passed") {
		t.Error("a lifely-only gate line survived project=ject")
	}
	if !strings.Contains(page, "ject-070-emendas-e14") {
		t.Error("the ject gate line is missing under project=ject")
	}

	// The fragment the page polls is rendered by the same block, so what the
	// first paint shows and what the poll replaces it with cannot disagree.
	rec, fragment := html(t, h, "/sonar/feed?project=ject")
	if rec.Code != http.StatusOK {
		t.Fatalf("fragment status = %d, want 200", rec.Code)
	}
	if strings.Contains(fragment, "<!doctype html>") {
		t.Error("the fragment is a whole document; it is meant to replace one element")
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("the fragment is cacheable; a feed that never moves would look like a quiet fleet")
	}
	if !strings.Contains(fragment, "ject-070-emendas-e14") {
		t.Error("the fragment lost the event the page shows")
	}
}

func TestSonarPageOnAMissingLogSaysSo(t *testing.T) {
	p, _ := sonarFixture(t, "")
	_, page := html(t, New(7777, "manual", p), "/sonar")
	if !strings.Contains(page, "no sonar log at") {
		t.Error("the empty state does not say the log is absent")
	}
	if !strings.Contains(page, sonar.LogRelPath) {
		t.Error("the empty state does not name the path it looked at")
	}
}

// The gate's first review found this at the API edge (run 01M0FB7BT4,
// `newest-at-not-filtered`), so the guard lives at the same edge: the age the
// panel reports has to be the age of the events the panel is showing.
func TestSonarFilteredFeedIsDatedByItsOwnEvents(t *testing.T) {
	// lifely went quiet 20h ago; ject moved a minute before the read.
	now := time.Date(2026, 8, 20, 8, 0, 0, 0, time.Local)
	log := now.Add(-20*time.Hour).Format("2006-01-02T15:04:05") + " portao lifely:  completed lifely-018\n" +
		now.Add(-1*time.Minute).Format("2006-01-02T15:04:05") + " portao ject:  running ject-096\n"

	root := t.TempDir()
	path := filepath.Join(root, sonar.LogRelPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}
	p := NewPanel(root, func(args ...string) ([]byte, error) { return []byte(`{"tickets":[]}`), nil })
	p.now = func() time.Time { return now }
	h := New(7777, "manual", p)

	_, body := get(t, h, "/api/sonar?project=lifely")
	if got := body["newest_at"].(string); !strings.HasPrefix(got, now.Add(-20*time.Hour).Format("2006-01-02T15:04")) {
		t.Errorf("newest_at = %s, want the lifely event's stamp -- not an event outside `events`", got)
	}
	if !body["stale"].(bool) {
		t.Error("stale = false for a filtered feed whose newest event is 20h old")
	}

	// And on the screen, where the colour is the claim.
	_, page := html(t, h, "/sonar?project=lifely")
	if !strings.Contains(page, "the sonar is cold") {
		t.Error("the page does not say the filtered feed is cold")
	}
	if strings.Contains(page, "just now") {
		t.Error("the page reports `just now` for a feed whose newest event is 20h old")
	}
}

// A tail of nothing but unstamped lines has no age at all. Saying "just now"
// there would be the ageing bar lying -- the package separates empty from
// cold, and the screen must not collapse them back.
func TestSonarPageWithNoStampedEventClaimsNoAge(t *testing.T) {
	p, _ := sonarFixture(t, "uma linha sem carimbo nenhum\noutra igual\n")
	_, page := html(t, New(7777, "manual", p), "/sonar")
	if !strings.Contains(page, "no stamped event") {
		t.Error("the page does not say the feed carries no stamp")
	}
	if strings.Contains(page, "just now") {
		t.Error("the page claims `just now` for a feed with no stamped event")
	}
	// The lines themselves are still there, raw.
	if !strings.Contains(page, "uma linha sem carimbo nenhum") {
		t.Error("the unstamped lines were dropped")
	}
}

func TestStaticAssetsAreServed(t *testing.T) {
	// web.Assets had no caller at all before this ticket: the embed existed
	// and nothing read it. This pins that the binary now serves what it
	// embeds -- FR4.11's `git clone` + `go build` produces the whole app.
	rec, body := html(t, New(7777, "manual", mustFixture(t)), "/static/lifely.css")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(body, "--lifely-accent") {
		t.Error("the stylesheet served is not the panel's")
	}
}

// A daemon without a panel serves /healthz and must not touch the sources.
// The feed is nothing but a read of a source, so it must not be mounted.
func TestPagesAreNotMountedWithoutAPanel(t *testing.T) {
	rec, _ := html(t, New(7777, "manual", nil), "/sonar")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 on a panel-less daemon", rec.Code)
	}
}

func mustFixture(t *testing.T) *Panel {
	t.Helper()
	p, _ := sonarFixture(t, sonarLog)
	return p
}

func rawsOf(t *testing.T, body map[string]any) []string {
	t.Helper()
	out := []string{}
	for _, raw := range body["events"].([]any) {
		out = append(out, raw.(map[string]any)["raw"].(string))
	}
	return out
}
