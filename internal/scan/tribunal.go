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
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
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
		case strings.HasPrefix(strings.TrimLeft(line, " "), "- [ ] "):
			// Indented sub-tasks count: an item nested under another is still
			// open work, and dropping it hides the very detail that a board
			// uses indentation to express.
			flush()
			title := strings.TrimPrefix(strings.TrimLeft(line, " "), "- [ ] ")
			p := pendency.Pendency{
				// Identity = lane + WHOLE title.
				//
				// The lane belongs here: the same sentence under Faixa 1 and
				// Faixa 3 is two different items -- one waits on the founder,
				// the other on an agent -- and lanes are stable, items almost
				// never move between them.
				//
				// Position does NOT belong: a counter renumbers everything
				// below an insertion and orphans those conversations. And the
				// title goes in whole, because truncating for display and then
				// keying on the truncation collapsed two long items into one.
				ID:      pendency.NewID("founder", "f"+faixa+"-"+pendency.Slug(boardKey(title))),
				Class:   "A1",
				Source:  "FOUNDER.md",
				Title:   strings.TrimSpace(title),
				Detail:  title,
				Blocks:  faixaBlocker(faixa),
				Origin:  pendency.Origin{Path: path, Locator: "Faixa " + faixa, Open: obsidianURI(path)},
				Surface: "veredito no FOUNDER.md",
				SeenAt:  now,
			}
			current = &p
		case strings.HasPrefix(strings.TrimLeft(line, " "), "- [x] "):
			flush()
		case strings.HasPrefix(line, "## "):
			// A section that is not a Faixa ends the lane too: without this,
			// items under an unrelated heading inherit the previous lane's
			// blocker and land in the wrong group.
			flush()
			faixa = ""
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

// cleanTitle strips the markdown noise without shortening: identity needs the
// whole thing, so that two items are only ever the same item.
// boardKey returns the part of a board line that names the item.
//
// The board's convention is `**Título** — nota que envelhece`. Keying on the
// whole line made every note edit mint a new id; keying on a truncation made
// two long items collapse. The bold title is the source's own answer to "what
// is this item called", so it is the key -- and when there is no bold title,
// the whole line is, because then nothing shorter is safe.
func boardKey(line string) string {
	// Only the bold that OPENS the item counts. Taking any bold run in the
	// line would key `- [ ] fazer X — **urgente** hoje` on "urgente", and two
	// unrelated items marked urgent would become one.
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "**") {
		if b := strings.Index(trimmed[2:], "**"); b > 0 {
			return strings.TrimSpace(trimmed[2 : 2+b])
		}
	}
	return cleanTitle(line)
}

func cleanTitle(s string) string {
	return strings.TrimSpace(strings.ReplaceAll(s, "*", ""))
}

// --- A2: every ledger with a status column ---------------------------------

// terminal lists the states that mean "done deciding". Anything else counts as
// open: showing one row too many is a smaller failure than hiding a decision
// that is still waiting.
var terminal = map[string]bool{
	"aprovada": true, "aprovado": true, "aplicada": true, "aplicado": true,
	"decidida": true, "decidido": true, "concluida": true, "concluido": true,
	// Only past-participle states belong here. "decisão" was added by reflex
	// and removed on review: it is the noun for the artifact, not a state, and
	// a row whose status column literally reads "decisão" is open, not closed.
	"concluída": true, "concluído": true,
	"cancelada": true, "cancelado": true, "desistida": true, "desistiu": true,
	"rejeitada": true, "rejeitado": true, "arquivada": true, "arquivado": true,
}

func ledgers(root string, now time.Time) ([]pendency.Pendency, SourceState) {
	state := SourceState{Name: "ledgers *.tsv", Path: root}
	var items []pendency.Pendency

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// A directory we cannot walk is a finding, not a shrug: this is
			// the only sweep that discovers files, so nothing else would ever
			// report it (NFR6).
			rel, _ := filepath.Rel(root, path)
			if state.Err != "" {
				state.Err += "; "
			}
			state.Err += rel + ": " + err.Error()
			return nil
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
		// Keep what was read before the failure: a ledger that breaks halfway
		// still told us about the rows above the break, and dropping them
		// hides open decisions because of an unrelated I/O error.
		items = append(items, found...)
		if ferr != nil {
			// Name the file: a bare error message from a walk over many
			// ledgers says nothing about which one broke, and the last one
			// silently overwrites the others.
			rel, _ := filepath.Rel(root, path)
			if state.Err != "" {
				state.Err += "; "
			}
			state.Err += rel + ": " + ferr.Error()
		}
		return nil
	})
	if err != nil {
		// WalkDir only returns what the callback returns, and the callback
		// never returns an error -- it records and continues. Keeping a
		// branch here would clobber the per-file messages accumulated above
		// with a single generic one.
		if state.Err != "" {
			state.Err += "; "
		}
		state.Err += err.Error()
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
	statusAt := -1
	var items []pendency.Pendency
	var rows [][]string

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		if header == nil {
			header = strings.Split(strings.TrimPrefix(line, "# "), "\t")
			statusAt = columnIndex(header, "status")
			if statusAt < 0 {
				// No status column: this file is not a ledger of decisions.
				return nil, nil
			}
			continue
		}
		cells := strings.Split(line, "\t")
		// A row whose status is empty or missing is NOT decided. Treating it
		// as terminal would hide a decision that is still waiting -- the exact
		// failure this scanner's terminal-by-exclusion rule exists to avoid.
		if statusAt < len(cells) && terminal[strings.ToLower(strings.TrimSpace(cells[statusAt]))] {
			continue
		}
		key := naturalKey(header, cells)
		rows = append(rows, cells)
		items = append(items, pendency.Pendency{
			ID:      pendency.NewID(pendency.Slug(rel), key),
			Class:   "A2",
			Source:  rel,
			Title:   describe(header, cells),
			Detail:  line,
			Blocks:  pendency.Founder,
			Origin:  pendency.Origin{Path: path, Locator: key, Open: obsidianURI(path)},
			Surface: surfaceFor(rel),
			SeenAt:  now,
		})
	}
	// Keep what was read before the failure. Returning nil here undid the
	// preservation this function does two screens above -- a scanner error on
	// line 900 would hide the 899 open decisions above it.
	err = sc.Err()
	return disambiguate(items, header, rows), err
}

// disambiguate adds a tiebreaker ONLY to rows whose key collides.
//
// A key is meant to survive edits, and every candidate tiebreaker in a ledger
// without an id column is content that can change. So the cost is paid only
// where it buys something: a unique row keeps the stable key it had, and rows
// that would otherwise collapse into one pendency get told apart -- because
// losing a row from the panel is the worse failure.
func disambiguate(items []pendency.Pendency, header []string, rows [][]string) []pendency.Pendency {
	groups := map[string][]int{}
	for i, it := range items {
		groups[it.ID] = append(groups[it.ID], i)
	}
	for _, idx := range groups {
		if len(idx) < 2 {
			continue
		}
		col := distinguishingColumn(header, rows, idx)
		if col < 0 {
			// Nothing in the source tells these rows apart. A human reading
			// the ledger could not either, so inventing a counter here would
			// hide a duplicate in the source behind a number of ours.
			continue
		}
		for _, i := range idx {
			tie := pendency.Slug(strings.TrimSpace(rows[i][col]))
			items[i].ID += "-" + tie
			items[i].Origin.Locator += "-" + tie
		}
	}
	return items
}

// distinguishingColumn finds the first column whose value actually DIFFERS
// across the colliding rows.
//
// Picking "the first eligible cell" of one row was not enough: if that column
// holds the same value on both, the id grows and the collision survives. The
// question is about the group, so it has to be asked of the group.
func distinguishingColumn(header []string, rows [][]string, idx []int) int {
	statusAt := columnIndex(header, "status")
	for col := range header {
		if col == statusAt {
			continue
		}
		seen := map[string]bool{}
		ok := true
		for _, i := range idx {
			if col >= len(rows[i]) {
				ok = false
				break
			}
			raw := strings.TrimSpace(rows[i][col])
			// Validate the value that will actually be USED: disambiguate
			// keys on the slug, so two raw values that slug alike -- or slug
			// to nothing -- would pass a raw check and still collide.
			v := pendency.Slug(raw)
			// A date distinguishes today and moves the identity tomorrow.
			if v == "" || looksLikeDate(raw) || seen[v] {
				ok = false
				break
			}
			seen[v] = true
		}
		if ok {
			return col
		}
	}
	return -1
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
	// Fall back to what NAMES the row, never to the whole row: folding status,
	// dates and notes into the identity means editing a note mints a new id
	// and orphans the conversation attached to it (spec FR2.2).
	//
	// describe() alone is not enough -- its own last resort is the joined row,
	// which is exactly what we are avoiding. The chain ends at a naming
	// column, never at "everything".
	if title := namingCell(header, cells); title != "" {
		return pendency.Slug(title)
	}
	// No naming column: key on the row's FIRST column, whatever it holds. It
	// is the closest thing a ledger without a title has to a key, and unlike
	// "the first non-empty cell" it does not wander to a note or a date when
	// the first column happens to be blank.
	first := ""
	if len(cells) > 0 {
		first = strings.TrimSpace(cells[0])
	}
	// The header is the schema, not the row's identity: folding it in meant
	// adding a column renumbered every row in the file.
	return pendency.LocationKey("ledger-row", first)
}

// looksLikeDate spots a timestamp cell, which must never carry identity: it
// changes every time the row is touched.
func looksLikeDate(v string) bool {
	if len(v) < 8 {
		return false
	}
	digits := 0
	for _, r := range v {
		if r >= '0' && r <= '9' {
			digits++
		}
	}
	return digits >= 6
}

// namingCell returns the value of the column the header declares as the row's
// name. It is the single place that lookup lives: describe() calls it too.
func namingCell(header, cells []string) string {
	for _, name := range []string{"titulo", "título", "decisao", "decisão", "title", "assunto"} {
		if i := columnIndex(header, name); i >= 0 && i < len(cells) {
			if v := strings.TrimSpace(cells[i]); v != "" {
				return v
			}
		}
	}
	// No naming column: stop here rather than walking forward. "Whatever came
	// next" is a volatile column by another name -- notes, dates and counters
	// all live to the right, and any of them would tie the identity to a value
	// that changes when the row is merely touched.
	return ""
}

func describe(header, cells []string) string {
	if v := namingCell(header, cells); v != "" {
		return v
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
		// A repository with no sessions/ yet is normal absence, the same call
		// this function already makes for a round without a summary. Two
		// answers to the same kind of fact, in one function, was the
		// incoherence the panel kept showing.
		if os.IsNotExist(err) {
			return nil, state
		}
		state.Err = err.Error()
		return nil, state
	}
	// Only date-shaped directories count: the pick is lexicographic, so a
	// stray folder ("tmp", "zzz") would otherwise win and the panel would read
	// the wrong round, or none at all.
	var latest string
	for _, e := range entries {
		if e.IsDir() && sessionDate.MatchString(e.Name()) && e.Name() > latest {
			latest = e.Name()
		}
	}
	if latest == "" {
		return nil, state
	}
	path := filepath.Join(dir, latest, "summary.md")
	state.Path = path
	if _, err := os.Stat(path); err != nil {
		// A round in progress has no summary yet. That is normal absence, not
		// an unreadable source: reporting it as ILEGIVEL trains the reader to
		// ignore the one marker that should always mean something.
		if errors.Is(err, os.ErrNotExist) {
			return nil, state
		}
		state.Err = err.Error()
		return nil, state
	}
	// Only a summary that actually carries something forward is a pendency.
	// Emitting one unconditionally would mean the panel could never reach
	// zero -- and "nada pendente" is a result the panel must be able to show.
	body, err := os.ReadFile(path)
	if err != nil {
		state.Err = err.Error()
		return nil, state
	}
	if !carriesForward(string(body)) {
		return nil, state
	}
	state.Count = 1
	return []pendency.Pendency{{
		ID:      pendency.NewID("summary", latest),
		Class:   "A3",
		Source:  "sessions/" + latest,
		Title:   "Pendências que atravessam a rodada de " + latest,
		Blocks:  pendency.Hygiene,
		Origin:  pendency.Origin{Path: path, Locator: latest, Open: obsidianURI(path)},
		Surface: "leitura do summary",
		SeenAt:  now,
	}}, state
}

// obsidianURI builds the link that opens a file in Obsidian.
//
// The path has to be percent-encoded: accented filenames are the norm in this
// record repository, and a raw path with a space or an accent produces a URI
// the app silently refuses to open.
func obsidianURI(path string) string {
	// This is a QUERY parameter value, and neither stock escaper fits alone:
	// PathEscape leaves '&', '=' and '+' raw (they are legal in a path), which
	// truncates or splits the value; QueryEscape handles them but writes a
	// space as '+', which Obsidian percent-decodes into a literal plus.
	// So: escape as a query value, then repair the one character it gets wrong.
	return "obsidian://open?path=" + strings.ReplaceAll(url.QueryEscape(path), "+", "%20")
}

// carriesForward reports whether a session summary leaves something open.
//
// The marker is the summary's own vocabulary: a section naming what is
// pending, blocked or carried over. Absent that, the round closed clean.
func carriesForward(body string) bool {
	lower := strings.ToLower(body)
	for _, marker := range []string{
		"## pendente", "## pendências", "## pendencias",
		"## em aberto", "## atravessa", "## bloque",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
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
			Origin:  pendency.Origin{Path: path, Open: obsidianURI(path)},
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
		// A root that is not a repository has no tree to be dirty: that is
		// normal absence, the same call the summary makes, and reporting it as
		// ILEGIVEL trains the reader to ignore the marker that should always
		// mean something.
		// Ask the filesystem, not git's prose: git ships translated messages,
		// so matching "not a git repository" breaks under any other locale.
		if _, statErr := os.Stat(filepath.Join(root, ".git")); os.IsNotExist(statErr) {
			return nil, state
		}
		var ee *exec.ExitError
		stderr := ""
		if errors.As(err, &ee) {
			stderr = strings.TrimSpace(string(ee.Stderr))
		}
		// Keep git's own words: "exit status 128" alone says nothing about
		// whether the root is unreadable, or something else entirely.
		state.Err = err.Error()
		if stderr != "" {
			state.Err += ": " + stderr
		}
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

// sessionDate matches the YYYY-MM-DD directories under sessions/.
// Anchored at BOTH ends: unanchored, "2026-08-18-rascunho" also matches and
// wins the lexicographic pick over the real round.
var sessionDate = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

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
	line := 0
	for sc.Scan() {
		line++
		text := sc.Text()
		m, rest, ok := openMarker(text)
		if strings.HasPrefix(text, "#") {
			heading = strings.TrimSpace(strings.TrimLeft(text, "# "))
			// A heading that itself RAISES a question is both: it sets the
			// context and it is an item. Skipping it because it starts with
			// '#' dropped exactly the markers that someone bothered to
			// promote to a heading.
			if !ok {
				continue
			}
		}
		if !ok {
			continue
		}
		items = append(items, pendency.Pendency{
			// The WHOLE line, not the displayed excerpt: excerpt cuts at 120
			// runes for the panel, and two markers that differ only past that
			// point would share an id and one would vanish. Same defect the
			// A1 identity had; swept here in the same pass.
			ID:      pendency.NewID("life", pendency.LocationKey(heading, strings.TrimSpace(text))),
			Class:   "A6",
			Source:  "life.md",
			Title:   strings.TrimSpace(m + " " + excerpt(rest)),
			Detail:  text,
			Blocks:  pendency.Founder,
			Origin:  pendency.Origin{Path: path, Locator: heading + ":" + strconv.Itoa(line), Open: obsidianURI(path)},
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
// It returns the marker and the rest of the line with the marker removed --
// the caller must use that rest, not the raw line: stripping the marker from
// the raw text fails whenever the line opens with a bullet or a quote, and the
// marker ends up printed twice.
func openMarker(line string) (marker_, rest string, ok bool) {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "|") {
		return "", "", false
	}
	head := strings.TrimLeft(trimmed, "-*>#0123456789. ")
	m := marker.FindStringIndex(head)
	if m == nil || m[0] != 0 {
		return "", "", false
	}
	return head[m[0]:m[1]], strings.TrimSpace(head[m[1]:]), true
}

func excerpt(s string) string {
	return cut(strings.TrimSpace(strings.Trim(s, "-*> ")), 120)
}

// cut truncates by runes, never by bytes.
//
// Titles here are Portuguese and accented; slicing a byte count lands inside a
// multi-byte rune and renders as a replacement character. Measured in the real
// sweep output before the reviewer named it.
func cut(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
