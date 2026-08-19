package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture is one synthetic Go file plus what doclint must conclude about it.
//
// The fixtures are written to a temp directory instead of testdata because
// doclint walks the repository it lives in: a deliberately wrong doc comment
// stored as a .go file would fail the repository's own lint run.
type fixture struct {
	name string
	src  string
	want int
	says string
}

// accepted lists one fixture per declaration form where the doc comment sits on
// the symbol it names. A lint that rejects correct code is worse than no lint,
// so every form is tested in both directions.
var accepted = []fixture{
	{
		name: "func",
		src: `package fixture

// Alpha does the alpha thing.
func Alpha() {}

// Beta does the beta thing.
func Beta() {}
`,
	},
	{
		name: "method with receiver and parameter",
		src: `package fixture

// Counter counts.
type Counter struct{}

// Add adds n to the counter.
func (c *Counter) Add(n int) int { return n }
`,
	},
	{
		name: "type standalone",
		src: `package fixture

// Alpha is a thing.
type Alpha struct{}

// Beta is another thing.
type Beta struct{}
`,
	},
	{
		name: "var standalone",
		src: `package fixture

// Alpha holds the alpha value.
var Alpha = 1

// Beta holds the beta value.
var Beta = 2
`,
	},
	{
		name: "const standalone",
		src: `package fixture

// Alpha is the alpha limit.
const Alpha = 1

// Beta is the beta limit.
const Beta = 2
`,
	},
	{
		name: "ValueSpec inside const block",
		src: `package fixture

// Blocker says whose move it is.
type Blocker string

const (
	// Founder marks a decision only the founder can make.
	Founder Blocker = "founder"
	// Gate marks work waiting on a gate or another ticket.
	Gate Blocker = "gate"
)
`,
	},
	{
		name: "ValueSpec inside var block",
		src: `package fixture

var (
	// Alpha holds the alpha value.
	Alpha = 1
	// Beta holds the beta value.
	Beta = 2
)
`,
	},
	{
		name: "TypeSpec inside type block",
		src: `package fixture

type (
	// Alpha is a thing.
	Alpha struct{}
	// Beta is another thing.
	Beta struct{}
)
`,
	},
	{
		name: "block comment owns every name in the block",
		src: `package fixture

// Founder and Gate say whose move it is.
const (
	Founder = "founder"
	Gate    = "gate"
)
`,
	},
	{
		// The comment opens with a name the file really declares, so this
		// fixture only stays quiet while the empty-owner guard in docUnits is
		// there: an unaliased import owns no name, and without the guard every
		// first word would belong to somebody else.
		name: "documented import declares no owner of its own",
		src: `package fixture

import (
	// Trim is the standard string package.
	"strings"
)

// Trim trims s.
func Trim(s string) string { return strings.TrimSpace(s) }
`,
	},
	{
		name: "doc comment written with a colon after the name",
		src: `package fixture

// identity is what we could learn about the process behind a marker.
type identity int

const (
	// identityGone: no such process, or it is not ours to talk to.
	identityGone identity = iota
	// identityOurs: the process is the same program we are.
	identityOurs
)
`,
	},
}

// rejected lists one fixture per declaration form where a doc comment names a
// symbol other than the one it is attached to. Each must be reported exactly
// once, naming the symbol the comment really documents.
var rejected = []fixture{
	{
		name: "func",
		src: `package fixture

// Alpha does the alpha thing.
func Beta() {}

func Alpha() {}
`,
		want: 1,
		says: `"Alpha"`,
	},
	{
		name: "doc opens with a parameter name",
		src: `package fixture

// count is the number of items.
func Total(count int) int { return count }
`,
		want: 1,
		says: `"count"`,
	},
	{
		name: "type standalone",
		src: `package fixture

// Alpha is a thing.
type Beta struct{}

type Alpha struct{}
`,
		want: 1,
		says: `"Alpha"`,
	},
	{
		name: "var standalone",
		src: `package fixture

// Alpha holds the alpha value.
var Beta = 2

var Alpha = 1
`,
		want: 1,
		says: `"Alpha"`,
	},
	{
		name: "const standalone",
		src: `package fixture

// Alpha is the alpha limit.
const Beta = 2

const Alpha = 1
`,
		want: 1,
		says: `"Alpha"`,
	},
	{
		// The reviewer's own reproduction, transcribed from
		// internal/pendency/pendency.go: a member inserted between a comment
		// and the constant it documents. This is the regression the ticket
		// exists for -- it compiled, read wrong, and passed the lint.
		name: "ValueSpec inside const block",
		src: `package fixture

// Blocker says whose move it is.
type Blocker string

const (
	// Founder marks a decision only the founder can make.
	Investor Blocker = "investor"
	Founder  Blocker = "founder"
	// Gate marks work waiting on a gate or another ticket.
	Gate Blocker = "gate"
)
`,
		want: 1,
		says: `"Founder"`,
	},
	{
		name: "ValueSpec inside var block",
		src: `package fixture

var (
	// Alpha holds the alpha value.
	Beta = 2
	Alpha = 1
)
`,
		want: 1,
		says: `"Alpha"`,
	},
	{
		name: "TypeSpec inside type block",
		src: `package fixture

type (
	// Alpha is a thing.
	Beta  struct{}
	Alpha struct{}
)
`,
		want: 1,
		says: `"Alpha"`,
	},
	{
		// The style of internal/runtime/live.go:33 and :231. With the colon
		// attached, the first token matched nothing and every comment in those
		// blocks read as correct -- coverage that never fires.
		name: "doc comment written with a colon after the name",
		src: `package fixture

// identity is what we could learn about the process behind a marker.
type identity int

const (
	// identityGone: no such process, or it is not ours to talk to.
	identityAlive identity = iota
	identityGone
)
`,
		want: 1,
		says: `"identityGone"`,
	},
	{
		name: "aliased import owns the name it binds",
		src: `package fixture

import (
	// Trim is the standard string package.
	s "strings"
)

// Trim trims x.
func Trim(x string) string { return s.TrimSpace(x) }
`,
		want: 1,
		says: `"Trim"`,
	},
}

// writeFixture drops src into a fresh directory and returns the directory, so
// each case is checked in isolation from the others.
func writeFixture(t *testing.T, src string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "fixture.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return dir
}

// TestCheckAcceptsEveryDeclarationForm proves the lint stays quiet on correct
// code in every form a doc comment can be attached to.
func TestCheckAcceptsEveryDeclarationForm(t *testing.T) {
	for _, f := range accepted {
		t.Run(f.name, func(t *testing.T) {
			problems, err := check(writeFixture(t, f.src))
			if err != nil {
				t.Fatalf("check: %v", err)
			}
			if len(problems) != 0 {
				t.Fatalf("want no problem, got %d: %v", len(problems), problems)
			}
		})
	}
}

// TestCheckRejectsEveryDeclarationForm proves the lint catches a misplaced doc
// comment in every form -- grouped specs included, which it never examined.
func TestCheckRejectsEveryDeclarationForm(t *testing.T) {
	for _, f := range rejected {
		t.Run(f.name, func(t *testing.T) {
			problems, err := check(writeFixture(t, f.src))
			if err != nil {
				t.Fatalf("check: %v", err)
			}
			if len(problems) != f.want {
				t.Fatalf("want %d problem(s), got %d: %v", f.want, len(problems), problems)
			}
			if f.says != "" && !strings.Contains(problems[0], f.says) {
				t.Fatalf("problem %q does not name %s", problems[0], f.says)
			}
		})
	}
}

// TestRunExitsOneOnGroupedConstBlock is the acceptance criterion in the form
// commands.lint consumes it: a const(...) block whose comment documents the
// wrong symbol must make the program exit 1, not merely produce a string.
func TestRunExitsOneOnGroupedConstBlock(t *testing.T) {
	dir := writeFixture(t, `package fixture

const (
	// Alpha is the alpha limit.
	Beta  = 2
	Alpha = 1
)
`)
	var stderr bytes.Buffer
	if code := run([]string{dir}, &stderr); code != 1 {
		t.Fatalf("want exit code 1, got %d (stderr: %s)", code, stderr.String())
	}
	if got := stderr.String(); !strings.Contains(got, "1 doc comment(s) on the wrong symbol") {
		t.Fatalf("stderr does not report the count: %q", got)
	}
}

// TestRunExitsZeroWhenClean pins the other half of the exit contract: a clean
// tree must return 0 and say nothing.
func TestRunExitsZeroWhenClean(t *testing.T) {
	dir := writeFixture(t, `package fixture

const (
	// Alpha is the alpha limit.
	Alpha = 1
	// Beta is the beta limit.
	Beta = 2
)
`)
	var stderr bytes.Buffer
	if code := run([]string{dir}, &stderr); code != 0 {
		t.Fatalf("want exit code 0, got %d (stderr: %s)", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("want silence, got %q", stderr.String())
	}
}
