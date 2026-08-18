package scan

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

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
		return nil, []SourceState{{Name: "ject", Err: describeExec(err)}}, graph
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
	unvisited := map[string]bool{}
	budgetErr := ""
	var decisionItems []pendency.Pendency
	decisionCount := 0

	for i, t := range recent.Tickets {
		if terminalStatus[t.Status] {
			continue
		}
		if time.Now().After(deadline) {
			// Say what was cut, and count only what would have been shown --
			// recent.Tickets includes the terminal ones this loop skips.
			// A truncated sweep that stays quiet is the same failure as the
			// --limit 20 this file already paid for.
			left := 0
			for _, rest := range recent.Tickets[i:] {
				if !terminalStatus[rest.Status] {
					left++
					// And the projects we never reached still get a line, so
					// they do not vanish from the panel along with the budget.
					unvisited["ject:"+rest.Project] = true
				}
			}
			budgetErr = fmt.Sprintf("varredura interrompida no orcamento de %s; %d tickets abertos nao foram detalhados", sweepBudget, left)
			break
		}
		detail, derr := showTicket(run, t.ID)
		if derr == nil {
			// Only record a graph edge set we actually read. Storing the zero
			// value on failure would publish "no dependencies" as a fact, and
			// the queue computes readiness from this map.
			graph[t.ID] = detail.Dependencies
		}
		if derr != nil {
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
			Detail:  ticketDetailLine(t, detail, open),
			Blocks:  ticketBlocker(t, detail, open, derr != nil),
			Origin:  pendency.Origin{Path: detail.Dir, Locator: t.ID, Open: "ject ticket show " + t.ID},
			Surface: "ject start " + t.ID + " --attached",
			SeenAt:  now,
		})
		byProject["ject:"+t.Project]++

		// A7: the founder's decision queue for this ticket.
		found := decisions(detail.Dir, t.ID, now)
		decisionItems = append(decisionItems, found...)
		decisionCount += len(found)
	}

	items = append(items, decisionItems...)
	// Deterministic order: Go randomises map iteration, and a panel whose
	// source list reshuffles on every sweep reads as if something changed.
	names := make([]string, 0, len(byProject)+len(unvisited))
	for name := range byProject {
		names = append(names, name)
	}
	for name := range unvisited {
		if _, seen := byProject[name]; !seen {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		st := SourceState{Name: name, Count: byProject[name], Err: detailErrs[name]}
		if budgetErr != "" {
			if st.Err != "" {
				st.Err += "; "
			}
			st.Err += budgetErr
		}
		states = append(states, st)
	}
	if decisionCount > 0 {
		states = append(states, SourceState{Name: "decisoes.md", Count: decisionCount})
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
		return d, fmt.Errorf("%s: %w", id, err)
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
func unmet(deps []string, open map[string]bool) []string {
	var out []string
	for _, dep := range deps {
		if open[dep] {
			out = append(out, dep)
		}
	}
	return out
}

func ticketDetailLine(t recentTicket, d ticketDetail, open map[string]bool) string {
	parts := []string{t.Status, t.Priority}
	if t.Progress.Total > 0 {
		parts = append(parts, "checklist "+strconv.Itoa(t.Progress.Done)+"/"+strconv.Itoa(t.Progress.Total))
	}
	if t.ActiveSession {
		parts = append(parts, "sessão ativa")
	}
	if blocked := unmet(d.Dependencies, open); len(blocked) > 0 {
		parts = append(parts, "bloqueado: depende de "+strings.Join(blocked, ", "))
	}
	return strings.Join(parts, " · ")
}

func describeExec(err error) string {
	if _, ok := err.(*exec.Error); ok {
		return "ject nao esta no PATH -- a fonte ject fica indisponivel"
	}
	return err.Error()
}

// --- A7: decisoes.md, the founder's decision queue --------------------------

// decisions reads the ticket's decision queue.
//
// This is the one source read as a file rather than through a command: no ject
// command returns decisoes.md yet. If one appears, this should use it -- the
// exception is declared in the spec, not smuggled in here.
func decisions(dir, ticketID string, now time.Time) []pendency.Pendency {
	if dir == "" {
		return nil
	}
	path := filepath.Join(dir, "decisoes.md")
	f, err := os.Open(path)
	if err != nil {
		return nil // no queue for this ticket is the normal case, not a finding
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
				Origin:  pendency.Origin{Path: path, Locator: id, Open: "obsidian://open?path=" + path},
				Surface: "a palavra do fundador, no campo Decisão do bloco",
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
		if strings.HasPrefix(line, "**Status:**") && strings.Contains(line, "pendente") {
			pendingBlock = true
		}
		body = append(body, line)
	}
	flush()
	return items
}
