// Package sonar reads the tribunal's fleet log and turns it into a feed.
//
// The log is `medicao/sonar.log` in the record repository: append-only, one
// event per line, written by the tribunal's plantao and by nobody else. This
// package is a STRICT READER -- it opens the file read-only and has no code
// path that creates, truncates, renames or writes it. That is the same class
// as FR15, where the tribunal's surfaces are write-only for lifely, read here
// in the opposite direction: what the tribunal writes, lifely only reads.
//
// The second rule is that nothing is dropped. The log's own format drifted
// over its first days -- timestamps with and without seconds, qualifiers
// written `portao ject:` and `portao(ject):` alike -- so a parser that
// discarded what it did not recognise would quietly hide the tribunal's own
// history. A line this package cannot read still travels, carrying its raw
// text and marked unparsed.
package sonar

import (
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

// LogRelPath is where the log lives inside the record repository. Relative on
// purpose: the panel is pointed at a root by `--root`, and a second absolute
// path here would be a second answer to "which tribunal are we reading".
const LogRelPath = "medicao/sonar.log"

// TailBytes caps how much of the log's end a single read looks at.
//
// The feed shows the recent past and polls every few seconds, so reading a
// file that only grows would make the cost of the newest event a function of
// the whole history. Reading the tail bounds it. The cut is repaired below:
// a partial first line is dropped rather than shown mangled.
const TailBytes = 512 * 1024

// StaleAfter is when the feed calls the log cold.
//
// The tribunal's charter puts the sonar on a ~10 minute cadence, so one
// missed beat is the smallest gap that means anything; twice the cadence is
// the threshold. An old stamp on the screen is itself the alarm -- the
// charter says so in those words -- and a feed that hides the age of its
// newest event would be the "barra que envelhece" the house already named.
const StaleAfter = 20 * time.Minute

// Theme is the palette token an event is painted with.
//
// The colours are not decoration: spec FR4.10 makes the semantics a
// requirement, and the same colour has to mean the same thing on every
// screen. These four map onto the tokens web/static/lifely.css already
// declares.
type Theme string

const (
	// ThemeGate is amber: a gate waiting. Gate transitions and the
	// no-mistakes notify hook.
	ThemeGate Theme = "gate"
	// ThemeFounder is sage: a decision or a dispatch -- the founder's own
	// colour, and the colour of an act that follows from his word.
	ThemeFounder Theme = "founder"
	// ThemeAgent is lavender: work an agent did or reported.
	ThemeAgent Theme = "agent"
	// ThemeBroken is red: a line this package could not read. It is a
	// finding, never silence.
	ThemeBroken Theme = "broken"
)

// Event is one line of the log.
//
// Raw is always the whole original line, parsed or not, because the feed's
// contract is that nothing is discarded: a consumer that cannot use the
// derived fields can always show what the tribunal actually wrote.
type Event struct {
	// At is the event's stamp. Zero when the line carried none.
	At time.Time
	// Kind is the first word after the stamp, lowercased and stripped of
	// punctuation: portao, notify, frota, sonar, decisao, despacho...
	Kind string
	// Topic is the qualifier the writer attached to the kind -- the `ject` of
	// `portao ject:` and of `portao(ject):`, the `frota` of `sonar(frota):`.
	// It is whatever was written there; this package does not decide that a
	// qualifier names a project, because on this log it often does not.
	Topic string
	// Theme is the palette token, derived from Kind.
	Theme Theme
	// Text is the message: the line with the stamp, the kind and the
	// qualifier already removed, because the screen shows those in their own
	// columns and repeating them made every row read "sonar(frota):
	// sonar(frota): ...". Raw is still there for anyone who wants the line
	// whole. On an unparsed line there was no stamp or kind to remove, so
	// Text is the trimmed line itself -- a consumer reading only Text still
	// gets what the tribunal wrote rather than an empty string.
	Text string
	// Raw is the line exactly as written.
	Raw string
	// Parsed is false when no stamp could be read. Such a line still travels
	// and still shows -- as Raw, marked.
	Parsed bool
}

// Feed is one read of the log.
//
// Err carries a source that could not be read. It is a field and not an
// error return at the HTTP edge for the reason spec FR3 states: a broken
// source becomes a marked entry, never an HTTP 500.
type Feed struct {
	// Events, newest first.
	Events []Event
	// Path is the file that was read, absolute, so a wrong --root is
	// visible on the screen instead of being guessed at.
	Path string
	// ReadAt is when this read happened.
	ReadAt time.Time
	// NewestAt is the stamp of the most recent event that carried one. Zero
	// when the feed is empty or nothing in it was stamped.
	NewestAt time.Time
	// Total is how many events the read produced before Limit cut it.
	Total int
	// Err is why the log could not be read, empty when it could.
	Err string
	// Missing distinguishes "no log here" from "the log is broken". An
	// absent file is the honest empty state of a machine where the tribunal
	// has not written yet; an unreadable one is a finding.
	Missing bool
}

// Stale reports whether the newest event is older than StaleAfter.
//
// A feed with no stamped event at all is not stale -- it is empty, and the
// two say different things to the reader.
func (f Feed) Stale() bool {
	if f.NewestAt.IsZero() {
		return false
	}
	return f.ReadAt.Sub(f.NewestAt) > StaleAfter
}

// Age is how old the newest event is, or zero when there is none.
func (f Feed) Age() time.Duration {
	if f.NewestAt.IsZero() {
		return 0
	}
	age := f.ReadAt.Sub(f.NewestAt)
	if age < 0 {
		// A stamp from the future is a clock disagreement, not a negative
		// age. Reporting "-3m ago" would read as a bug in the panel.
		return 0
	}
	return age
}

// stamp matches both shapes the log has used: with seconds and without. The
// log's own writer changed mid-history, and both halves are real history.
var stamp = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(?::\d{2})?)(?:\s+(.*))?$`)

// qualified matches a kind that carries its qualifier in parentheses:
// `portao(ject):`, `sonar(frota):`, `custodia(plantao):`.
var qualified = regexp.MustCompile(`^([A-Za-z_-]+)\(([^)]*)\)$`)

// Parse turns one line into an Event.
//
// It never fails. A line whose stamp cannot be read comes back with
// Parsed false, ThemeBroken and its Raw intact -- the constraint the ticket
// states in those words: a line that does not match the format shows raw and
// is never silently discarded.
func Parse(line string) Event {
	ev := Event{Raw: line}
	m := stamp.FindStringSubmatch(strings.TrimRight(line, "\r"))
	if m == nil {
		ev.Theme = ThemeBroken
		ev.Text = strings.TrimSpace(line)
		return ev
	}

	// Both layouts are tried because the log holds both. Local time, not UTC:
	// the writer stamps with the machine's clock, and reinterpreting it as
	// UTC would shift every event on the screen by the timezone offset.
	var err error
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04"} {
		ev.At, err = time.ParseInLocation(layout, m[1], time.Local)
		if err == nil {
			break
		}
	}
	if err != nil {
		// The regexp matched digits the parser rejects -- month 19, day 40.
		// Shaped like a stamp is not the same as being one.
		ev.Theme = ThemeBroken
		ev.Text = strings.TrimSpace(line)
		return ev
	}

	ev.Parsed = true
	ev.Kind, ev.Topic, ev.Text = split(strings.TrimSpace(m[2]))
	ev.Theme = themeOf(ev.Kind)
	return ev
}

// split reads the kind and its qualifier off the front of an event's text and
// returns what is left as the message.
//
// Three shapes appear in the log and all three are history: `portao ject:`,
// `portao(ject):` and `portao(lifely)` with no colon at all. They are read
// here in one place so that a fourth shape is one function to teach, not a
// search through the renderer. A word is consumed only when it was actually
// read as kind or qualifier -- anything this function declines to interpret
// stays in the message rather than disappearing off the front of it.
func split(text string) (kind, topic, rest string) {
	head, tail := cutField(text)
	if head == "" {
		return "", "", ""
	}

	trimmed := strings.TrimSuffix(head, ":")
	if m := qualified.FindStringSubmatch(trimmed); m != nil {
		if slug := slugOrEmpty(m[2]); slug != "" {
			return normalise(m[1]), slug, tail
		}
		// The name is real, the thing in brackets is not one: `custodia(2.5.31):`
		// is a law citation, `sonar(frota 2):` a note. Declining it used to
		// drop it from the message along with the kind, which is the same
		// silent loss the space-separated branch below was written to avoid --
		// caught by the gate on the second review. Only the name is consumed;
		// what was declined goes back to the front of the message.
		return normalise(m[1]), "", joinFields(head[len(m[1]):], tail)
	}

	// A first word that does not look like a name is not a kind. `-- nota do
	// plantao` has no kind, and consuming the `--` because it sat in the kind
	// position would edit the tribunal's line on the way to the screen.
	kind = slugOrEmpty(head)
	if kind == "" {
		return "", "", text
	}

	// `portao ject:` -- the qualifier is the next word, and it is only read
	// as one when it ends in a colon AND looks like a name. Reading any
	// second word as a qualifier would turn `frota runs=5` into topic
	// "runs=5", and `DESPACHO (2.5.31):` into a project called 2.5.31.
	second, after := cutField(tail)
	if strings.HasSuffix(second, ":") {
		if slug := slugOrEmpty(strings.TrimSuffix(second, ":")); slug != "" {
			return kind, slug, after
		}
	}
	return kind, "", tail
}

// cutField splits the first whitespace-delimited word off a string.
func cutField(s string) (field, rest string) {
	s = strings.TrimLeft(s, " \t")
	i := strings.IndexAny(s, " \t")
	if i < 0 {
		return s, ""
	}
	return s[:i], strings.TrimLeft(s[i+1:], " \t")
}

// joinFields puts a declined fragment back in front of the message.
func joinFields(head, tail string) string {
	head = strings.TrimSpace(head)
	switch {
	case head == "":
		return tail
	case tail == "":
		return head
	default:
		return head + " " + tail
	}
}

// slugLike is what a qualifier has to look like to be one: a word that starts
// with a letter.
var slugLike = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// slugOrEmpty returns the normalised token when it looks like a qualifier and
// nothing otherwise.
//
// The colon rule alone was not enough. `DESPACHO (2.5.31): ject-096` puts a
// parenthesised citation of a law where a qualifier would go, ends it in a
// colon, and the parser reported topic "2.5.31" -- a section number filed as
// if it named a project. The log's prose will keep doing this; a qualifier
// that does not look like a name is not a name.
func slugOrEmpty(s string) string {
	if n := normalise(s); slugLike.MatchString(n) {
		return n
	}
	return ""
}

// normalise lowercases a token and drops the punctuation the log's prose
// wraps around it, so `[DIRECAO]` and `DECISAO` index like their kinds.
func normalise(s string) string {
	return strings.ToLower(strings.Trim(s, "[](){}:.,;·-"))
}

// gateKinds and founderKinds are the two named halves of the palette; every
// other parsed kind is agent work.
//
// The lists are written out rather than inferred because the mapping is a
// meaning, not a pattern: FR4.10 makes colour semantics a requirement, and a
// heuristic would repaint an event the day the tribunal invents a word.
var gateKinds = map[string]bool{
	"portao": true,
	"notify": true,
	"gate":   true,
}

var founderKinds = map[string]bool{
	"decisao":  true,
	"decisoes": true,
	"despacho": true,
	"direcao":  true,
	"fundador": true,
	"ideia":    true,
	"merge":    true,
}

// themeOf maps a kind onto its palette token.
func themeOf(kind string) Theme {
	switch {
	case gateKinds[kind]:
		return ThemeGate
	case founderKinds[kind]:
		return ThemeFounder
	default:
		return ThemeAgent
	}
}

// Matcher decides whether an event belongs to one project.
//
// It is a type rather than a function because the caller runs it over the
// whole tail on every poll -- a thousand-odd events every three seconds --
// and compiling the same expression per event was measurable waste the gate
// caught on the first review. Built once, asked many times.
//
// The match is on the whole raw line, and that is a deliberate choice with a
// cost stated: the log declares a project structurally only on `portao` and
// `notify` lines, while the prose lines that carry the actual news
// ("DESPACHO ... frente A=lifely-018") name it in running text. Deriving the
// project from structure alone would make the filter hide exactly the events
// worth filtering for. The options offered on the screen are the other half
// of the bargain: they come from Projects, which reads only what the log
// DECLARES -- so a generous match never invents a project to click on.
type Matcher struct {
	// re is nil for the empty slug, which keeps everything. A nil regexp is
	// the whole of that case: no branch elsewhere, no sentinel expression.
	re *regexp.Regexp
}

// NewMatcher builds a matcher for one project slug. The empty slug matches
// every event.
//
// The expression pins word boundaries by hand instead of using \b: the slugs
// carry dashes (`no-mistakes`), and \b would fire in the middle of one.
func NewMatcher(slug string) Matcher {
	if slug == "" {
		return Matcher{}
	}
	return Matcher{re: regexp.MustCompile(
		`(?i)(^|[^0-9A-Za-z_])` + regexp.QuoteMeta(slug) + `([^0-9A-Za-z_]|$)`)}
}

// Matches reports whether the event names the matcher's project.
func (m Matcher) Matches(ev Event) bool {
	return m.re == nil || m.re.MatchString(ev.Raw)
}

// Filter narrows a feed to one project, keeping at most limit events.
//
// It lives here, next to Read, because it has to keep the feed's fields
// AGREEING with each other and that is one invariant, not two. The first
// version filtered Events and Total in the caller and left NewestAt at the
// whole-log value: asking for `?project=lifely` on a day when lifely had been
// quiet for twenty hours and ject had just moved rendered "newest 1min ago",
// in the healthy colour, describing an event the reader could not see. The
// age on the screen has to be the age OF THE FEED ON THE SCREEN.
//
// limit of zero or less keeps everything that matched.
func Filter(feed Feed, slug string, limit int) Feed {
	match := NewMatcher(slug)
	out := feed
	out.Events = make([]Event, 0, len(feed.Events))
	out.Total = 0
	out.NewestAt = time.Time{}

	for _, ev := range feed.Events {
		if !match.Matches(ev) {
			continue
		}
		out.Total++
		if out.NewestAt.IsZero() && ev.Parsed {
			// Events arrive newest first, so the first stamped match is the
			// newest one. An unparsed match carries no stamp and cannot
			// answer "how old is this feed" -- it is skipped for the age and
			// kept for the feed, which are different questions.
			out.NewestAt = ev.At
		}
		if limit > 0 && len(out.Events) >= limit {
			// Counted, not kept: Total stays the honest answer to "how much
			// matched", which is what tells the reader the feed is cut.
			continue
		}
		out.Events = append(out.Events, ev)
	}
	return out
}

// repoPath pulls the project out of the no-mistakes hook's environment dump,
// the one place a `notify` line names the repository it is about.
var repoPath = regexp.MustCompile(`NM_REPO_PATH=\S*/([0-9A-Za-z_.-]+)`)

// Projects lists the project slugs the events declare STRUCTURALLY, sorted.
//
// Structurally means: the qualifier of a `portao` line, and the last path
// element of a `notify` line's NM_REPO_PATH. Prose is not read here even
// though the filter reads it, and the asymmetry is the point -- the filter
// may be generous because a false positive only shows one extra line, while
// this list becomes the options on the screen, and an option the founder
// clicks has to be a project that exists rather than a word that looked like
// one.
func Projects(events []Event) []string {
	seen := map[string]bool{}
	for _, ev := range events {
		switch ev.Kind {
		case "portao":
			if ev.Topic != "" {
				seen[ev.Topic] = true
			}
		case "notify":
			if m := repoPath.FindStringSubmatch(ev.Raw); m != nil {
				seen[strings.ToLower(m[1])] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for slug := range seen {
		out = append(out, slug)
	}
	sort.Strings(out)
	return out
}

// Read returns the tail of the log at path, newest first.
//
// limit of zero or less means every event the tail held. A missing file is
// not an error: it is a machine where the tribunal has not written yet, and
// the empty feed says so.
func Read(path string, limit int, now time.Time) Feed {
	feed := Feed{Path: path, ReadAt: now, Events: []Event{}}

	// os.Open, and only ever os.Open: read-only by construction is this
	// package's whole contract, and there is a test that walks this file's
	// syntax tree to keep it true.
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			feed.Missing = true
			return feed
		}
		feed.Err = "the sonar log could not be read: " + err.Error()
		return feed
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		feed.Err = "the sonar log could not be read: " + err.Error()
		return feed
	}

	data, cut, err := tail(f, info.Size(), TailBytes)
	if err != nil {
		feed.Err = "the sonar log could not be read: " + err.Error()
		return feed
	}

	lines := strings.Split(string(data), "\n")
	if cut && len(lines) > 0 {
		// The tail started mid-line. Showing half an event as an unparsed
		// line would invent a finding out of our own truncation -- the one
		// case where dropping a line is honest, because the line is ours.
		lines = lines[1:]
	}

	// Newest first is the file's own order reversed. Sorting by stamp was
	// the alternative and it is worse: an unparsed line has no stamp, so
	// sorting would pile every unreadable line at one end, stripped of the
	// context that says when it happened.
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) == "" {
			continue
		}
		ev := Parse(lines[i])
		feed.Total++
		if feed.NewestAt.IsZero() && ev.Parsed {
			feed.NewestAt = ev.At
		}
		if limit > 0 && len(feed.Events) >= limit {
			continue
		}
		feed.Events = append(feed.Events, ev)
	}
	return feed
}

// tail reads the last max bytes of an open file, reporting whether it had to
// cut INTO A LINE to do it.
//
// The distinction is the whole function. The first version reported a cut
// whenever the file was bigger than the cap, and the caller drops the first
// line of a cut tail -- so a boundary that happened to land exactly on a line
// start threw away a complete, readable event. That is the silent loss this
// package exists to refuse, committed by the package itself, and the gate
// found it (run 01M0FD1G16). A byte is read from BEFORE the offset to answer
// the question: a newline there means the tail begins at a line start, and
// nothing has to be dropped.
//
// max is a parameter rather than the TailBytes constant so the boundary can
// be tested at a size a test can construct exactly.
func tail(r io.ReaderAt, size, max int64) (data []byte, cut bool, err error) {
	read := func(off int64) ([]byte, error) {
		buf := make([]byte, size-off)
		n, err := r.ReadAt(buf, off)
		if err != nil && err != io.EOF {
			return nil, err
		}
		return buf[:n], nil
	}

	if size <= max {
		data, err = read(0)
		return data, false, err
	}

	// One byte before the cut: it is the only thing that can tell a landing
	// mid-line from a landing on a line start.
	probe, err := read(size - max - 1)
	if err != nil {
		return nil, false, err
	}
	if len(probe) == 0 {
		return probe, false, nil
	}
	return probe[1:], probe[0] != '\n', nil
}
