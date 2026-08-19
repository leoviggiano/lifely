package md

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func read(t *testing.T, body string) *Doc {
	t.Helper()
	path := filepath.Join(t.TempDir(), "doc.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return Read(path)
}

// The fence guard, direction one: a heading or an item inside a fence is
// content, never structure -- and it still REACHES the consumer, raw intact,
// because the first hand copy of this guard swallowed the snippet it existed
// to protect.
func TestFenceTurnsStructureIntoContent(t *testing.T) {
	doc := read(t, "# fora\n```sh\n# dentro\n- [ ] dentro\n```\n# depois\n")
	if doc.Err != "" {
		t.Fatalf("a balanced fence produced an error: %q", doc.Err)
	}
	want := []Kind{Heading, Fenced, Fenced, Fenced, Fenced, Heading}
	if len(doc.Lines) != len(want) {
		t.Fatalf("got %d lines, want %d", len(doc.Lines), len(want))
	}
	for i, k := range want {
		if doc.Lines[i].Kind != k {
			t.Errorf("line %d (%q) is kind %v, want %v", i+1, doc.Lines[i].Raw, doc.Lines[i].Kind, k)
		}
	}
	if doc.Lines[2].Raw != "# dentro" {
		t.Errorf("fenced content did not travel verbatim: %q", doc.Lines[2].Raw)
	}
}

// The fence guard, direction two: a fence that never closes is a FINDING,
// with the line where it opened -- and the lines after it are still
// delivered, as content, so nothing vanishes silently.
func TestUnclosedFenceIsReported(t *testing.T) {
	doc := read(t, "texto\n```sh\n# nunca fecha\n- [ ] perdido\n")
	if !strings.Contains(doc.Err, "never closed") {
		t.Fatalf("an unclosed fence was not reported: %q", doc.Err)
	}
	if !strings.Contains(doc.Err, "line 2") {
		t.Errorf("the finding does not say where the fence opened: %q", doc.Err)
	}
	if len(doc.Lines) != 4 {
		t.Fatalf("got %d lines, want 4 -- the tail after the fence was dropped", len(doc.Lines))
	}
	for _, ln := range doc.Lines[1:] {
		if ln.Kind != Fenced {
			t.Errorf("line %d (%q) is kind %v, want Fenced", ln.Num, ln.Raw, ln.Kind)
		}
	}
}

// The error order is fixed here, once: the read failure first, the fence
// after, joined with "; ". bufio.ErrTooLong truncates the file and an
// unclosed fence is its symptom -- the symptom first sends the reader
// hunting a backtick that exists.
func TestReadFailureComesBeforeTheFence(t *testing.T) {
	huge := strings.Repeat("x", maxLine+1)
	doc := read(t, "```sh\n"+huge+"\n")
	if doc.Err == "" {
		t.Fatal("a line above the buffer inside an open fence produced no error")
	}
	readAt := strings.Index(doc.Err, "too long")
	fenceAt := strings.Index(doc.Err, "never closed")
	if readAt < 0 || fenceAt < 0 {
		t.Fatalf("the error must carry both the read failure and the fence: %q", doc.Err)
	}
	if readAt > fenceAt {
		t.Errorf("the fence is announced before the read failure that caused it: %q", doc.Err)
	}
	if !strings.Contains(doc.Err, "; ") {
		t.Errorf("the two failures are not joined with %q: %q", "; ", doc.Err)
	}
}

// The heading guard, both directions: the whitespace after the hashes is
// load-bearing. '#tag' is a tag, and one scanner reading it as a heading let
// a hashtag contaminate the locator of everything below it.
func TestHeadingRequiresWhitespaceAfterTheHashes(t *testing.T) {
	for _, c := range []struct {
		raw   string
		kind  Kind
		level int
		title string
	}{
		{"# Tag", Heading, 1, "Tag"},
		{"## Faixa 1", Heading, 2, "Faixa 1"},
		{"##\tFaixa 1", Heading, 2, "Faixa 1"},
		{"###### fundo", Heading, 6, "fundo"},
		{"#tag", Text, 0, ""},
		{"####### sete", Text, 0, ""},
		{"  ## recuado", Text, 0, ""},
		{"##", Text, 0, ""},
	} {
		got := classify(1, c.raw)
		if got.Kind != c.kind || got.Level != c.level || got.Title != c.title {
			t.Errorf("classify(%q) = {%v %d %q}, want {%v %d %q}",
				c.raw, got.Kind, got.Level, got.Title, c.kind, c.level, c.title)
		}
	}
}

// The item guard, direction one: every CommonMark list marker opens an item,
// because the cost of a rigid parser is an item invisible on the founder's
// board and the cost of a loose one is zero.
func TestItemAcceptsEveryCommonMarkMarker(t *testing.T) {
	for _, raw := range []string{"- [ ] tarefa", "* [ ] tarefa", "+ [ ] tarefa"} {
		got := classify(1, raw)
		if got.Kind != Item {
			t.Errorf("classify(%q) = %v, want Item", raw, got.Kind)
			continue
		}
		if got.Checked {
			t.Errorf("classify(%q) came back checked", raw)
		}
		if got.Text != "tarefa" {
			t.Errorf("classify(%q).Text = %q: the marker leaked into the text", raw, got.Text)
		}
	}
}

// The item guard, direction two: what is NOT an item stays text, so the
// widening above cannot swallow prose that merely resembles a checkbox.
func TestNonItemsStayText(t *testing.T) {
	for _, raw := range []string{
		"-[ ] sem espaco",
		"- [] caixa vazia",
		"x [ ] marcador estranho",
		"1. [ ] lista numerada",
		"- [y] estado desconhecido",
	} {
		if got := classify(1, raw); got.Kind != Text {
			t.Errorf("classify(%q) = %v, want Text", raw, got.Kind)
		}
	}
}

// A checked item is closed work; the indent travels, because the board reads
// nesting from it.
func TestItemCarriesCheckedAndIndent(t *testing.T) {
	for _, c := range []struct {
		raw     string
		checked bool
		indent  string
		text    string
	}{
		{"- [ ] aberta", false, "", "aberta"},
		{"- [x] feita", true, "", "feita"},
		{"- [X] feita", true, "", "feita"},
		{"  - [ ] filha", false, "  ", "filha"},
		{"\t- [ ] filha", false, "\t", "filha"},
	} {
		got := classify(1, c.raw)
		if got.Kind != Item {
			t.Fatalf("classify(%q) = %v, want Item", c.raw, got.Kind)
		}
		if got.Checked != c.checked || got.Indent != c.indent || got.Text != c.text {
			t.Errorf("classify(%q) = {checked %v indent %q text %q}, want {%v %q %q}",
				c.raw, got.Checked, got.Indent, got.Text, c.checked, c.indent, c.text)
		}
	}
}

// Absence and failure are different states, and Read must never collapse
// them: a missing file is silence, an unreadable one is a finding.
func TestMissingIsNotAnError(t *testing.T) {
	doc := Read(filepath.Join(t.TempDir(), "nao-existe.md"))
	if !doc.Missing {
		t.Error("a file that does not exist did not come back Missing")
	}
	if doc.Err != "" {
		t.Errorf("a file that does not exist came back with an error: %q", doc.Err)
	}
}

func TestUnreadableIsAnErrorNotMissing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not block reads")
	}
	path := filepath.Join(t.TempDir(), "doc.md")
	if err := os.WriteFile(path, []byte("conteudo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Skipf("cannot make a file unreadable here: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

	doc := Read(path)
	if doc.Missing {
		t.Error("an unreadable file was reported as absent")
	}
	if doc.Err == "" {
		t.Error("an unreadable file produced no finding")
	}
}

// Line numbers are 1-based, the way editors count, because they end up in
// locators a human follows.
func TestLineNumbersStartAtOne(t *testing.T) {
	doc := read(t, "primeira\nsegunda\n")
	if len(doc.Lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(doc.Lines))
	}
	if doc.Lines[0].Num != 1 || doc.Lines[1].Num != 2 {
		t.Errorf("line numbers = %d, %d; want 1, 2", doc.Lines[0].Num, doc.Lines[1].Num)
	}
}
