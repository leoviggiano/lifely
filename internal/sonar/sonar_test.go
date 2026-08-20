package sonar

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The fixture is copied out of the real log, not invented, because the whole
// difficulty of this parser is the shapes the tribunal actually wrote: two
// stamp precisions, three ways of qualifying a kind, and prose lines that
// carry the news.
const fixture = "testdata/sample.log"

func TestParseReadsBothStampPrecisions(t *testing.T) {
	// 58 of the log's 1404 lines on 2026-08-20 carried no seconds. A parser
	// that took only one layout would have marked every one of them broken.
	withSeconds := Parse("2026-08-18T17:33:56 frota runs=5 socks=6")
	if !withSeconds.Parsed {
		t.Fatalf("stamp with seconds did not parse: %+v", withSeconds)
	}
	if got, want := withSeconds.At.Format("2006-01-02T15:04:05"), "2026-08-18T17:33:56"; got != want {
		t.Errorf("At = %s, want %s", got, want)
	}

	withoutSeconds := Parse("2026-08-18T18:00 sonar(frota): tudo calmo")
	if !withoutSeconds.Parsed {
		t.Fatalf("stamp without seconds did not parse: %+v", withoutSeconds)
	}
	if got, want := withoutSeconds.At.Format("2006-01-02T15:04:05"), "2026-08-18T18:00:00"; got != want {
		t.Errorf("At = %s, want %s", got, want)
	}
}

func TestParseReadsTheThreeQualifierShapes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		line  string
		kind  string
		topic string
		text  string
		theme Theme
	}{
		{"space and colon", "2026-08-18T17:30:52 portao ject:  cancelled x", "portao", "ject", "cancelled x", ThemeGate},
		{"parentheses and colon", "2026-08-19T15:47:10 portao(ject): run parked", "portao", "ject", "run parked", ThemeGate},
		{"parentheses, no colon", "2026-08-19T15:56:00 portao(lifely) 01M0 COMPLETED", "portao", "lifely", "01M0 COMPLETED", ThemeGate},
		{"no qualifier at all", "2026-08-18T17:33:56 frota runs=5 socks=6", "frota", "", "runs=5 socks=6", ThemeAgent},
		// The parenthesised law citation is NOT a qualifier, so it is not
		// consumed either: it stays where the tribunal wrote it.
		{"prose kind in caps", "2026-08-20T07:14:00 DESPACHO (2.5.31): ject-096", "despacho", "", "(2.5.31): ject-096", ThemeFounder},
		{"bracketed kind", "2026-08-19T10:00:00 [DIRECAO] fila normal", "direcao", "", "fila normal", ThemeFounder},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ev := Parse(tc.line)
			if !ev.Parsed {
				t.Fatalf("did not parse: %q", tc.line)
			}
			if ev.Kind != tc.kind {
				t.Errorf("Kind = %q, want %q", ev.Kind, tc.kind)
			}
			if ev.Topic != tc.topic {
				t.Errorf("Topic = %q, want %q", ev.Topic, tc.topic)
			}
			// Text is what the screen prints next to the kind. A kind left in
			// it renders as `sonar(frota): sonar(frota): ...`; a word wrongly
			// eaten out of it loses news the tribunal wrote.
			if ev.Text != tc.text {
				t.Errorf("Text = %q, want %q", ev.Text, tc.text)
			}
			if ev.Theme != tc.theme {
				t.Errorf("Theme = %q, want %q", ev.Theme, tc.theme)
			}
			// Whatever the split did, the line itself is untouched.
			if !strings.HasSuffix(ev.Raw, tc.text) {
				t.Errorf("Raw = %q lost its tail", ev.Raw)
			}
		})
	}
}

// The qualifier is only the SECOND word when that word ends in a colon.
// Without the guard, `frota runs=5 socks=6` reported topic "runs=5" -- a
// qualifier invented out of a value.
func TestParseDoesNotInventAQualifier(t *testing.T) {
	ev := Parse("2026-08-18T17:33:56 frota runs=5 socks=6")
	if ev.Topic != "" {
		t.Errorf("Topic = %q, want empty: `runs=5` is a value, not a qualifier", ev.Topic)
	}
}

// What split declines to interpret has to stay in the message. The gate's
// second review (run 01M0FC79HB) found the parenthesised branch dropping it:
// `custodia(2.5.31):` returned topic "" AND lost `(2.5.31)` from Text, which
// is the text the screen renders. Every case here is a line the screen would
// have shown with a piece quietly missing.
func TestParseNeverEatsWhatItDeclines(t *testing.T) {
	for _, tc := range []struct {
		name  string
		line  string
		kind  string
		topic string
		text  string
	}{
		{
			"parenthesised law citation, not a project",
			"2026-08-20T07:00:00 custodia(2.5.31): plantao devolveu a branch",
			"custodia", "", "(2.5.31): plantao devolveu a branch",
		},
		{
			"parenthesised note with a space in it",
			"2026-08-20T07:00:00 sonar(frota 2): segunda leva",
			"", "", "sonar(frota 2): segunda leva",
		},
		{
			"first word is punctuation, so there is no kind",
			"2026-08-20T07:00:00 -- nota solta do plantao",
			"", "", "-- nota solta do plantao",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ev := Parse(tc.line)
			if !ev.Parsed {
				t.Fatalf("did not parse: %q", tc.line)
			}
			if ev.Kind != tc.kind || ev.Topic != tc.topic {
				t.Errorf("Kind/Topic = %q/%q, want %q/%q", ev.Kind, ev.Topic, tc.kind, tc.topic)
			}
			if ev.Text != tc.text {
				t.Errorf("Text = %q, want %q", ev.Text, tc.text)
			}
			// The whole message, minus only what the columns show, is still
			// reachable: kind + topic + text accounts for the line.
			if !strings.HasSuffix(ev.Raw, ev.Text) {
				t.Errorf("Raw = %q does not end in Text = %q", ev.Raw, ev.Text)
			}
		})
	}
}

func TestParseKeepsAnUnstampedLineRaw(t *testing.T) {
	line := "uma linha que o plantao escreveu sem carimbo"
	ev := Parse(line)
	if ev.Parsed {
		t.Fatalf("a line with no stamp must not report as parsed: %+v", ev)
	}
	if ev.Raw != line {
		t.Errorf("Raw = %q, want the line verbatim", ev.Raw)
	}
	if ev.Theme != ThemeBroken {
		t.Errorf("Theme = %q, want %q", ev.Theme, ThemeBroken)
	}
	if !ev.At.IsZero() {
		t.Errorf("At = %v, want the zero time: there was no stamp to read", ev.At)
	}
}

// Shaped like a stamp is not the same as being one. The regexp accepts the
// digits; time.Parse is what rejects month 19.
func TestParseRejectsAStampShapedNonStamp(t *testing.T) {
	ev := Parse("2026-19-40T99:99 portao ject: nada disso existe")
	if ev.Parsed {
		t.Fatalf("an impossible date parsed: %+v", ev)
	}
	if ev.Theme != ThemeBroken {
		t.Errorf("Theme = %q, want %q", ev.Theme, ThemeBroken)
	}
}

func TestReadIsNewestFirstAndDropsNothing(t *testing.T) {
	feed := Read(fixture, 0, time.Now())
	if feed.Err != "" {
		t.Fatalf("Err = %q, want none", feed.Err)
	}

	raw, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	want := 0
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) != "" {
			want++
		}
	}
	// Every non-blank line of the file has to be on the feed. This is the
	// falsifier for "linha fora do formato aparece crua, nunca descartada":
	// make Read skip what it cannot parse and this count goes short.
	if len(feed.Events) != want {
		t.Fatalf("read %d events, want %d -- a line was dropped", len(feed.Events), want)
	}
	if feed.Total != want {
		t.Errorf("Total = %d, want %d", feed.Total, want)
	}

	// The unparsed line is present, in its place, still readable.
	found := false
	for _, ev := range feed.Events {
		if strings.Contains(ev.Raw, "sem carimbo") {
			found = true
			if ev.Parsed {
				t.Error("the unstamped line came back marked parsed")
			}
		}
	}
	if !found {
		t.Error("the unstamped line of the fixture is not on the feed")
	}

	// Newest first: the fixture's last line is the DESPACHO of 07:14 on
	// 2026-08-20.
	if !strings.Contains(feed.Events[0].Raw, "DESPACHO") {
		t.Errorf("first event is %q, want the newest line of the file", feed.Events[0].Raw)
	}
	if got, want := feed.NewestAt.Format("2006-01-02T15:04"), "2026-08-20T07:14"; got != want {
		t.Errorf("NewestAt = %s, want %s", got, want)
	}
}

func TestReadLimitCutsTheFeedButNotTheCount(t *testing.T) {
	feed := Read(fixture, 2, time.Now())
	if len(feed.Events) != 2 {
		t.Fatalf("len(Events) = %d, want 2", len(feed.Events))
	}
	if feed.Total <= 2 {
		t.Errorf("Total = %d, want the whole tail's count -- a limit hides events, it does not unmake them", feed.Total)
	}
	if feed.NewestAt.IsZero() {
		t.Error("NewestAt lost to the limit")
	}
}

func TestReadOnAMissingLogIsEmptyNotBroken(t *testing.T) {
	feed := Read(filepath.Join(t.TempDir(), "nope.log"), 0, time.Now())
	if !feed.Missing {
		t.Error("Missing = false, want true")
	}
	if feed.Err != "" {
		t.Errorf("Err = %q -- an absent log is an empty state, not a finding", feed.Err)
	}
	if feed.Events == nil {
		t.Error("Events is nil; the panel serialises it and a null is a shape only a bug produces")
	}
	if feed.Stale() {
		t.Error("an empty feed reported stale; empty and cold say different things")
	}
}

func TestReadOnAnUnreadableLogIsAFinding(t *testing.T) {
	// A directory where a file is expected: os.Open succeeds, the read does
	// not. That is "exists and cannot be read", which the house says is a
	// finding and never silence (spec FR1.3).
	dir := filepath.Join(t.TempDir(), "sonar.log")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	feed := Read(dir, 0, time.Now())
	if feed.Missing {
		t.Error("Missing = true for a path that exists")
	}
	if feed.Err == "" {
		t.Error("Err is empty; an unreadable source must travel marked")
	}
}

// The cut is only honest when it actually cut a line. The gate found the
// first version reporting a cut on size alone (run 01M0FD1G16), so a tail
// whose boundary landed exactly on a line start threw away a complete,
// readable event -- the package committing the silent loss it exists to
// refuse. The cap is a parameter precisely so this boundary is constructible.
func TestTailCutsOnlyWhenItLandsMidLine(t *testing.T) {
	// Six 10-byte lines: "aaaaaaaaa\n", "bbbbbbbbb\n", ... 60 bytes total.
	var b strings.Builder
	for _, c := range "abcdef" {
		b.WriteString(strings.Repeat(string(c), 9) + "\n")
	}
	data := []byte(b.String())
	r := strings.NewReader(string(data))
	size := int64(len(data))

	for _, tc := range []struct {
		name    string
		max     int64
		wantCut bool
		wantHas string
	}{
		// 30 bytes back from 60 is byte 30, and byte 29 is a newline: the
		// tail starts exactly at "dddddddd". Nothing was cut.
		{"boundary on a line start", 30, false, "ddddddddd"},
		// 25 back from 60 is byte 35, mid-way through the "d" line.
		{"boundary mid-line", 25, true, ""},
		// A cap bigger than the file cuts nothing at all.
		{"file smaller than the cap", 1000, false, "aaaaaaaaa"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, cut, err := tail(r, size, tc.max)
			if err != nil {
				t.Fatal(err)
			}
			if cut != tc.wantCut {
				t.Errorf("cut = %v, want %v (tail begins %q)", cut, tc.wantCut, first(got, 12))
			}
			if tc.wantHas != "" && !strings.HasPrefix(string(got), tc.wantHas) {
				t.Errorf("tail begins %q, want it to begin %q", first(got, 12), tc.wantHas)
			}
		})
	}
}

// The same boundary through Read, which is where the loss was visible: a
// complete event at the head of the tail must reach the feed.
func TestReadKeepsTheFirstLineWhenTheTailLandsCleanly(t *testing.T) {
	// Lines of exactly 128 bytes, so the 512 KiB boundary (a multiple of 128)
	// lands on a line start by construction.
	const width = 128
	if TailBytes%width != 0 {
		t.Fatalf("this test's arithmetic assumes TailBytes %% %d == 0", width)
	}
	line := func(tag string) string {
		body := "2026-08-20T07:00:00 frota " + tag + " "
		return body + strings.Repeat("x", width-1-len(body)) + "\n"
	}
	var b strings.Builder
	// One line more than the cap holds, so exactly one line falls outside.
	for i := 0; i < TailBytes/width+1; i++ {
		b.WriteString(line("n" + strconv.Itoa(i)))
	}

	path := filepath.Join(t.TempDir(), "aligned.log")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	feed := Read(path, 0, time.Now())
	if got, want := len(feed.Events), TailBytes/width; got != want {
		t.Fatalf("read %d events, want %d -- the tail dropped a whole, readable line", got, want)
	}
	// n0 is the one line legitimately outside the cap; n1 is the first one
	// inside it and must have survived.
	oldest := feed.Events[len(feed.Events)-1].Raw
	if !strings.Contains(oldest, " n1 ") {
		t.Errorf("oldest event is %q, want the line at the tail's first byte", first([]byte(oldest), 40))
	}
}

func first(b []byte, n int) string {
	if len(b) < n {
		n = len(b)
	}
	return string(b[:n])
}

func TestTailDropsOnlyItsOwnPartialLine(t *testing.T) {
	// The cut is ours, so dropping the half-line it produces is honest. The
	// test proves the SECOND line survives -- an off-by-one here would eat a
	// whole real event on every read of a big log.
	path := filepath.Join(t.TempDir(), "big.log")
	var b strings.Builder
	for b.Len() < TailBytes+4096 {
		b.WriteString("2026-08-20T07:00:00 frota runs=1 socks=1 " + strings.Repeat("x", 200) + "\n")
	}
	b.WriteString("2026-08-20T07:30:00 sonar(frota): a ultima linha\n")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	feed := Read(path, 0, time.Now())
	if !strings.Contains(feed.Events[0].Raw, "a ultima linha") {
		t.Errorf("newest event is %q, want the last line of the file", feed.Events[0].Raw)
	}
	for _, ev := range feed.Events {
		if !ev.Parsed {
			t.Fatalf("the tail cut left a mangled line on the feed: %q", ev.Raw)
		}
	}
}

func TestStaleAndAge(t *testing.T) {
	now := time.Date(2026, 8, 20, 8, 0, 0, 0, time.Local)
	feed := Feed{ReadAt: now, NewestAt: now.Add(-5 * time.Minute)}
	if feed.Stale() {
		t.Error("5 minutes reported stale; the sonar's cadence is ~10")
	}
	if feed.Age() != 5*time.Minute {
		t.Errorf("Age = %v, want 5m", feed.Age())
	}

	cold := Feed{ReadAt: now, NewestAt: now.Add(-StaleAfter - time.Second)}
	if !cold.Stale() {
		t.Error("an event older than StaleAfter did not report stale")
	}

	// A stamp from the future is a clock disagreement between the tribunal's
	// writer and this reader; "-3m ago" on the screen reads as a panel bug.
	future := Feed{ReadAt: now, NewestAt: now.Add(3 * time.Minute)}
	if future.Age() != 0 {
		t.Errorf("Age = %v, want 0 for a stamp in the future", future.Age())
	}
}

func TestMatcherMatchesOnWordBoundaries(t *testing.T) {
	feed := Read(fixture, 0, time.Now())
	var despacho, notify Event
	for _, ev := range feed.Events {
		if strings.Contains(ev.Raw, "DESPACHO") {
			despacho = ev
		}
		if ev.Kind == "notify" {
			notify = ev
		}
	}

	// The prose line names both projects in running text. Deriving the
	// project from structure alone would hide exactly this event.
	if !NewMatcher("lifely").Matches(despacho) {
		t.Error("the DESPACHO line names lifely and the filter missed it")
	}
	if !NewMatcher("ject").Matches(despacho) {
		t.Error("the DESPACHO line names ject and the filter missed it")
	}
	// `ject` inside `projects` is not a mention of the project.
	if NewMatcher("ject").Matches(Event{Raw: "2026-08-20T07:00:00 frota /Users/x/projects"}) {
		t.Error("`projects` matched the slug `ject`; the boundary guard is gone")
	}
	// A slug with a dash is why the boundaries are written by hand: \b fires
	// inside `no-mistakes` and would match on the `mistakes` half alone.
	dashed := Event{Raw: "2026-08-20T07:00:00 frota repo=no-mistakes"}
	if !NewMatcher("no-mistakes").Matches(dashed) {
		t.Error("a slug with a dash did not match its own line")
	}
	if !NewMatcher("lifely").Matches(notify) {
		t.Error("NM_REPO_PATH=.../projects/lifely is a mention of lifely")
	}
	if !NewMatcher("").Matches(despacho) {
		t.Error("an empty filter must keep everything")
	}
}

// The bug this pins was found by the gate on the first review (run
// 01M0FB7BT4, finding `newest-at-not-filtered`): the filter narrowed Events
// and Total and left NewestAt describing the whole log, so a feed filtered to
// a quiet project reported the age of a busy one -- in the healthy colour,
// about an event the reader could not see.
func TestFilterDatesTheFeedItActuallyReturns(t *testing.T) {
	now := time.Date(2026, 8, 20, 8, 0, 0, 0, time.Local)
	feed := Feed{
		ReadAt: now,
		Events: []Event{
			// Newest first, as Read returns them.
			{At: now.Add(-1 * time.Minute), Raw: "... portao ject: running", Parsed: true},
			{At: now.Add(-20 * time.Hour), Raw: "... portao lifely: completed", Parsed: true},
		},
		Total:    2,
		NewestAt: now.Add(-1 * time.Minute),
	}

	got := Filter(feed, "lifely", 0)
	if len(got.Events) != 1 || got.Total != 1 {
		t.Fatalf("len(Events)=%d Total=%d, want 1 and 1", len(got.Events), got.Total)
	}
	if want := now.Add(-20 * time.Hour); !got.NewestAt.Equal(want) {
		t.Errorf("NewestAt = %v, want %v -- the age must be the age of the feed on the screen", got.NewestAt, want)
	}
	if !got.Stale() {
		t.Error("a feed whose newest event is 20h old reported healthy")
	}

	// And the unfiltered read is unchanged by the filtering.
	if !feed.NewestAt.Equal(now.Add(-1 * time.Minute)) {
		t.Error("Filter mutated the feed it was given")
	}
}

func TestFilterWithNoStampedMatchHasNoAge(t *testing.T) {
	now := time.Date(2026, 8, 20, 8, 0, 0, 0, time.Local)
	feed := Feed{
		ReadAt:   now,
		Events:   []Event{{Raw: "linha sem carimbo mencionando lifely"}},
		Total:    1,
		NewestAt: now,
	}
	got := Filter(feed, "lifely", 0)
	if len(got.Events) != 1 {
		t.Fatalf("len(Events) = %d, want 1: the unstamped line still travels", len(got.Events))
	}
	if !got.NewestAt.IsZero() {
		t.Errorf("NewestAt = %v, want zero: no matched event carries a stamp", got.NewestAt)
	}
	if got.Stale() {
		t.Error("a feed with no stamped event reported stale; empty and cold say different things")
	}
}

func TestFilterCountsWhatTheLimitHides(t *testing.T) {
	now := time.Date(2026, 8, 20, 8, 0, 0, 0, time.Local)
	feed := Feed{ReadAt: now}
	for i := 0; i < 5; i++ {
		feed.Events = append(feed.Events, Event{
			At: now.Add(-time.Duration(i) * time.Minute), Parsed: true,
			Raw: "... portao lifely: run " + strconv.Itoa(i),
		})
	}
	feed.Total = 5

	got := Filter(feed, "lifely", 2)
	if len(got.Events) != 2 {
		t.Fatalf("len(Events) = %d, want 2", len(got.Events))
	}
	if got.Total != 5 {
		t.Errorf("Total = %d, want 5: a limit hides events, it does not unmake them", got.Total)
	}
	if !got.NewestAt.Equal(now) {
		t.Errorf("NewestAt = %v, want the newest match", got.NewestAt)
	}
}
