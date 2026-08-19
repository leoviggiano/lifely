package scan

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/leoviggiano/lifely/internal/pendency"
)

// Runner executes the ject binary and returns its JSON.
//
// lifely never reads or writes the vault's files for ticket data: ject already
// owns that, and reimplementing it would make a second, drifting source. The
// one exception is decisoes.md, which no ject command returns yet -- see
// decisions below (spec FR1.2, A7).
type Runner func(args ...string) ([]byte, error)

// CLI runs the real binary, under a deadline.
//
// The sweep runs on every request and fans out one subprocess per open ticket.
// Without a deadline, a single hung `ject` would hang the panel for as long as
// the operating system lets it -- and the founder would see a page that never
// finishes loading, with nothing to tell him why.
func CLI(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cliTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ject", args...).Output()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("ject %s: %w", strings.Join(args, " "), ctx.Err())
	}
	return out, err
}

// sweepBudget bounds the whole ject sweep, however many tickets it finds. It
// is a variable so a test can make exhaustion happen instead of describing it.
var sweepBudget = 8 * time.Second

// cliTimeout bounds a single ject call. Generous for a healthy binary, short
// enough that a hung one shows up as a marked source instead of a blank page.
const cliTimeout = 10 * time.Second

type project struct {
	Slug           string `json:"slug"`
	Name           string `json:"name"`
	RepositoryPath string `json:"repository_path"`
}

type recentTicket struct {
	ID       string `json:"id"`
	Project  string `json:"project"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Priority string `json:"priority"`
	Blockers int    `json:"blockers"`
	Progress struct {
		Done    int `json:"done"`
		Total   int `json:"total"`
		Percent int `json:"percent"`
	} `json:"progress"`
	ActiveSession bool `json:"active_session"`
}

type ticketDetail struct {
	ID           string   `json:"id"`
	Dir          string   `json:"dir"`
	Status       string   `json:"status"`
	Dependencies []string `json:"dependencies"`
}

// terminalStatus lists ticket states that are done deciding and done working.
var terminalStatus = map[string]bool{"done": true, "cancelled": true}

// Ject sweeps the ject vaults through the binary, and returns the pendencies
// plus the dependency graph that the queue's "blocked" state is computed from.
func Ject(run Runner, now time.Time) ([]pendency.Pendency, []SourceState, map[string][]string) {
	// The whole sweep is bounded, not just each call: one ticket per
	// subprocess times 88 open tickets is a page that never finishes loading
	// even when every individual call is inside its own deadline.
	deadline := time.Now().Add(sweepBudget)
	var items []pendency.Pendency
	var states []SourceState
	graph := map[string][]string{}

	// `--limit 0` means all. Without it ject defaults to 20, and the panel
	// silently showed the 20 most recent tickets as if they were every open
	// one: measured on 18-08, 88 tickets open and 20 on screen. A source that
	// truncates without saying so is worse than a source that fails.
	raw, err := run("recent", "--limit", "0", "--json")
	if err != nil {
		return nil, []SourceState{{Name: "ject", Err: describeExec("ject", err)}}, graph
	}
	var recent struct {
		Tickets []recentTicket `json:"tickets"`
	}
	if err := json.Unmarshal(raw, &recent); err != nil {
		return nil, []SourceState{{Name: "ject", Err: err.Error()}}, graph
	}

	open := map[string]bool{}
	for _, t := range recent.Tickets {
		if !terminalStatus[t.Status] {
			open[t.ID] = true
		}
	}

	byProject := map[string]int{}
	detailErrs := map[string]string{}
	budgetCut := map[string]int{}
	var decisionItems []pendency.Pendency
	decisionCount := 0
	decisionErr := ""

	for _, t := range recent.Tickets {
		if terminalStatus[t.Status] {
			continue
		}
		// The budget buys DETAIL, not the row. Breaking out of the loop here
		// dropped every remaining ticket from the panel entirely -- the sweep
		// went quiet about the majority of the vault while reporting only that
		// they "were not detailed". The listing is already in hand and cost
		// nothing; only `ject ticket show` is expensive. So past the deadline
		// we keep emitting rows and stop paying for their detail.
		var detail ticketDetail
		var derr error
		if time.Now().After(deadline) {
			budgetCut["ject:"+t.Project]++
			derr = errBudgetSpent
		} else {
			detail, derr = showTicket(run, t.ID)
		}
		if derr == nil {
			// Only record a graph edge set we actually read. Storing the zero
			// value on failure would publish "no dependencies" as a fact, and
			// the queue computes readiness from this map.
			graph[t.ID] = detail.Dependencies
		}
		if derr != nil && !errors.Is(derr, errBudgetSpent) {
			// And it changes the ANSWER, not just the log: a ticket whose
			// detail we could not read has unknown dependencies, and the queue
			// must not offer it as ready on the strength of a failed read.
			key := "ject:" + t.Project
			if detailErrs[key] != "" {
				detailErrs[key] += "; "
			}
			detailErrs[key] += derr.Error()
		}
		items = append(items, pendency.Pendency{
			// The ticket id is the most stable natural key a source can offer.
			ID:      pendency.NewID("ject:"+t.Project, t.ID),
			Class:   "B",
			Source:  "ject:" + t.Project,
			Title:   t.ID + " — " + t.Title,
			Detail:  ticketDetailLine(t, detail, open, derr != nil, errors.Is(derr, errBudgetSpent)),
			Blocks:  ticketBlocker(t, detail, open, derr != nil),
			Origin:  pendency.Origin{Path: detail.Dir, Locator: t.ID, Open: "ject ticket show " + t.ID},
			Surface: "ject start " + t.ID + " --attached",
			SeenAt:  now,
		})
		byProject["ject:"+t.Project]++

		// A7: the founder's decision queue for this ticket.
		found, decErr := decisions(detail.Dir, t.ID, now, derr == nil)
		decisionItems = append(decisionItems, found...)
		decisionCount += len(found)
		if decErr != "" {
			if decisionErr != "" {
				decisionErr += "; "
			}
			decisionErr += t.ID + ": " + decErr
		}
	}

	items = append(items, decisionItems...)
	// Deterministic order: Go randomises map iteration, and a panel whose
	// source list reshuffles on every sweep reads as if something changed.
	names := make([]string, 0, len(byProject))
	for name := range byProject {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		st := SourceState{Name: name, Count: byProject[name], Err: detailErrs[name]}
		// Only the projects actually cut carry the note, with their own count:
		// a project swept in full was being labelled "partial" and handed a
		// number of tickets belonging to someone else.
		if cut := budgetCut[name]; cut > 0 {
			if st.Err != "" {
				st.Err += "; "
			}
			st.Err += fmt.Sprintf("sweep stopped at its %s budget; %d of its open tickets are listed without detail", sweepBudget, cut)
		}
		states = append(states, st)
	}
	if decisionCount > 0 || decisionErr != "" || len(budgetCut) > 0 {
		// The budget cut applies here too: tickets we never opened may hold
		// decisions waiting on the founder, so this source under-reports for
		// the same reason the ject ones do -- and it is the one source where
		// silence is least affordable.
		err := decisionErr
		if cut := len(budgetCut); cut > 0 {
			if err != "" {
				err += "; "
			}
			err += fmt.Sprintf("the sweep budget was spent; tickets listed without detail in %d project(s) were not read for decisions", cut)
		}
		states = append(states, SourceState{Name: "decisoes.md", Count: decisionCount, Err: err})
	}
	return items, states, graph
}

// showTicket reads one ticket's detail, reporting what it could not read.
//
// Swallowing these errors made a ticket with an unreadable detail look like a
// ticket with no dependencies -- which is exactly the state that decides
// whether the queue offers it as ready.
func showTicket(run Runner, id string) (ticketDetail, error) {
	var d ticketDetail
	raw, err := run("ticket", "show", id, "--json")
	if err != nil {
		// describeExec keeps git/ject's own words; the bare error is only
		// "exit status 1", which says nothing about what the tool refused on.
		return d, fmt.Errorf("%s: %s", id, describeExec("ject", err))
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return d, fmt.Errorf("%s: %w", id, err)
	}
	return d, nil
}

// ticketBlocker decides whose move a ticket is.
//
// A dependency that is still open, or a recorded blocker, means the ticket is
// waiting on something else -- that is a gate, not work an agent can pick up.
func ticketBlocker(t recentTicket, d ticketDetail, open map[string]bool, unknown bool) pendency.Blocker {
	// Unknown dependencies count as blocked: reading failed, so "no
	// dependencies" is an absence of information, not a fact.
	if unknown || t.Blockers > 0 || len(unmet(d.Dependencies, open)) > 0 {
		return pendency.Gate
	}
	return pendency.AI
}

// unmet returns the dependencies that have not closed yet.
//
// A dependency absent from `open` is treated as met, and that rests on two
// facts, not on optimism: the sweep asks for EVERY open ticket (`--limit 0`,
// after the truncation bug this file already paid for), and `ject doctor`
// validates that a dependency names a ticket that exists. So absent from the
// open set means terminal -- done or cancelled -- which is exactly "met".
//
// The case this does NOT cover is a dependency pointing at a ticket that never
// existed; that is a vault error, it belongs to `ject doctor`, and inventing a
// blocked state here would hide it behind a panel that looks merely cautious.
func unmet(deps []string, open map[string]bool) []string {
	var out []string
	for _, dep := range deps {
		if open[dep] {
			out = append(out, dep)
		}
	}
	return out
}

func ticketDetailLine(t recentTicket, d ticketDetail, open map[string]bool, unknown, budget bool) string {
	parts := []string{t.Status, t.Priority}
	if t.Progress.Total > 0 {
		parts = append(parts, "checklist "+strconv.Itoa(t.Progress.Done)+"/"+strconv.Itoa(t.Progress.Total))
	}
	if t.ActiveSession {
		parts = append(parts, "active session")
	}
	if blocked := unmet(d.Dependencies, open); len(blocked) > 0 {
		parts = append(parts, "blocked: depends on "+strings.Join(blocked, ", "))
	}
	switch {
	case budget:
		// Never say "could not be read" about a call that was never made: the
		// founder would go looking for a broken vault instead of a slow one.
		parts = append(parts, "blocked: not detailed, the sweep budget was spent; dependencies unknown")
	case unknown:
		// Say WHY it is held back. A ticket marked `gate` with no reason reads
		// as a judgement; it is an admission that we could not read its
		// dependencies.
		parts = append(parts, "blocked: detail could not be read, dependencies unknown")
	}
	return strings.Join(parts, " · ")
}

// tool names the binary that failed. Folding dirtyTree into this function
// (rightly, to keep one owner for "keep the tool's own words") carried ject's
// name to git's failures: a host without git reported that ject was missing.
// Deduplicating two things that are only ALMOST the same moves the difference
// into a parameter -- it does not delete it.
func describeExec(tool string, err error) string {
	if _, ok := err.(*exec.Error); ok {
		return tool + " is not on PATH -- this source is unavailable"
	}
	// Keep what the binary said. Output() puts stderr in ExitError.Stderr and
	// the bare error is only "exit status 1", which tells the reader nothing
	// about which invariant the tool refused on.
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(ee.Stderr) > 0 {
		return err.Error() + ": " + strings.TrimSpace(string(ee.Stderr))
	}
	return err.Error()
}

// --- A7: decisoes.md, the founder's decision queue --------------------------

// statusIsPending reads the STATUS WORD, not the line.
//
// `strings.Contains(line, "pendente")` counted `**Status:** decidido (antes:
// pendente)` as waiting on the founder -- the word appears in the note that
// says the opposite. The template writes a marker emoji before the word
// (`**Status:** 🟡 pendente`), so the emoji is skipped and only the first
// actual word decides.
func statusIsPending(value string) bool {
	for _, field := range strings.Fields(value) {
		// Skip the marker emoji: it carries no letters.
		if !strings.ContainsFunc(field, unicode.IsLetter) {
			continue
		}
		// plainText first: the template's own emphasis (`**pendente**`) made
		// the comparison fail, and this direction is the dangerous one -- a
		// decision that IS waiting drops out of the founder's queue.
		word := strings.ToLower(strings.Trim(plainText(field), ",.;:()"))
		return word == "pendente"
	}
	return false
}

// errBudgetSpent marks a row whose detail was never ATTEMPTED. It travels the
// same path as a failed read because both leave dependencies unknown, but the
// reason shown to the reader must differ: "could not be read" about a call
// that was never made sends the founder looking for a broken vault.
var errBudgetSpent = errors.New("detail not read: the sweep budget was spent")

// decisions reads the ticket's decision queue.
//
// This is the one source read as a file rather than through a command: no ject
// command returns decisoes.md yet. If one appears, this should use it -- the
// exception is declared in the spec, not smuggled in here.
func decisions(dir, ticketID string, now time.Time, detailRead bool) ([]pendency.Pendency, string) {
	if dir == "" {
		if !detailRead {
			// The detail read already failed and was already reported; saying
			// it twice would double-count the same blindness.
			return nil, ""
		}
		// The read SUCCEEDED and still gave no directory: we cannot look at
		// the founder's decision queue, and answering "nothing pending" is the
		// silence this source can least afford.
		// Unprefixed, like every other error this function returns: the caller
		// adds the ticket id, and doing it here too produced "b-1: b-1: ...".
		return nil, "ticket directory unknown, decision queue not read"
	}
	path := filepath.Join(dir, "decisoes.md")
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "" // no queue for this ticket: the normal case
		}
		// A decision queue that EXISTS and cannot be read is the worst thing
		// this scanner can hide: it is the founder's own list, and reporting
		// "nothing pending" would be a lie with his name on it.
		return nil, err.Error()
	}
	defer f.Close()

	var items []pendency.Pendency
	var id, title string
	var body []string
	pendingBlock := false

	flush := func() {
		if id != "" && pendingBlock {
			items = append(items, pendency.Pendency{
				ID:     pendency.NewID("decisao:"+ticketID, strings.ToLower(id)),
				Class:  "A7",
				Source: "decisoes.md · " + ticketID,
				Title:  ticketID + " " + id + " — " + title,
				// The whole block travels: the options and their costs are the
				// decision surface, and summarising them would decide for him.
				Detail:  strings.TrimSpace(strings.Join(body, "\n")),
				Blocks:  pendency.Founder,
				Origin:  pendency.Origin{Path: path, Locator: id, Open: obsidianURI(path)},
				Surface: "the founder\u0027s word, in the block\u0027s Decision field",
				SeenAt:  now,
			})
		}
		id, title, body, pendingBlock = "", "", nil, false
	}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "## ") {
			flush()
			head := strings.TrimPrefix(line, "## ")
			id, title, _ = strings.Cut(head, " · ")
			id = strings.TrimSpace(id)
			title = strings.TrimSpace(title)
			continue
		}
		if id == "" {
			continue
		}
		if v, ok := strings.CutPrefix(line, "**Status:**"); ok {
			pendingBlock = statusIsPending(v)
		}
		body = append(body, line)
	}
	flush()
	if err := sc.Err(); err != nil {
		return items, err.Error()
	}
	return items, ""
}
