// Package scan reads the house's sources and turns what is still open into
// pendencies.
//
// Everything here is discovery, never a fixed list. A new ledger has to appear
// in the panel by being born, not by someone remembering to edit this file --
// that is the rule the tribunal's own panel is built on, and the reason it has
// no inventory of sources (spec FR1.1).
package scan

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/leoviggiano/lifely/internal/pendency"
)

// SourceState is what one source had to say this scan: how many open items it
// held, or why it could not be read.
//
// A source that exists and cannot be read is a finding, never silence: it goes
// to the panel marked (spec FR1.3).
type SourceState struct {
	Name  string
	Path  string
	Count int
	Err   string
}

// Result is one whole sweep.
type Result struct {
	Pendencies []pendency.Pendency
	Sources    []SourceState
	At         time.Time
}

// Tribunal sweeps the house's record repository.
func Tribunal(root string) Result {
	now := time.Now()
	res := Result{At: now}

	for _, sweep := range []func(string, time.Time) ([]pendency.Pendency, SourceState){
		founderBoard, ledgers, latestSummary, agenda, dirtyTree, lifeMarkers,
	} {
		items, state := sweep(root, now)
		res.Pendencies = append(res.Pendencies, items...)
		// A source with nothing open is omitted: a "0 pending" line is noise,
		// and the panel is an index, not an inventory (spec FR1.4).
		if state.Count > 0 || state.Err != "" {
			res.Sources = append(res.Sources, state)
		}
	}

	pendency.Sort(res.Pendencies)
	return res
}

// --- A1: FOUNDER.md, the founder's own board -------------------------------

var faixaHeading = regexp.MustCompile(`^##\s+Faixa\s+(\d+)`)

func founderBoard(root string, now time.Time) ([]pendency.Pendency, SourceState) {
	path := filepath.Join(root, "FOUNDER.md")
	state := SourceState{Name: "FOUNDER.md", Path: path}

	f, err := os.Open(path)
	if err != nil {
		state.Err = err.Error()
		return nil, state
	}
	defer f.Close()

	var items []pendency.Pendency
	var faixa string
	var current *pendency.Pendency

	flush := func() {
		if current != nil {
			current.Detail = strings.TrimSpace(current.Detail)
			items = append(items, *current)
			current = nil
		}
	}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if m := faixaHeading.FindStringSubmatch(line); m != nil {
			flush()
			faixa = m[1]
			continue
		}
		switch {
		case strings.HasPrefix(line, "- [ ] "):
			flush()
			title := strings.TrimPrefix(line, "- [ ] ")
			p := pendency.Pendency{
				ID:      pendency.NewID("founder", pendency.Slug(firstSentence(title))),
				Class:   "A1",
				Source:  "FOUNDER.md",
				Title:   strings.TrimSpace(title),
				Detail:  title,
				Blocks:  faixaBlocker(faixa),
				Origin:  pendency.Origin{Path: path, Locator: "Faixa " + faixa, Open: "obsidian://open?path=" + path},
				Surface: "veredito no FOUNDER.md",
				SeenAt:  now,
			}
			current = &p
		case strings.HasPrefix(line, "- [x] "), strings.HasPrefix(line, "## "):
			// A closed item or a new section ends whatever was open.
			flush()
		case current != nil && strings.HasPrefix(line, "  "):
			current.Detail += "\n" + line
		default:
			flush()
		}
	}
	flush()
	if err := sc.Err(); err != nil {
		state.Err = err.Error()
	}
	state.Count = len(items)
	return items, state
}

// faixaBlocker maps a board lane to whose move it is. Faixa 1 is the founder's
// own agenda; 2 and 3 are the agents' -- though the verdict on Faixa 2 still
// ends up being his, which the panel says out loud (spec FR2.3).
func faixaBlocker(faixa string) pendency.Blocker {
	if faixa == "1" {
		return pendency.Founder
	}
	return pendency.AI
}

func firstSentence(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "*", ""))
	for _, cut := range []string{" — ", " – ", ". ", ": "} {
		if i := strings.Index(s, cut); i > 0 {
			s = s[:i]
		}
	}
	if len(s) > 90 {
		s = s[:90]
	}
	return strings.TrimSpace(s)
}

// --- A2: every ledger with a status column ---------------------------------

// terminal lists the states that mean "done deciding". Anything else counts as
// open: showing one row too many is a smaller failure than hiding a decision
// that is still waiting.
var terminal = map[string]bool{
	"aprovada": true, "aprovado": true, "aplicada": true, "aplicado": true,
	"decidida": true, "decidido": true, "concluida": true, "concluido": true,
	"cancelada": true, "cancelado": true, "desistida": true, "desistiu": true,
	"rejeitada": true, "rejeitado": true, "arquivada": true, "arquivado": true,
	"": true,
}

func ledgers(root string, now time.Time) ([]pendency.Pendency, SourceState) {
	state := SourceState{Name: "ledgers *.tsv", Path: root}
	var items []pendency.Pendency

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // an unreadable branch is reported by its own source
		}
		if d.IsDir() {
			// sessions/ is append-only history, and .git is not a source.
			if name := d.Name(); name == ".git" || name == "sessions" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".tsv" {
			return nil
		}
		found, ferr := ledgerRows(root, path, now)
		if ferr != nil {
			state.Err = ferr.Error()
			return nil
		}
		items = append(items, found...)
		return nil
	})
	if err != nil {
		state.Err = err.Error()
	}
	state.Count = len(items)
	return items, state
}

func ledgerRows(root, path string, now time.Time) ([]pendency.Pendency, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	rel, _ := filepath.Rel(root, path)
	var header []string
	var items []pendency.Pendency

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		if header == nil {
			header = strings.Split(strings.TrimPrefix(line, "# "), "\t")
			continue
		}
		statusAt := columnIndex(header, "status")
		if statusAt < 0 {
			// No status column: this file is not a ledger of decisions.
			return nil, nil
		}
		cells := strings.Split(line, "\t")
		if statusAt >= len(cells) || terminal[strings.ToLower(strings.TrimSpace(cells[statusAt]))] {
			continue
		}
		key := naturalKey(header, cells)
		items = append(items, pendency.Pendency{
			ID:      pendency.NewID(pendency.Slug(rel), key),
			Class:   "A2",
			Source:  rel,
			Title:   describe(header, cells),
			Detail:  line,
			Blocks:  pendency.Founder,
			Origin:  pendency.Origin{Path: path, Locator: key, Open: "obsidian://open?path=" + path},
			Surface: surfaceFor(rel),
			SeenAt:  now,
		})
	}
	return items, sc.Err()
}

func columnIndex(header []string, name string) int {
	for i, h := range header {
		if strings.EqualFold(strings.TrimSpace(h), name) {
			return i
		}
	}
	return -1
}

// naturalKey prefers the row's own id, which is the most stable key a source
// can offer (spec FR2.2).
func naturalKey(header, cells []string) string {
	if i := columnIndex(header, "id"); i >= 0 && i < len(cells) {
		if v := strings.TrimSpace(cells[i]); v != "" {
			return v
		}
	}
	return pendency.Slug(strings.Join(cells, " "))
}

func describe(header, cells []string) string {
	for _, name := range []string{"titulo", "título", "decisao", "decisão", "title"} {
		if i := columnIndex(header, name); i >= 0 && i < len(cells) {
			if v := strings.TrimSpace(cells[i]); v != "" {
				return v
			}
		}
	}
	return strings.Join(cells, " · ")
}

// surfaceFor names the command that owns writing to a ledger, so the detail
// screen can send the reader there instead of pretending to decide.
func surfaceFor(rel string) string {
	switch {
	case strings.Contains(rel, "ideias"):
		return "/ideia <id> aderir · desistir · refinar"
	case strings.Contains(rel, "auto-aplicadas"):
		return "/auto-aplicadas"
	default:
		return "a superfície dona do ledger"
	}
}

// --- A3: the most recent session summary -----------------------------------

func latestSummary(root string, now time.Time) ([]pendency.Pendency, SourceState) {
	dir := filepath.Join(root, "sessions")
	state := SourceState{Name: "sessions/<data>/summary.md", Path: dir}

	entries, err := os.ReadDir(dir)
	if err != nil {
		state.Err = err.Error()
		return nil, state
	}
	var latest string
	for _, e := range entries {
		if e.IsDir() && e.Name() > latest {
			latest = e.Name()
		}
	}
	if latest == "" {
		return nil, state
	}
	path := filepath.Join(dir, latest, "summary.md")
	state.Path = path
	if _, err := os.Stat(path); err != nil {
		state.Err = err.Error()
		return nil, state
	}
	// The summary is read as one item: what carries across the round is prose,
	// and cutting it into false pieces would misread the source.
	state.Count = 1
	return []pendency.Pendency{{
		ID:      pendency.NewID("summary", latest),
		Class:   "A3",
		Source:  "sessions/" + latest,
		Title:   "Pendências que atravessam a rodada de " + latest,
		Blocks:  pendency.Hygiene,
		Origin:  pendency.Origin{Path: path, Locator: latest, Open: "obsidian://open?path=" + path},
		Surface: "leitura do summary",
		SeenAt:  now,
	}}, state
}

// --- A4: agenda files sitting in the root ----------------------------------

func agenda(root string, now time.Time) ([]pendency.Pendency, SourceState) {
	state := SourceState{Name: "pauta-*.md", Path: root}
	entries, err := os.ReadDir(root)
	if err != nil {
		state.Err = err.Error()
		return nil, state
	}
	var items []pendency.Pendency
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "pauta") || filepath.Ext(e.Name()) != ".md" {
			continue
		}
		path := filepath.Join(root, e.Name())
		items = append(items, pendency.Pendency{
			ID:      pendency.NewID("pauta", pendency.Slug(e.Name())),
			Class:   "A4",
			Source:  e.Name(),
			Title:   "Pauta em aberto: " + e.Name(),
			Blocks:  pendency.Founder,
			Origin:  pendency.Origin{Path: path, Open: "obsidian://open?path=" + path},
			Surface: "o arquivo de pauta",
			SeenAt:  now,
		})
	}
	state.Count = len(items)
	return items, state
}

// --- A5: the working tree --------------------------------------------------

func dirtyTree(root string, now time.Time) ([]pendency.Pendency, SourceState) {
	state := SourceState{Name: "git status", Path: root}
	cmd := exec.Command("git", "-C", root, "status", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		state.Err = err.Error()
		return nil, state
	}
	lines := strings.FieldsFunc(string(out), func(r rune) bool { return r == '\n' })
	if len(lines) == 0 {
		return nil, state
	}
	state.Count = 1
	return []pendency.Pendency{{
		ID:      pendency.NewID("git", "arvore-suja"),
		Class:   "A5",
		Source:  "git status",
		Title:   "Árvore com alterações não commitadas",
		Detail:  strings.Join(lines, "\n"),
		Blocks:  pendency.Hygiene,
		Origin:  pendency.Origin{Path: root, Locator: "git status"},
		Surface: "commit na sessão dona do lote",
		SeenAt:  now,
	}}, state
}

// --- A6: open markers in life.md -------------------------------------------

var marker = regexp.MustCompile(`\[(ABERTO|PROPOSTA)\]`)

func lifeMarkers(root string, now time.Time) ([]pendency.Pendency, SourceState) {
	path := filepath.Join(root, "life.md")
	state := SourceState{Name: "life.md", Path: path}

	f, err := os.Open(path)
	if err != nil {
		state.Err = err.Error()
		return nil, state
	}
	defer f.Close()

	var items []pendency.Pendency
	var heading string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for line := 0; sc.Scan(); line++ {
		text := sc.Text()
		if strings.HasPrefix(text, "#") {
			heading = strings.TrimSpace(strings.TrimLeft(text, "# "))
			continue
		}
		m, ok := openMarker(text)
		if !ok {
			continue
		}
		items = append(items, pendency.Pendency{
			ID:      pendency.NewID("life", pendency.LocationKey(heading, excerpt(text))),
			Class:   "A6",
			Source:  "life.md",
			Title:   m + " " + excerpt(strings.TrimPrefix(strings.TrimSpace(text), m)),
			Detail:  text,
			Blocks:  pendency.Founder,
			Origin:  pendency.Origin{Path: path, Locator: heading, Open: "obsidian://open?path=" + path},
			Surface: "emenda no life.md, pelo tribunal",
			SeenAt:  now,
		})
	}
	if err := sc.Err(); err != nil {
		state.Err = err.Error()
	}
	state.Count = len(items)
	return items, state
}

// openMarker decides whether a line actually RAISES an open question, rather
// than merely mentioning the marker.
//
// Measured against the real life.md: matching the marker anywhere turned 60+
// changelog rows and the legend that defines the markers into pendencies --
// noise that buries the dozen real ones. Two rules cut it: a table row is
// never an item (the changelog is history, not an open question), and the
// marker has to open the line, not appear inside a sentence about it.
func openMarker(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "|") {
		return "", false
	}
	head := strings.TrimLeft(trimmed, "-*>#0123456789. ")
	m := marker.FindStringIndex(head)
	if m == nil || m[0] != 0 {
		return "", false
	}
	return head[m[0]:m[1]], true
}

func excerpt(s string) string {
	s = strings.TrimSpace(strings.Trim(s, "-*> "))
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}
