package scan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The link is a QUERY parameter, so every character that means something in a
// query has to be escaped -- '&' would truncate the path, '=' would split it,
// '+' would arrive as a space. Two earlier fixes traded one wrong escaper for
// another; this pins the property instead of the call.
func TestObsidianURIEscapesQueryMetacharacters(t *testing.T) {
	got := obsidianURI("/Users/leo/a & b/c=d/e+f/nota com espaço.md")
	for _, raw := range []string{"&", "=", "+"} {
		if strings.Contains(strings.TrimPrefix(got, "obsidian://open?path="), raw) {
			t.Errorf("the URI carries a raw %q: %q", raw, got)
		}
	}
	if !strings.Contains(got, "%20") {
		t.Errorf("the space was not percent-encoded: %q", got)
	}
}

// What the ledger identity DOES promise, stated as a test.
//
// Stable across edits, unique, and independent of the other rows cannot all
// hold without an explicit id column. The declared choice keeps uniqueness and
// row-locality; stability is promised only against the two cells that change
// by design -- the status and any date.
func TestNaturalKeyIgnoresStatusAndDates(t *testing.T) {
	root := t.TempDir()
	const header = "# projeto\ttitulo\tatualizado\tstatus\n"

	write(t, filepath.Join(root, "fila.tsv"), header+"lifely\trevisar spec\t2026-08-18\tpendente\n")
	first := find(Tribunal(root).Pendencies, "A2")

	write(t, filepath.Join(root, "fila.tsv"), header+"lifely\trevisar spec\t2026-09-30\tem-refino\n")
	second := find(Tribunal(root).Pendencies, "A2")

	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("got %d then %d rows, want 1 each", len(first), len(second))
	}
	if first[0].ID != second[0].ID {
		t.Errorf("status or date moved the identity: %q became %q", first[0].ID, second[0].ID)
	}
}

// `sessions/` is picked lexicographically among date-shaped names, and an
// unanchored pattern lets "2026-08-18-rascunho" beat the real round.
func TestSessionDirectoryMustBeExactlyADate(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "sessions", "2026-08-18", "summary.md"), "## Pendente\n\nreal.\n")
	write(t, filepath.Join(root, "sessions", "2026-08-18-rascunho", "summary.md"), "## Pendente\n\nrascunho.\n")

	got := find(Tribunal(root).Pendencies, "A3")
	if len(got) != 1 {
		t.Fatalf("got %d summaries, want 1", len(got))
	}
	if !strings.HasSuffix(got[0].Source, "2026-08-18") {
		t.Errorf("a draft directory won the pick: %q", got[0].Source)
	}
}

// Two board items that share their first 90 runes must not collapse into one
// pendency -- and one of them would silently vanish from the panel.
func TestFounderBoardItemsWithTheSameOpeningDoNotCollide(t *testing.T) {
	long := strings.Repeat("a", 95)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "FOUNDER.md"),
		[]byte("## Faixa 1\n\n- [ ] **"+long+"X**\n- [ ] **"+long+"Y**\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := find(Tribunal(root).Pendencies, "A1")
	if len(got) != 2 {
		t.Fatalf("got %d items, want 2", len(got))
	}
	if got[0].ID == got[1].ID {
		t.Errorf("two distinct items share the id %q -- one of them disappears", got[0].ID)
	}
}

// Inserting an item at the top of the board must not renumber everyone below.
//
// The first fix for id collisions put a file-wide counter into the identity,
// which encodes POSITION -- so adding one line orphaned every conversation
// under it. Truncation belongs to display, never to identity.
func TestFounderBoardIdentitySurvivesInsertion(t *testing.T) {
	root := t.TempDir()
	board := "## Faixa 1\n\n- [ ] **Decidir E18**\n- [ ] **Auditoria amostral**\n"
	if err := os.WriteFile(filepath.Join(root, "FOUNDER.md"), []byte(board), 0o644); err != nil {
		t.Fatal(err)
	}
	before := map[string]bool{}
	for _, p := range find(Tribunal(root).Pendencies, "A1") {
		before[p.ID] = true
	}
	if len(before) != 2 {
		t.Fatalf("got %d items, want 2", len(before))
	}

	// Somebody adds an item above the others.
	inserted := "## Faixa 1\n\n- [ ] **Item novo no topo**\n- [ ] **Decidir E18**\n- [ ] **Auditoria amostral**\n"
	if err := os.WriteFile(filepath.Join(root, "FOUNDER.md"), []byte(inserted), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, p := range find(Tribunal(root).Pendencies, "A1") {
		if strings.Contains(p.Title, "novo no topo") {
			continue
		}
		if !before[p.ID] {
			t.Errorf("inserting a line changed the id of %q to %q", p.Title, p.ID)
		}
	}
}

// A life.md marker takes its identity from the WHOLE line, not from the
// leading counter: two markers can share a number and mean different things.
// (The comment here was copy-pasted from the board test above and described
// that test instead of this one.)
func TestLifeMarkerIdentityUsesTheWholeLine(t *testing.T) {
	long := strings.Repeat("z", 130)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "life.md"),
		[]byte("# life\n\n[ABERTO] "+long+"A\n[ABERTO] "+long+"B\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := find(Tribunal(root).Pendencies, "A6")
	if len(got) != 2 {
		t.Fatalf("got %d markers, want 2", len(got))
	}
	if got[0].ID == got[1].ID {
		t.Errorf("two markers that differ past rune 120 share the id %q", got[0].ID)
	}
}

// Two ledger rows without an id column, differing only in their first column,
// are different rows and must not collapse into one pendency.
func TestLedgerRowsWithoutIdDoNotCollide(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "fila.tsv"),
		"# projeto\ttitulo\tstatus\nlifely\trevisar spec\tpendente\nject\trevisar spec\tpendente\n")
	got := find(Tribunal(root).Pendencies, "A2")
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2", len(got))
	}
	if got[0].ID == got[1].ID {
		t.Errorf("two rows share the id %q -- one disappears from the panel", got[0].ID)
	}
}

// Date cells are dropped from the key so a touched row keeps its identity --
// and that is exactly what lets two rows differing ONLY in a date collapse
// into one. Uniqueness is the promise that wins here: the second row must
// still get its own id, even at the cost of depending on its position.
func TestLedgerRowsDifferingOnlyByDateDoNotCollide(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "fila.tsv"),
		"# projeto\tatualizado\tstatus\nlifely\t2026-08-17\tpendente\nlifely\t2026-08-18\tpendente\n")
	got := find(Tribunal(root).Pendencies, "A2")
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2", len(got))
	}
	if got[0].ID == got[1].ID {
		t.Errorf("two rows share the id %q -- one disappears from the panel", got[0].ID)
	}
}

// The panel renders text, not markdown: the title is cleaned for display.
//
// The identity is a different question and it keys on the RAW line, so bolding
// a word DOES mint a new id -- the declared cost of keying on what the row
// says about itself (see naturalKey). This comment used to claim the opposite
// of what the assertion below checks; the assertion is the one that was right.
func TestBoardTitleIsPlainTextButIdentityIsNot(t *testing.T) {
	root := t.TempDir()
	board := "## Faixa 1\n\n- [ ] **Construir o lifely** -- aprovado\n"
	if err := os.WriteFile(filepath.Join(root, "FOUNDER.md"), []byte(board), 0o644); err != nil {
		t.Fatal(err)
	}
	got := find(Tribunal(root).Pendencies, "A1")
	if len(got) != 1 {
		t.Fatalf("got %d items, want 1", len(got))
	}
	if strings.Contains(got[0].Title, "**") {
		t.Errorf("the panel shows raw markdown: %q", got[0].Title)
	}
	if !strings.Contains(got[0].Title, "Construir o lifely") {
		t.Errorf("stripping emphasis ate the sentence: %q", got[0].Title)
	}

	plain := "## Faixa 1\n\n- [ ] Construir o lifely -- aprovado\n"
	if err := os.WriteFile(filepath.Join(root, "FOUNDER.md"), []byte(plain), 0o644); err != nil {
		t.Fatal(err)
	}
	unbolded := find(Tribunal(root).Pendencies, "A1")
	if len(unbolded) == 1 && unbolded[0].ID == got[0].ID {
		t.Error("identity followed the DISPLAY text: unbolding a line would keep its id, which means bolding one silently moves it")
	}
}

// Removing the positional counter from the A1 identity was right; removing the
// LANE with it was not. The same title under two faixas is two different
// items — one waits on the founder, the other on an agent.
func TestFounderBoardSameTitleInTwoLanes(t *testing.T) {
	root := t.TempDir()
	board := "## Faixa 1\n\n- [ ] **Higiene do repo**\n\n## Faixa 3\n\n- [ ] **Higiene do repo**\n"
	if err := os.WriteFile(filepath.Join(root, "FOUNDER.md"), []byte(board), 0o644); err != nil {
		t.Fatal(err)
	}
	got := find(Tribunal(root).Pendencies, "A1")
	if len(got) != 2 {
		t.Fatalf("got %d items, want 2", len(got))
	}
	if got[0].ID == got[1].ID {
		t.Errorf("the same title in two lanes shares the id %q", got[0].ID)
	}
}

// A ledger that HAS an id column but leaves it blank on a row must still
// disambiguate that row: the presence of the header is not the presence of a key.
func TestLedgerWithBlankIdStillDisambiguates(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "fila.tsv"),
		"# id\tprojeto\ttitulo\tstatus\n\tlifely\trevisar\tpendente\n\tject\trevisar\tpendente\n")
	got := find(Tribunal(root).Pendencies, "A2")
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2", len(got))
	}
	if got[0].ID == got[1].ID {
		t.Errorf("two rows with blank ids share %q", got[0].ID)
	}
}

// A tiebreaker that does not differ between the colliding rows breaks nothing
// and hides the collision behind a longer id.
func TestTiebreakerMustActuallyDistinguish(t *testing.T) {
	root := t.TempDir()
	// `projeto` is identical on both rows; only `responsavel` tells them apart.
	write(t, filepath.Join(root, "fila.tsv"),
		"# projeto\ttitulo\tresponsavel\tstatus\n"+
			"lifely\trevisar\tana\tpendente\n"+
			"lifely\trevisar\tbruno\tpendente\n")
	got := find(Tribunal(root).Pendencies, "A2")
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2", len(got))
	}
	if got[0].ID == got[1].ID {
		t.Errorf("the tiebreaker did not distinguish: both rows are %q", got[0].ID)
	}
}

// A heading that itself raises a question is both context and item. Skipping
// every '#' line dropped exactly the markers someone bothered to promote.
func TestHeadingThatOpensWithAMarkerIsAnItem(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "life.md"),
		[]byte("# life\n\n## [ABERTO] onde passa a fronteira?\n\ntexto\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := find(Tribunal(root).Pendencies, "A6")
	if len(got) != 1 {
		t.Fatalf("got %d markers, want 1 -- the heading was dropped", len(got))
	}
	if !strings.Contains(got[0].Title, "fronteira") {
		t.Errorf("title = %q", got[0].Title)
	}
}

// A repository with no sessions/ directory is normal absence, not a source
// that could not be read -- the same answer this scanner gives for a round
// without a summary.
func TestMissingSessionsDirectoryIsNotAFinding(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "FOUNDER.md"), []byte("## Faixa 1\n\n- [ ] **algo**\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, s := range Tribunal(root).Sources {
		if strings.Contains(s.Name, "summary") && s.Err != "" {
			t.Errorf("a missing sessions/ was reported as unreadable: %q", s.Err)
		}
	}
}

// The board's own convention is `- [ ] **Título** — nota que muda`. Keying on
// the whole line means editing the note after the title mints a new id and
// orphans the conversation -- the same defect fixed for ledgers, left behind
// here.
func TestFounderBoardIdentityIgnoresTheTrailingNote(t *testing.T) {
	root := t.TempDir()
	write := func(note string) {
		body := "## Faixa 1\n\n- [ ] **Construir o lifely** — " + note + "\n"
		if err := os.WriteFile(filepath.Join(root, "FOUNDER.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("aprovado, execucao nesta janela")
	before := find(Tribunal(root).Pendencies, "A1")
	write("status 18-08: quatro portas abertas, portao rodando")
	after := find(Tribunal(root).Pendencies, "A1")

	if len(before) != 1 || len(after) != 1 {
		t.Fatalf("got %d then %d items, want 1 each", len(before), len(after))
	}
	if before[0].ID != after[0].ID {
		t.Errorf("editing the note changed the id: %q became %q", before[0].ID, after[0].ID)
	}
}

// The cost of that choice, pinned so nobody "fixes" it back by accident.
//
// Editing a cell the key is built from DOES mint a new id and orphan the
// conversation attached to it. Deliberate: a row that disappears from the
// panel is a decision the founder never sees, while an orphaned conversation
// still leaves the item on screen. The way out lives in the source -- give the
// ledger an `id` column -- not here.
func TestLedgerIdentityMovesWithContent_AcceptedCost(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "fila.tsv"), "# projeto\ttitulo\tstatus\nlifely\trevisar\tpendente\n")
	before := find(Tribunal(root).Pendencies, "A2")
	write(t, filepath.Join(root, "fila.tsv"), "# projeto\ttitulo\tstatus\nlifely\trevisar tudo\tpendente\n")
	after := find(Tribunal(root).Pendencies, "A2")

	if len(before) != 1 || len(after) != 1 {
		t.Fatalf("got %d then %d rows, want 1 each", len(before), len(after))
	}
	if before[0].ID == after[0].ID {
		t.Error("the id survived a content edit -- if that is now intended, the trade-off comment in naturalKey is stale")
	}
}

// With an id column none of that applies: the identity IS the id.
func TestLedgerWithAnIdColumnIsFullyStable(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "fila.tsv"), "# id\tprojeto\ttitulo\tstatus\nX1\tlifely\trevisar\tpendente\n")
	before := find(Tribunal(root).Pendencies, "A2")
	write(t, filepath.Join(root, "fila.tsv"), "# id\tprojeto\ttitulo\tstatus\tnota\nX1\tlifely\trevisar tudo\tem-refino\toutra\n")
	after := find(Tribunal(root).Pendencies, "A2")

	if len(before) != 1 || len(after) != 1 {
		t.Fatalf("got %d then %d rows, want 1 each", len(before), len(after))
	}
	if before[0].ID != after[0].ID {
		t.Errorf("an explicit id did not hold the identity: %q became %q", before[0].ID, after[0].ID)
	}
}

// The key is the bold title that OPENS the item, not any bold run in the line:
// `- [ ] fazer X — **urgente** hoje` names an item called "fazer X".
func TestBoardKeyUsesTheOpeningBoldOnly(t *testing.T) {
	root := t.TempDir()
	body := "## Faixa 1\n\n- [ ] fazer a migracao — **urgente** ate sexta\n- [ ] revisar o portao — **urgente** tambem\n"
	if err := os.WriteFile(filepath.Join(root, "FOUNDER.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got := find(Tribunal(root).Pendencies, "A1")
	if len(got) != 2 {
		t.Fatalf("got %d items, want 2", len(got))
	}
	if got[0].ID == got[1].ID {
		t.Errorf("two different items keyed on the same inline bold: %q", got[0].ID)
	}
}

// Counting digits is not recognising a date. Money, phone numbers and ids like
// `PROJ-123456` are stable values that belong in an identity; the old
// heuristic ("8+ chars, 6+ digits") threw them away as if they aged.
func TestLooksLikeDateOnlyMatchesDates(t *testing.T) {
	dates := []string{"2026-08-18", "2026-08-18T21:30:00Z", "18/08/2026", "18-08-2026"}
	for _, v := range dates {
		if !looksLikeDate(v) {
			t.Errorf("looksLikeDate(%q) = false, want true", v)
		}
	}
	notDates := []string{"R$ 1.234.567", "PROJ-123456", "+55 11 91234-5678", "0.0.1-bootstrap"}
	for _, v := range notDates {
		if looksLikeDate(v) {
			t.Errorf("looksLikeDate(%q) = true -- a stable value was discarded as a timestamp", v)
		}
	}
}

// A title that slugs to nothing must not key on the empty string: every such
// line would collapse into one pendency.
func TestBoardItemsThatSlugToNothingDoNotCollide(t *testing.T) {
	root := t.TempDir()
	body := "## Faixa 1\n\n- [ ] ***\n- [ ] ###\n"
	if err := os.WriteFile(filepath.Join(root, "FOUNDER.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got := find(Tribunal(root).Pendencies, "A1")
	if len(got) != 2 {
		t.Fatalf("got %d items, want 2", len(got))
	}
	if got[0].ID == got[1].ID {
		t.Errorf("two unnameable items share the id %q", got[0].ID)
	}
}
