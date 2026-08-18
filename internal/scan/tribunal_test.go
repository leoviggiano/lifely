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
	if ledger[0].ID != "ideias-index-tsv:3" {
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
	root := house(t)
	if err := os.Remove(filepath.Join(root, "FOUNDER.md")); err != nil {
		t.Fatal(err)
	}
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
		"excerpt":       excerpt(long),
		"firstSentence": firstSentence(long),
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
		if strings.Contains(p.Title, "outra secao") && p.Blocks == pendency.Founder {
			t.Error("an item outside a Faixa inherited the founder lane")
		}
	}
}

// A broken ledger must say WHICH file broke, and must not erase the report of
// another one that broke before it.
func TestLedgerErrorNamesTheFile(t *testing.T) {
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
