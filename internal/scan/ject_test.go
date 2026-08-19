package scan

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leoviggiano/lifely/internal/pendency"
)

// fakeJect answers like the real binary, so the scanner can be tested without
// a vault -- and so a change in what lifely ASKS for shows up as a test
// failure rather than as an empty panel.
// dirOf gives every ticket a directory, as ject does, unless the test asked
// for a specific one.
func dirOf(dirs map[string]string, id string, t *testing.T) string {
	if d, ok := dirs[id]; ok {
		return d
	}
	return t.TempDir()
}

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
				// A successful `ticket show` always carries a dir. Returning ""
				// here made the fixture describe a state ject never produces,
				// and a real anomaly looked like a test failure.
				"id": id, "dir": dirOf(dirs, id, t), "status": "planning", "dependencies": deps,
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
	if !strings.Contains(blocked.Detail, "depends on ject-070") {
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

// The sweep must ask for ALL tickets, not the 20 most recent.
//
// `ject recent` defaults to --limit 20, so the panel silently showed a slice
// of the source as if it were the whole: measured on 18-08, 88 tickets open
// and 20 on screen. A source that truncates without saying so is worse than
// one that fails.
func TestJectAsksForEveryTicket(t *testing.T) {
	var asked []string
	run := func(args ...string) ([]byte, error) {
		if args[0] == "recent" {
			asked = args
			return []byte(`{"tickets":[]}`), nil
		}
		return []byte("{}"), nil
	}
	Ject(run, time.Now())

	var unlimited bool
	for i, a := range asked {
		if a == "--limit" && i+1 < len(asked) && asked[i+1] == "0" {
			unlimited = true
		}
	}
	if !unlimited {
		t.Errorf("recent was called as %v, without --limit 0", asked)
	}
}

// A ticket whose detail cannot be read must not look like a ticket with no
// dependencies -- that is the state deciding whether the queue offers it.
func TestJectReportsUnreadableTicketDetail(t *testing.T) {
	run := func(args ...string) ([]byte, error) {
		switch args[0] {
		case "recent":
			return []byte(`{"tickets":[{"id":"x-1","project":"p","title":"t","status":"ready"}]}`), nil
		default:
			return nil, os.ErrPermission
		}
	}
	_, states, _ := Ject(run, time.Now())

	var reported bool
	for _, s := range states {
		if s.Err != "" {
			reported = true
		}
	}
	if !reported {
		t.Error("an unreadable ticket detail was swallowed")
	}
}

// The source list must not reshuffle between sweeps: Go randomises map order,
// and a panel that reorders itself reads as if something changed.
func TestJectSourceOrderIsStable(t *testing.T) {
	run := fakeJect(t, nil)
	var first []string
	for i := 0; i < 5; i++ {
		_, states, _ := Ject(run, time.Now())
		var names []string
		for _, s := range states {
			names = append(names, s.Name)
		}
		if i == 0 {
			first = names
			continue
		}
		if strings.Join(names, ",") != strings.Join(first, ",") {
			t.Fatalf("source order changed between sweeps: %v then %v", first, names)
		}
	}
}

// A ticket whose detail could not be read has UNKNOWN dependencies, and the
// queue must not offer it as ready on the strength of a failed read.
//
// The earlier fix only reported the error; the answer stayed the same. Saying
// "I could not read this" while still marking it ready is worse than silence,
// because the queue looks considered.
func TestUnreadableDetailIsBlockedNotReady(t *testing.T) {
	run := func(args ...string) ([]byte, error) {
		if args[0] == "recent" {
			return []byte(`{"tickets":[{"id":"x-1","project":"p","title":"t","status":"ready"}]}`), nil
		}
		return nil, os.ErrPermission
	}
	items, _, _ := Ject(run, time.Now())
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if items[0].Blocks != pendency.Gate {
		t.Errorf("a ticket with unreadable detail blocks %q, want %q", items[0].Blocks, pendency.Gate)
	}
}

// The detail error belongs to the project it came from, not to every project
// in the sweep.
func TestDetailErrorIsNotSmearedAcrossProjects(t *testing.T) {
	run := func(args ...string) ([]byte, error) {
		if args[0] == "recent" {
			return []byte(`{"tickets":[
				{"id":"a-1","project":"alfa","title":"t","status":"ready"},
				{"id":"b-1","project":"beta","title":"t","status":"ready"}]}`), nil
		}
		if args[2] == "a-1" {
			return nil, os.ErrPermission
		}
		return []byte(`{"id":"b-1","dir":"","dependencies":[]}`), nil
	}
	_, states, _ := Ject(run, time.Now())
	for _, s := range states {
		if s.Name == "ject:beta" && s.Err != "" {
			t.Errorf("beta was stamped with alfa's error: %q", s.Err)
		}
	}
}

// A failed detail read must not publish "no dependencies" into the graph.
//
// The queue computes readiness from that map, so a zero value stored on
// failure is an absence of information dressed as a fact -- the same shape as
// the --limit 20 this file already paid for.
func TestGraphOmitsTicketsWhoseDetailFailed(t *testing.T) {
	run := func(args ...string) ([]byte, error) {
		if args[0] == "recent" {
			return []byte(`{"tickets":[{"id":"x-1","project":"p","title":"t","status":"ready"}]}`), nil
		}
		return nil, os.ErrPermission
	}
	_, _, graph := Ject(run, time.Now())
	if _, present := graph["x-1"]; present {
		t.Error("the graph recorded dependencies for a ticket whose detail could not be read")
	}
}

// When the sweep runs out of budget the tickets stay ON THE PANEL -- only
// their detail is missing. The first version of this test asserted that the
// project LINES survived and never looked at the rows, so it stayed green
// while the sweep silently dropped ~85 of 90 open tickets: the budget check
// broke out of the loop before the row was appended. The budget buys detail,
// never the ticket's existence.
// The row must name WHICH of the two blindnesses it is under. "could not be
// read" about a `ticket show` that was never invoked sends the reader after a
// broken vault when the vault is merely large.
// The missing-binary sentence must name the binary that is missing. One owner
// for "keep the tool's own words" is right; one hardcoded NAME inside it is
// how git's absence came to be reported as ject's.
func TestDescribeExecNamesTheToolThatFailed(t *testing.T) {
	_, err := exec.Command("definitely-not-a-real-binary-xyz").Output()
	if err == nil {
		t.Skip("the impossible binary exists here")
	}
	if got := describeExec("git", err); !strings.Contains(got, "git") {
		t.Errorf("describeExec(\"git\", ...) = %q, want it to name git", got)
	}
	if got := describeExec("git", err); strings.Contains(got, "ject") {
		t.Errorf("describeExec(\"git\", ...) = %q: it blames ject for git", got)
	}
}

func TestBudgetCutSaysBudget_NotUnreadable(t *testing.T) {
	original := sweepBudget
	sweepBudget = time.Nanosecond
	t.Cleanup(func() { sweepBudget = original })

	run := func(args ...string) ([]byte, error) {
		if args[0] == "recent" {
			return []byte(`{"tickets":[{"id":"a-1","project":"alfa","title":"t","status":"ready"}]}`), nil
		}
		return nil, nil
	}
	items, _, _ := Ject(run, time.Now())
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if strings.Contains(items[0].Detail, "could not be read") {
		t.Errorf("a ticket never asked about is reported as unreadable: %q", items[0].Detail)
	}
	if !strings.Contains(items[0].Detail, "budget") {
		t.Errorf("the row does not say the budget was spent: %q", items[0].Detail)
	}
}

func TestBudgetExhaustionKeepsTicketsListed(t *testing.T) {
	original := sweepBudget
	sweepBudget = time.Nanosecond // exhausted before the first ticket
	t.Cleanup(func() { sweepBudget = original })

	run := func(args ...string) ([]byte, error) {
		if args[0] == "recent" {
			return []byte(`{"tickets":[
				{"id":"a-1","project":"alfa","title":"t","status":"ready"},
				{"id":"b-1","project":"beta","title":"t","status":"ready"}]}`), nil
		}
		t.Error("the sweep paid for a ticket detail after its budget was spent")
		return []byte(`{"id":"a-1","dependencies":[]}`), nil
	}

	items, states, _ := Ject(run, time.Now())

	listed := map[string]bool{}
	for _, it := range items {
		listed[it.Title] = true
	}
	for _, want := range []string{"a-1 — t", "b-1 — t"} {
		if !listed[want] {
			t.Errorf("%q vanished from the panel when the budget ran out", want)
		}
	}

	seen := map[string]string{}
	for _, s := range states {
		seen[s.Name] = s.Err
	}
	for _, want := range []string{"ject:alfa", "ject:beta"} {
		msg, present := seen[want]
		if !present {
			t.Errorf("%s vanished from the panel when the budget ran out", want)
			continue
		}
		if !strings.Contains(msg, "budget") {
			t.Errorf("%s was listed without saying the sweep was cut: %q", want, msg)
		}
	}
}

// A project swept in FULL must not be labelled partial, and must never be
// handed a count of tickets belonging to another project. The budget note is
// per project, because "partial" is a claim about that project's rows.
func TestBudgetNoteOnlyMarksTheProjectsActuallyCut(t *testing.T) {
	original := sweepBudget
	sweepBudget = 50 * time.Millisecond
	t.Cleanup(func() { sweepBudget = original })

	calls := 0
	run := func(args ...string) ([]byte, error) {
		if args[0] == "recent" {
			return []byte(`{"tickets":[
				{"id":"a-1","project":"alfa","title":"t","status":"ready"},
				{"id":"b-1","project":"beta","title":"t","status":"ready"},
				{"id":"b-2","project":"beta","title":"t","status":"ready"}]}`), nil
		}
		calls++
		if calls == 1 {
			// alfa is complete, and this detail costs more than the whole
			// budget -- so the cut lands exactly on beta's first ticket.
			// The deadline is fixed when the sweep starts, so it has to be
			// real time that passes here, not a rewritten sweepBudget.
			time.Sleep(80 * time.Millisecond)
			return []byte(`{"id":"a-1","dependencies":[]}`), nil
		}
		t.Error("the sweep paid for a ticket detail after its budget was spent")
		return nil, nil
	}

	_, states, _ := Ject(run, time.Now())

	for _, s := range states {
		switch s.Name {
		case "ject:alfa":
			if s.Err != "" {
				t.Errorf("alfa was swept in full but reads as partial: %q", s.Err)
			}
		case "ject:beta":
			if !strings.Contains(s.Err, "2 of its open tickets") {
				t.Errorf("beta must own its own cut count, got %q", s.Err)
			}
		}
	}
}

// The founder's own decision queue must never disappear quietly.
//
// A `decisoes.md` that exists and cannot be read was reported as "no queue for
// this ticket" -- the panel would say nothing is pending, in his name, about
// the file that holds what is pending for him.
func TestUnreadableDecisionQueueIsAFinding(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not block reads")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "decisoes.md")
	if err := os.WriteFile(path, []byte("## D1 · algo\n\n**Status:** pendente\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Skipf("cannot make a file unreadable here: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

	run := fakeJect(t, map[string]string{"ject-070": dir})
	_, states, _ := Ject(run, time.Now())

	var reported bool
	for _, s := range states {
		if s.Name == "decisoes.md" && s.Err != "" {
			reported = true
		}
	}
	if !reported {
		t.Error("an unreadable decision queue was swallowed as 'nothing pending'")
	}
}
