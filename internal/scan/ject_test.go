package scan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leoviggiano/lifely/internal/pendency"
)

// fakeJect answers like the real binary, so the scanner can be tested without
// a vault -- and so a change in what lifely ASKS for shows up as a test
// failure rather than as an empty panel.
func fakeJect(t *testing.T, dirs map[string]string) Runner {
	t.Helper()
	return func(args ...string) ([]byte, error) {
		switch {
		case args[0] == "recent":
			return json.Marshal(map[string]any{"tickets": []map[string]any{
				{"id": "ject-070", "project": "ject", "title": "emendas E14", "status": "planning",
					"priority": "urgent", "blockers": 0, "active_session": true,
					"progress": map[string]int{"done": 1, "total": 4}},
				{"id": "ject-071", "project": "ject", "title": "schema knowledge", "status": "ready",
					"priority": "urgent", "blockers": 0,
					"progress": map[string]int{"done": 0, "total": 3}},
				{"id": "ject-060", "project": "ject", "title": "ja fechado", "status": "done"},
			}})
		case args[0] == "ticket" && args[1] == "show":
			id := args[2]
			deps := []string{}
			if id == "ject-071" {
				deps = []string{"ject-070"}
			}
			return json.Marshal(map[string]any{
				"id": id, "dir": dirs[id], "status": "planning", "dependencies": deps,
			})
		}
		return []byte("{}"), nil
	}
}

func TestJectReadsOpenTicketsAndGraph(t *testing.T) {
	items, states, graph := Ject(fakeJect(t, nil), time.Now())

	if len(items) != 2 {
		t.Fatalf("got %d tickets, want 2 (the done one must be dropped)", len(items))
	}
	if got := graph["ject-071"]; len(got) != 1 || got[0] != "ject-070" {
		t.Errorf("graph for ject-071 = %v, want [ject-070]", got)
	}

	byID := map[string]pendency.Pendency{}
	for _, p := range items {
		byID[p.ID] = p
	}

	// A ticket whose dependency is still open is waiting on a gate, not work
	// an agent can pick up.
	blocked := byID["ject:ject:ject-071"]
	if blocked.Blocks != pendency.Gate {
		t.Errorf("ject-071 blocks %q, want %q", blocked.Blocks, pendency.Gate)
	}
	if !strings.Contains(blocked.Detail, "depende de ject-070") {
		t.Errorf("the blocked ticket does not name what it waits on: %q", blocked.Detail)
	}
	free := byID["ject:ject:ject-070"]
	if free.Blocks != pendency.AI {
		t.Errorf("ject-070 blocks %q, want %q", free.Blocks, pendency.AI)
	}
	// Every ticket points at the one way work may start (spec FR8.1).
	if free.Surface != "ject start ject-070 --attached" {
		t.Errorf("surface = %q, want the --attached command", free.Surface)
	}

	if len(states) == 0 {
		t.Error("no source state was reported for ject")
	}
}

// ject missing from the PATH must degrade the source, not the panel.
func TestJectMissingIsAFinding(t *testing.T) {
	run := func(args ...string) ([]byte, error) { return nil, os.ErrNotExist }
	items, states, _ := Ject(run, time.Now())
	if len(items) != 0 {
		t.Errorf("got %d items from a broken ject, want 0", len(items))
	}
	if len(states) != 1 || states[0].Err == "" {
		t.Fatalf("a broken ject was not reported as a finding: %+v", states)
	}
}

// A7: a pending decision block is a pendency waiting on the founder, and the
// whole block travels -- the options and their costs are the decision surface.
func TestDecisionsReadsOnlyPendingBlocks(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "decisoes.md"), `# Decisões — bot-048

---

## D1 · nome do canal

**Status:** 🟡 pendente

**A pergunta:** o canal se chama avisos ou anuncios?

| | Opção | Custo real |
|---|---|---|
| **A** | avisos | perde o termo do cliente |

**Recomendo A** — porque sim.

**Decisão:** —

---

## D2 · ja decidida

**Status:** ✅ aprovado, fundador, 18-08-2026

**A pergunta:** algo ja resolvido?

**Decisão:** aprovado
`)
	run := fakeJect(t, map[string]string{"ject-070": dir})
	items, states, _ := Ject(run, time.Now())

	var found []pendency.Pendency
	for _, p := range items {
		if p.Class == "A7" {
			found = append(found, p)
		}
	}
	if len(found) != 1 {
		t.Fatalf("got %d decision items, want 1 (only D1 is pending)", len(found))
	}
	d := found[0]
	if d.Blocks != pendency.Founder {
		t.Errorf("a pending decision blocks %q, want %q", d.Blocks, pendency.Founder)
	}
	if !strings.Contains(d.Detail, "Opção") || !strings.Contains(d.Detail, "Recomendo A") {
		t.Error("the block did not travel whole: options or recommendation missing")
	}
	if !strings.Contains(d.ID, "d1") {
		t.Errorf("decision id = %q, want it keyed by the block id", d.ID)
	}

	var reported bool
	for _, s := range states {
		if s.Name == "decisoes.md" && s.Count == 1 {
			reported = true
		}
	}
	if !reported {
		t.Error("decisoes.md was not reported as a source")
	}
}

// A ticket without a decision queue is the normal case, not a finding.
func TestNoDecisionsFileIsSilent(t *testing.T) {
	items, states, _ := Ject(fakeJect(t, map[string]string{"ject-070": t.TempDir()}), time.Now())
	for _, p := range items {
		if p.Class == "A7" {
			t.Error("a ticket with no decisoes.md produced a decision item")
		}
	}
	for _, s := range states {
		if s.Name == "decisoes.md" {
			t.Error("decisoes.md took a line in the panel with nothing pending")
		}
	}
}
