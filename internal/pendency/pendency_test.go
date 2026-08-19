package pendency

import (
	"regexp"
	"testing"
)

var uuidShape = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// The conversation about a pendency is keyed by its UUID, so the same id must
// fold to the same UUID forever -- and it must be a UUID `claude --session-id`
// accepts (spec FR6.1).
func TestUUIDIsStableAndWellFormed(t *testing.T) {
	id := NewID("founder", "construir-o-lifely")
	first, second := UUID(id), UUID(id)
	if first != second {
		t.Errorf("UUID(%q) is not deterministic: %q then %q", id, first, second)
	}
	if !uuidShape.MatchString(first) {
		t.Errorf("UUID(%q) = %q, which is not a v5 UUID", id, first)
	}
	if other := UUID(NewID("founder", "outra-coisa")); other == first {
		t.Error("two different ids folded to the same conversation")
	}
}

func TestSlug(t *testing.T) {
	tests := map[string]string{
		"Construir o `lifely`":          "construir-o-lifely",
		"  As 4 emendas do portão  ":    "as-4-emendas-do-portao",
		"Veredito da ideia 3 — monitor": "veredito-da-ideia-3-monitor",
		"decisão com acentuação e ç":    "decisao-com-acentuacao-e-c",
	}
	for in, want := range tests {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

// The panel is an index of what to do next: the founder's decisions come
// first and hygiene last, never alphabetical order (spec FR2.4).
func TestSortOrdersByWhoIsBlocked(t *testing.T) {
	items := []Pendency{
		{Title: "arvore suja", Blocks: Hygiene, Source: "git"},
		{Title: "migracao", Blocks: AI, Source: "ject"},
		{Title: "veredito", Blocks: Founder, Source: "FOUNDER.md"},
		{Title: "portao esperando", Blocks: Gate, Source: "ject"},
	}
	Sort(items)

	want := []Blocker{Founder, Gate, AI, Hygiene}
	for i, w := range want {
		if items[i].Blocks != w {
			t.Fatalf("position %d = %q, want %q", i, items[i].Blocks, w)
		}
	}
}

func TestSortIsStableWithinAGroup(t *testing.T) {
	items := []Pendency{
		{Title: "zebra", Blocks: Founder, Source: "b.md"},
		{Title: "alfa", Blocks: Founder, Source: "a.md"},
	}
	Sort(items)
	if items[0].Source != "a.md" {
		t.Errorf("within a group the order = %q first, want a.md", items[0].Source)
	}
}

func TestLocationKeyIsStableAndDistinct(t *testing.T) {
	a := LocationKey("life.md", "§17.3")
	if a != LocationKey("life.md", "§17.3") {
		t.Error("LocationKey is not deterministic")
	}
	if a == LocationKey("life.md", "§17.4") {
		t.Error("two headings produced the same key")
	}
}
