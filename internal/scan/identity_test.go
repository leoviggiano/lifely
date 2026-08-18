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

// Skipping date-shaped cells is not enough: the scan keeps walking and can
// land on another volatile column. The identity has to come from a column the
// header NAMES, or from a stable location -- never from "whatever came next".
func TestNaturalKeyDoesNotLandOnAVolatileColumn(t *testing.T) {
	root := t.TempDir()
	const header = "# projeto\tstatus\tatualizado\tnota\n"

	write(t, filepath.Join(root, "fila.tsv"), header+"\tpendente\t2026-08-18\tprimeira nota\n")
	first := find(Tribunal(root).Pendencies, "A2")
	if len(first) != 1 {
		t.Fatalf("got %d rows, want 1", len(first))
	}

	// The naming column is empty and the date changed; only the note differs.
	write(t, filepath.Join(root, "fila.tsv"), header+"\tpendente\t2026-09-30\tnota reescrita\n")
	second := find(Tribunal(root).Pendencies, "A2")
	if len(second) != 1 {
		t.Fatalf("got %d rows, want 1", len(second))
	}
	if first[0].ID != second[0].ID {
		t.Errorf("identity moved with a note edit: %q became %q", first[0].ID, second[0].ID)
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
