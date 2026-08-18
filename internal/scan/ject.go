package scan

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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

// CLI runs the real binary.
func CLI(args ...string) ([]byte, error) {
	return exec.Command("ject", args...).Output()
}

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
	var items []pendency.Pendency
	var states []SourceState
	graph := map[string][]string{}

	raw, err := run("recent", "--json")
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
	var decisionItems []pendency.Pendency
	decisionCount := 0

	for _, t := range recent.Tickets {
		if terminalStatus[t.Status] {
			continue
		}
		detail := showTicket(run, t.ID)
		graph[t.ID] = detail.Dependencies

		items = append(items, pendency.Pendency{
			// The ticket id is the most stable natural key a source can offer.
			ID:      pendency.NewID("ject:"+t.Project, t.ID),
			Class:   "B",
			Source:  "ject:" + t.Project,
			Title:   t.ID + " — " + t.Title,
			Detail:  ticketDetailLine(t, detail, open),
			Blocks:  ticketBlocker(t, detail, open),
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
	for name, count := range byProject {
		states = append(states, SourceState{Name: name, Count: count})
	}
	if decisionCount > 0 {
		states = append(states, SourceState{Name: "decisoes.md", Count: decisionCount})
	}
	return items, states, graph
}

func showTicket(run Runner, id string) ticketDetail {
	var d ticketDetail
	raw, err := run("ticket", "show", id, "--json")
	if err != nil {
		return d
	}
	_ = json.Unmarshal(raw, &d)
	return d
}

// ticketBlocker decides whose move a ticket is.
//
// A dependency that is still open, or a recorded blocker, means the ticket is
// waiting on something else -- that is a gate, not work an agent can pick up.
func ticketBlocker(t recentTicket, d ticketDetail, open map[string]bool) pendency.Blocker {
	if t.Blockers > 0 || len(unmet(d.Dependencies, open)) > 0 {
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
		parts = append(parts, "checklist "+itoa(t.Progress.Done)+"/"+itoa(t.Progress.Total))
	}
	if t.ActiveSession {
		parts = append(parts, "sessão ativa")
	}
	if blocked := unmet(d.Dependencies, open); len(blocked) > 0 {
		parts = append(parts, "bloqueado: depende de "+strings.Join(blocked, ", "))
	}
	return strings.Join(parts, " · ")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
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
