package scan

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/leoviggiano/lifely/internal/pendency"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// house builds a miniature of the record repository.
func house(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	write(t, filepath.Join(root, "FOUNDER.md"), `# FOUNDER.md

## Faixa 1 — Só você

- [ ] **Construir o lifely** — aprovado, execução nesta janela.
      Detalhe que continua na linha seguinte.
- [x] ~~Decidir E14~~ — decidida em 13-08.
- [ ] **Auditoria amostral por fase**

## Faixa 3 — IA executa

- [ ] Higiene do repo do life
`)
	write(t, filepath.Join(root, "ideias", "index.tsv"),
		"# id\tdata\ttitulo\tstatus\n3\t2026-08-18\tmonitor-insight\tem-refino\n1\t2026-08-17\torcamento\taprovada\n")
	write(t, filepath.Join(root, "life.md"), `# life

## 17.3

Texto normal sem marcador.
[ABERTO] teto percentual emendaria o §15.1?
`)
	write(t, filepath.Join(root, "pauta-ferramentas.md"), "pauta\n")
	write(t, filepath.Join(root, "sessions", "2026-08-18", "summary.md"),
		"# Sessao\n\n## Pendente\n\nfalta o veredito do fundador.\n")
	return root
}

func find(items []pendency.Pendency, class string) []pendency.Pendency {
	var out []pendency.Pendency
	for _, p := range items {
		if p.Class == class {
			out = append(out, p)
		}
	}
	return out
}

func TestTribunalReadsEachSource(t *testing.T) {
	res := Tribunal(house(t))

	// A1: two open items, the closed one ignored.
	if got := find(res.Pendencies, "A1"); len(got) != 3 {
		t.Errorf("FOUNDER.md gave %d open items, want 3", len(got))
	}
	// A2: only the non-terminal row.
	ledger := find(res.Pendencies, "A2")
	if len(ledger) != 1 {
		t.Fatalf("the ledger gave %d open rows, want 1", len(ledger))
	}
	// The raw path, not its slug: two ledgers whose paths slug the same used
	// to share a namespace and one row would take the other's identity.
	if ledger[0].ID != "ideias/index.tsv:3" {
		t.Errorf("ledger id = %q, want the row's own id", ledger[0].ID)
	}
	if ledger[0].Surface != "/ideia <id> aderir · desistir · refinar" {
		t.Errorf("ledger surface = %q, want the owning command", ledger[0].Surface)
	}
	for _, class := range []string{"A3", "A4", "A6"} {
		if len(find(res.Pendencies, class)) == 0 {
			t.Errorf("class %s produced nothing", class)
		}
	}
}

// The rule the tribunal's own panel is built on: a ledger born after this code
// was written must appear without anyone editing the scanner (spec FR1.1).
func TestNewLedgerEntersOnItsOwn(t *testing.T) {
	root := house(t)
	before := len(find(Tribunal(root).Pendencies, "A2"))

	write(t, filepath.Join(root, "medicao", "orcamento-2027.tsv"),
		"# id\tprojeto\tstatus\nX1\tlifely\tpendente\n")

	after := len(find(Tribunal(root).Pendencies, "A2"))
	if after != before+1 {
		t.Errorf("a brand new ledger did not enter on its own: %d rows before, %d after", before, after)
	}
}

// A file without a status column is not a ledger of decisions and must not
// invent pendencies.
func TestTsvWithoutStatusIsIgnored(t *testing.T) {
	root := house(t)
	before := len(find(Tribunal(root).Pendencies, "A2"))
	write(t, filepath.Join(root, "medicao", "cota.tsv"), "# quando\tpct\n2026-08-18\t98\n")
	if after := len(find(Tribunal(root).Pendencies, "A2")); after != before {
		t.Errorf("a tsv without a status column produced %d extra items", after-before)
	}
}

// A source that exists and cannot be read is a finding, not silence -- and it
// must not take the other sources down with it (spec FR1.3).
func TestUnreadableSourceIsReportedNotFatal(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not block reads")
	}
	root := house(t)
	// UNREADABLE, not absent. This test removed the file and still checked for
	// a finding -- asserting the opposite of the contract above it, and of the
	// absence-vs-failure rule the rest of this scanner is built on. The
	// root-skip guard at the top was the tell: it only means anything for a
	// permission scenario, which is what the name always promised.
	if err := os.Chmod(filepath.Join(root, "FOUNDER.md"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(root, "FOUNDER.md"), 0o644) })
	res := Tribunal(root)

	var reported bool
	for _, s := range res.Sources {
		if s.Name == "FOUNDER.md" && s.Err != "" {
			reported = true
		}
	}
	if !reported {
		t.Error("the missing FOUNDER.md was not reported as a finding")
	}
	if len(find(res.Pendencies, "A2")) == 0 {
		t.Error("one broken source silenced the others")
	}
}

// A source that simply is not there is NOT a finding: absence is normal, and
// marking it teaches the reader to ignore the one marker that should always
// mean something.
func TestAbsentSourceIsNotAFinding(t *testing.T) {
	root := house(t)
	if err := os.Remove(filepath.Join(root, "FOUNDER.md")); err != nil {
		t.Fatal(err)
	}
	for _, s := range Tribunal(root).Sources {
		if s.Name == "FOUNDER.md" && s.Err != "" {
			t.Errorf("an absent FOUNDER.md was reported as unreadable: %q", s.Err)
		}
	}
}

// An empty source is omitted: a "0 pending" line is noise (spec FR1.4).
func TestEmptySourceIsOmitted(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "FOUNDER.md"), "# nada aberto\n\n## Faixa 1\n\n- [x] ~~feito~~\n")
	for _, s := range Tribunal(root).Sources {
		if s.Name == "FOUNDER.md" && s.Err == "" && s.Count == 0 {
			t.Error("a source with nothing open still took a line in the panel")
		}
	}
}

func TestDirtyTreeIsHygiene(t *testing.T) {
	root := house(t)
	if err := exec.Command("git", "-C", root, "init", "-q").Run(); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	got := find(Tribunal(root).Pendencies, "A5")
	if len(got) != 1 {
		t.Fatalf("a dirty tree gave %d items, want 1", len(got))
	}
	if got[0].Blocks != pendency.Hygiene {
		t.Errorf("a dirty tree blocks %q, want %q", got[0].Blocks, pendency.Hygiene)
	}
}

// life.md mentions its own markers constantly -- in the changelog table, and
// in the legend that defines what they mean. Matching the marker anywhere
// turned 60+ rows of history into pendencies and buried the dozen real ones,
// measured against the real file. Only a line the marker OPENS is an item.
func TestOpenMarkerIgnoresMentions(t *testing.T) {
	raises := []string{
		"[ABERTO] teto percentual emendaria o §15.1?",
		"- [PROPOSTA] corrigir 15.6 para 15.7",
		"> [ABERTO] confirmar o termo com o cliente",
		"3. [PROPOSTA] promover a decisao",
	}
	for _, line := range raises {
		if _, _, ok := openMarker(line); !ok {
			t.Errorf("openMarker(%q) = false, want true", line)
		}
	}

	mentions := []string{
		"| 2.5.17 | 13-08 | **[DIREÇÃO]** ... residual [ABERTO] que segue |",
		"| **[ABERTO]** — Questão em aberto |",
		"a decisao segue [ABERTO] ate o veredito",
		"Promoção de [PROPOSTA] exige veredito explicito",
	}
	for _, line := range mentions {
		if _, _, ok := openMarker(line); ok {
			t.Errorf("openMarker(%q) = true, want false -- that is a mention, not an item", line)
		}
	}
}

func TestLifeMarkerTitleIsNotDoubled(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "life.md"), "# life\n\n[ABERTO] teto percentual emendaria o §15.1?\n")
	got := find(Tribunal(root).Pendencies, "A6")
	if len(got) != 1 {
		t.Fatalf("got %d items, want 1", len(got))
	}
	if want := "[ABERTO] teto percentual emendaria o §15.1?"; got[0].Title != want {
		t.Errorf("title = %q, want %q", got[0].Title, want)
	}
}

// Titles here are accented Portuguese. Cutting by bytes lands inside a
// multi-byte rune and renders as a replacement character -- visible in the
// real sweep output before the gate named it.
func TestTruncationCutsRunesNotBytes(t *testing.T) {
	long := strings.Repeat("ç", 200)
	for name, got := range map[string]string{
		"excerpt": excerpt(long),
	} {
		if strings.ContainsRune(got, '\uFFFD') {
			t.Errorf("%s produced a broken rune", name)
		}
		if !utf8.ValidString(got) {
			t.Errorf("%s produced invalid utf-8", name)
		}
	}
}

// A ledger row whose status cell is empty or missing is NOT decided: hiding a
// decision that is still waiting is the failure this scanner refuses.
func TestEmptyStatusCountsAsOpen(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "fila.tsv"), "# id\ttitulo\tstatus\nX1\tsem status\t\nX2\ttruncada\n")
	got := find(Tribunal(root).Pendencies, "A2")
	if len(got) != 2 {
		t.Fatalf("got %d open rows, want 2 (empty and missing status are both open)", len(got))
	}
}

// The panel must be able to reach zero: "nada pendente" is a result. A summary
// that closed clean is not a pendency.
func TestCleanSummaryIsNotAPendency(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "sessions", "2026-08-18", "summary.md"),
		"# Sessao\n\n## Entregue\n\nTudo fechado.\n")
	if got := find(Tribunal(root).Pendencies, "A3"); len(got) != 0 {
		t.Errorf("a summary with nothing open produced %d pendencies", len(got))
	}

	write(t, filepath.Join(root, "sessions", "2026-08-18", "summary.md"),
		"# Sessao\n\n## Pendente\n\nfalta o veredito.\n")
	if got := find(Tribunal(root).Pendencies, "A3"); len(got) != 1 {
		t.Errorf("a summary that carries something forward produced %d pendencies, want 1", len(got))
	}
}

// An item under a heading that is not a Faixa must not inherit the previous
// lane's blocker.
func TestFaixaDoesNotLeakAcrossSections(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "FOUNDER.md"), `## Faixa 1

- [ ] **Decidir E18**

## Rodape

- [ ] **Item de outra secao**
`)
	for _, p := range find(Tribunal(root).Pendencies, "A1") {
		if !strings.Contains(p.Title, "outra secao") {
			continue
		}
		// Not Founder (the lane must not leak into a non-Faixa section) and
		// not AI either (that hid the line from the person whose board it is).
		// Unlaned is unclassified, and unclassified on a board built out of
		// lanes is hygiene.
		if p.Blocks != pendency.Hygiene {
			t.Errorf("an item outside a Faixa was routed to %q, want hygiene", p.Blocks)
		}
	}
}

// A broken ledger must say WHICH file broke, and must not erase the report of
// another one that broke before it.
func TestLedgerErrorNamesTheFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not block reads")
	}
	root := t.TempDir()
	write(t, filepath.Join(root, "bom.tsv"), "# id\tstatus\nA\tpendente\n")
	bad := filepath.Join(root, "ruim.tsv")
	write(t, bad, "# id\tstatus\n")
	if err := os.Chmod(bad, 0o000); err != nil {
		t.Skipf("cannot make a file unreadable here: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(bad, 0o644) })

	var reported string
	for _, s := range Tribunal(root).Sources {
		if s.Name == "ledgers *.tsv" {
			reported = s.Err
		}
	}
	if !strings.Contains(reported, "ruim.tsv") {
		t.Errorf("the ledger error did not name the file: %q", reported)
	}
}

// The marker must not be printed twice, including when the line opens with a
// bullet or a quote -- stripping it from the raw text silently fails there.
func TestLifeMarkerTitleNotDoubledWithBullets(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "life.md"),
		"# life\n\n- [ABERTO] teto percentual?\n> [PROPOSTA] corrigir 15.6 para 15.7\n")
	for _, p := range find(Tribunal(root).Pendencies, "A6") {
		marker, _, _ := openMarker(p.Title)
		if strings.Count(p.Title, marker) != 1 {
			t.Errorf("title %q repeats the marker", p.Title)
		}
	}
}

// A ledger status written naturally, with accents, must still count as decided.
func TestTerminalStatusWithAccents(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "fila.tsv"), "# id\ttitulo\tstatus\nA\tfeita\tconcluída\nB\taberta\tpendente\n")
	got := find(Tribunal(root).Pendencies, "A2")
	if len(got) != 1 {
		t.Fatalf("got %d open rows, want 1 -- an accented terminal status was read as open", len(got))
	}
}

// Obsidian percent-decodes the path parameter, so a space has to arrive as
// %20. QueryEscape sends "+", which the app reads literally and fails to open.
func TestObsidianURIEncodesSpacesAsPercent20(t *testing.T) {
	got := obsidianURI("/Users/leo/my life be life/nota com acento é.md")
	if strings.Contains(got, "+") {
		t.Errorf("the URI carries a literal plus: %q", got)
	}
	if !strings.Contains(got, "%20") {
		t.Errorf("the space was not percent-encoded: %q", got)
	}
}

// A stray directory under sessions/ must not win the "most recent round" pick.
func TestLatestSummaryIgnoresNonDateDirectories(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "sessions", "2026-08-18", "summary.md"), "## Pendente\n\nfalta o veredito.\n")
	write(t, filepath.Join(root, "sessions", "zzz-rascunho", "summary.md"), "## Pendente\n\nlixo.\n")

	got := find(Tribunal(root).Pendencies, "A3")
	if len(got) != 1 {
		t.Fatalf("got %d summaries, want 1", len(got))
	}
	if !strings.Contains(got[0].Source, "2026-08-18") {
		t.Errorf("the stray directory won the pick: %q", got[0].Source)
	}
}

// A --root that does not exist is a FINDING, and it is named as the root.
//
// Two wrong answers were tried before this one. First it came back as "the
// ledgers are unreadable", pointing the reader at the wrong thing. The fix for
// that made it silent -- and silence was worse: a typo in --root then read
// exactly like a clean tribunal, so "nothing pending" meant "I never looked".
// Absent SOURCE is silence; absent ROOT is a finding.
func TestAbsentRootIsReportedAsTheRootNotAsALedger(t *testing.T) {
	res := Tribunal(filepath.Join(t.TempDir(), "does-not-exist"))

	if len(res.Sources) != 1 {
		t.Fatalf("got %d sources, want exactly 1 (the root)", len(res.Sources))
	}
	got := res.Sources[0]
	if got.Name != "root" {
		t.Errorf("the finding is named %q, want \"root\": a missing root must not be blamed on a source inside it", got.Name)
	}
	if got.Err == "" {
		t.Error("an absent root produced no finding: nothing pending would mean nothing looked at")
	}
	if len(res.Pendencies) != 0 {
		t.Errorf("got %d pendencies from a root that does not exist", len(res.Pendencies))
	}
}

// The lane follows document NESTING, which is the rule that stops needing a
// new answer every time a board grows a section.
//
// Three answers were tried in three rounds: reset only on `## ` (a `# Apêndice`
// then inherited the lane), reset on any heading (a `### Caixa` subsection then
// demoted its own items), and finally this one -- same level or shallower
// closes the lane, deeper is inside it.
func TestLaneEndsAtASiblingHeadingButNotAtASubsection(t *testing.T) {
	for _, c := range []struct {
		heading  string
		stillHis bool
	}{
		{"# Apêndice", false},  // shallower: a new top-level section
		{"## Rodape", false},   // sibling: a new lane-level section
		{"### Caixa", true},    // deeper: a subsection OF Faixa 1
		{"#### Detalhe", true}, // deeper still
	} {
		t.Run(c.heading, func(t *testing.T) {
			root := t.TempDir()
			write(t, filepath.Join(root, "FOUNDER.md"),
				"## Faixa 1\n\n- [ ] **Dele**\n\n"+c.heading+"\n\n- [ ] **Depois**\n")
			for _, p := range find(Tribunal(root).Pendencies, "A1") {
				if !strings.Contains(p.Title, "Depois") {
					continue
				}
				got := p.Blocks == pendency.Founder
				if got != c.stillHis {
					t.Errorf("item after %q: founder lane = %v, want %v", c.heading, got, c.stillHis)
				}
			}
		})
	}
}

// A `#` inside a fenced code block is code, not a section. The lane rule
// widened to every heading level, and this widened with it: a board that shows
// a shell snippet would have closed the lane on its comment line.
func TestFencedCodeBlockDoesNotCloseTheLane(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "FOUNDER.md"),
		"## Faixa 1\n\n- [ ] **Antes**\n\n```sh\n# isto e um comentario de shell\n```\n\n- [ ] **Depois**\n")

	var found bool
	for _, p := range find(Tribunal(root).Pendencies, "A1") {
		if strings.Contains(p.Title, "Depois") {
			found = true
			if p.Blocks != pendency.Founder {
				t.Errorf("an item after a fenced block left lane 1: blocks = %q", p.Blocks)
			}
		}
	}
	if !found {
		t.Fatal("the item after the fenced block was not swept at all")
	}
}

// A `## Pendencias` inside a fenced example is content, never a carried-over
// section: carriesForward matched the whole body as one string, so a clean
// round whose summary SHOWS the template read as carrying work forward.
// (defect D4)
func TestFencedPendencyHeadingDoesNotCarryForward(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "sessions", "2026-08-19", "summary.md"),
		"# Sessao\n\n## Entregue\n\ntudo fechado.\n\n```markdown\n## Pendencias\n\nexemplo de template.\n```\n")
	if got := find(Tribunal(root).Pendencies, "A3"); len(got) != 0 {
		t.Errorf("a fenced example heading produced %d pendencies, want 0", len(got))
	}
}

// A '#' without a space after it is a hashtag, not a heading: '#tag' must not
// open a section, and '# Tag' must. life.md read '#tag' as a heading and the
// hashtag contaminated the locator of every marker below it. (defect D6)
func TestHashWithoutSpaceIsNotASection(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "life.md"),
		"## Secao real\n\n#tag\n\n[ABERTO] pergunta aberta\n")
	got := find(Tribunal(root).Pendencies, "A6")
	if len(got) != 1 {
		t.Fatalf("got %d markers, want 1", len(got))
	}
	if !strings.Contains(got[0].Origin.Locator, "Secao real") {
		t.Errorf("locator = %q: the hashtag stole the section", got[0].Origin.Locator)
	}

	write(t, filepath.Join(root, "life.md"), "# Tag\n\n[ABERTO] pergunta aberta\n")
	got = find(Tribunal(root).Pendencies, "A6")
	if len(got) != 1 {
		t.Fatalf("got %d markers, want 1", len(got))
	}
	if !strings.Contains(got[0].Origin.Locator, "Tag") {
		t.Errorf("locator = %q: a real heading did not open its section", got[0].Origin.Locator)
	}
}

// A fenced snippet nested under a board item is part of that item's Detail.
// Inside a fence the line is CONTENT: the guard exists to stop it becoming
// structure, never to swallow it -- A1 dropped these lines while A7 declared
// the opposite rule for the same guard, two copies each sure of itself.
// (defect D2)
func TestFencedSnippetStaysInTheItemDetail(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "FOUNDER.md"),
		"## Faixa 1\n\n- [ ] **Com snippet**\n  contexto\n```sh\nject start lifely-028\n```\n  depois\n")
	got := find(Tribunal(root).Pendencies, "A1")
	if len(got) != 1 {
		t.Fatalf("got %d items, want 1", len(got))
	}
	if !strings.Contains(got[0].Detail, "ject start lifely-028") {
		t.Errorf("the fenced command was dropped from the Detail: %q", got[0].Detail)
	}
	if !strings.Contains(got[0].Detail, "depois") {
		t.Errorf("the prose after the fence was dropped from the Detail: %q", got[0].Detail)
	}
}

// Every CommonMark list marker is swept, and the marker never leaks into the
// identity: the same title keeps the same id whichever marker wrote it. Items
// written with '*' or '+' used to vanish with no count, no Err and no Note --
// an invisible item on the founder's own board. (defect D5)
func TestEveryListMarkerIsSweptWithTheSameIdentity(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "FOUNDER.md"),
		"## Faixa 1\n\n- [ ] **Um**\n* [ ] **Dois**\n+ [ ] **Tres**\n")
	got := find(Tribunal(root).Pendencies, "A1")
	if len(got) != 3 {
		t.Fatalf("swept %d items, want 3 -- a marker variant vanished from the board", len(got))
	}

	write(t, filepath.Join(root, "FOUNDER.md"), "## Faixa 1\n\n- [ ] **Mesma tarefa**\n")
	dash := find(Tribunal(root).Pendencies, "A1")
	write(t, filepath.Join(root, "FOUNDER.md"), "## Faixa 1\n\n* [ ] **Mesma tarefa**\n")
	star := find(Tribunal(root).Pendencies, "A1")
	if len(dash) != 1 || len(star) != 1 {
		t.Fatalf("got %d then %d items, want 1 each", len(dash), len(star))
	}
	if dash[0].ID != star[0].ID {
		t.Errorf("rewriting the marker moved the identity: %q became %q", dash[0].ID, star[0].ID)
	}
}

// An unbalanced code fence must be a FINDING, never silence. The fence guard
// skips everything between fences, so a fence that never closes swallowed the
// rest of the founder's board with nothing on screen to say so.
func TestUnbalancedFenceIsReported(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "FOUNDER.md"),
		"## Faixa 1\n\n- [ ] **Antes**\n\n```sh\n# a cerca nunca fecha\n\n- [ ] **Perdido**\n")

	res := Tribunal(root)
	var marked bool
	for _, s := range res.Sources {
		if s.Name == "FOUNDER.md" && strings.Contains(s.Err, "never closed") {
			marked = true
		}
	}
	if !marked {
		t.Error("the board was truncated by an unbalanced fence and nothing said so")
	}
}

// The snippet survives the blank line a board actually writes. The rule that
// keeps fenced content in the item's Detail used to hold only when the fence
// ABUTTED the item: a blank line closed the item through the default arm, and
// every fenced line after it was dropped for having no item to belong to. The
// spelling below -- item, blank line, fence -- is the normal one, so AC002 was
// green in the only spelling that worked. (defect D2, gate finding
// `founderboard-blank-line-drops-fenced-snippet`)
func TestFencedSnippetSurvivesABlankLineAfterTheItem(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "FOUNDER.md"),
		"## Faixa 1\n\n- [ ] **Com snippet**\n\n```sh\nject start lifely-028\n```\n")
	got := find(Tribunal(root).Pendencies, "A1")
	if len(got) != 1 {
		t.Fatalf("got %d items, want 1", len(got))
	}
	if !strings.Contains(got[0].Detail, "ject start lifely-028") {
		t.Errorf("a blank line between the item and its fence dropped the command: %q", got[0].Detail)
	}
}

// An unclosed fence marks the source and stops there: it must not hand the
// last open item a Detail containing the rest of the board. Past the fence
// that never closed nothing is nested under anything -- every remaining line
// arrives Fenced only because the structure stopped being read. (gate finding
// `founderboard-unclosed-fence-detail-bloat`)
func TestUnclosedFenceDoesNotSwallowTheRestOfTheBoard(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "FOUNDER.md"),
		"## Faixa 1\n\n- [ ] **Item A**\n\n```sh\ncmd\n\n## Faixa 3\n\n- [ ] **Item B**\n")
	got := find(Tribunal(root).Pendencies, "A1")
	if len(got) == 0 {
		t.Fatalf("the board lost every item")
	}
	for _, p := range got {
		if strings.Contains(p.Detail, "Item B") || strings.Contains(p.Detail, "Faixa 3") {
			t.Errorf("item %q swallowed the rest of the board: %q", p.Title, p.Detail)
		}
	}
}

// Heading LEVEL is not part of the carried-over marker: `#` through `######`
// all count, so `### Pendentes` carries a round forward exactly like
// `## Pendencias`. The substring match this replaced searched for `"## "`,
// which pinned level 2 by accident of the string it looked for; the accident
// kept being read back as the rule. (FR7)
func TestCarriesForwardAtEveryHeadingLevel(t *testing.T) {
	for _, marker := range []string{"#", "##", "###", "####", "#####", "######"} {
		root := t.TempDir()
		write(t, filepath.Join(root, "sessions", "2026-08-19", "summary.md"),
			"# Sessao\n\n"+marker+" Pendentes\n\nfalta o veredito.\n")
		if got := find(Tribunal(root).Pendencies, "A3"); len(got) != 1 {
			t.Errorf("%q Pendentes produced %d pendencies, want 1 -- level leaked into the marker", marker, len(got))
		}
	}

	// The level is not a marker on its own: a heading at ANY level that does
	// not name pending work still closes the round clean.
	for _, marker := range []string{"#", "##", "###", "####", "#####", "######"} {
		root := t.TempDir()
		write(t, filepath.Join(root, "sessions", "2026-08-19", "summary.md"),
			"# Sessao\n\n"+marker+" Entregue\n\ntudo fechado.\n")
		if got := find(Tribunal(root).Pendencies, "A3"); len(got) != 0 {
			t.Errorf("%q Entregue produced %d pendencies, want 0", marker, len(got))
		}
	}
}
