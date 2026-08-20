package main

import (
	"bytes"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture is one synthetic Go file plus what doclint must conclude about it.
//
// The fixtures are written to a temp directory rather than to testdata. The
// original reason was that doclint walked testdata too, so a deliberately wrong
// doc comment stored there failed the repository's own lint run -- that stopped
// being true in lifely-033, which taught the walk to skip it. What keeps them
// here is the reason that outlived it: several tests assert on the SHAPE of the
// tree around a file -- which directory it sits in, where the module root is,
// what the walk was pointed at -- and a tree written per test can hold that,
// while one fixed testdata layout cannot.
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
		// there: an import declaration owns no name, and without the guard
		// every first word would belong to somebody else.
		name: "comment above an import block owns no name",
		src: `package fixture

// Trim needs the standard string package.
import (
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
	{
		// An enumeration is not a claim of ownership. Stripping the trailing
		// comma along with the colon turned this into a build failure for one
		// gate round.
		name: "block comment that enumerates names with commas",
		src: `package fixture

// Gamma is the third mode.
const Gamma = 3

// Gamma, Alpha and Beta are the modes.
const (
	Alpha = 1
	Beta  = 2
)
`,
	},
	{
		// Transcribed from internal/pendency/pendency.go: every field comment
		// opens with the name of its own field.
		name: "struct fields documented correctly",
		src: `package fixture

// Origin points back at where the pendency was read from.
type Origin struct {
	// Path is the file the item came from.
	Path string
	// Locator narrows it down inside the file.
	Locator string
	// Open is the command or URL that opens the source.
	Open string
}
`,
	},
	{
		name: "interface methods documented correctly",
		src: `package fixture

// Store keeps items.
type Store interface {
	// List returns every item.
	List() []string
	// Get returns one item by id.
	Get(id string) string
}
`,
	},
	{
		// An embedded field answers to its implicit name -- the unqualified
		// type name -- so an ordinary comment opening with it is the owner
		// speaking, not a misplacement.
		name: "comment on an embedded field owns the implicit name",
		src: `package fixture

// Base is the common part.
type Base struct{}

// Wrapper wraps Base.
type Wrapper struct {
	// Base carries the common part.
	Base
}
`,
	},
	{
		// The implicit name of a qualified embedded type is the bare type
		// name, exactly as Go derives it: sync.Mutex embeds as Mutex.
		name: "comment on a qualified embedded field",
		src: `package fixture

import "sync"

// Panel is the record repository panel.
type Panel struct {
	// Mutex guards the panel against concurrent reads.
	sync.Mutex
	// Root is the record repository.
	Root string
}
`,
	},
	{
		// The comment on a driver import names the type that needs the driver,
		// which is the whole point of writing it. No import spec is an owner.
		name: "comment on a blank import spec",
		src: `package fixture

import (
	// Server needs the strings package linked in.
	_ "strings"
)

// Server serves.
type Server struct{}
`,
	},
	{
		name: "comment on an aliased import spec",
		src: `package fixture

import (
	// Server needs the string helpers.
	sq "strings"
)

// Server serves.
type Server struct{}

// Trim trims s.
func Trim(s string) string { return sq.TrimSpace(s) }
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
		// The ticket's live surface, transcribed from
		// internal/pendency/pendency.go: a field inserted between a comment
		// and the struct field it documents. It compiled, read wrong, and
		// passed the lint -- exit 0 measured against the doclint of c34a9e6.
		name: "field inserted inside a struct",
		src: `package fixture

// Origin points back at where the pendency was read from.
type Origin struct {
	// Path is the file the item came from.
	Kind string
	Path string
	// Locator narrows it down inside the file.
	Locator string
}
`,
		want: 1,
		says: `"Path"`,
	},
	{
		// The gate's own trace (finding embedded-field-insertion-unreached):
		// an inserted EMBEDDED field swallows the displaced comment, because
		// round one dropped every field with no explicit name. The embedded
		// field's implicit name is the owner that keeps this reachable.
		name: "embedded field inserted inside a struct",
		src: `package fixture

import "sync"

// Panel is the record repository panel.
type Panel struct {
	// Root is the record repository.
	sync.Mutex
	Root string
}
`,
		want: 1,
		says: `"Root"`,
	},
	{
		// The reverse displacement: a NAMED field inserted between a comment
		// and the embedded field it documents. Only reported while the
		// embedded field's implicit name is in the sibling set -- the
		// mutation that dropped it from there survived the other fixtures.
		name: "field inserted above an embedded field",
		src: `package fixture

import "sync"

// Panel is the record repository panel.
type Panel struct {
	// Mutex guards the panel against concurrent reads.
	Root string
	sync.Mutex
}
`,
		want: 1,
		says: `"Mutex"`,
	},
	{
		name: "method inserted inside an interface",
		src: `package fixture

// Store keeps items.
type Store interface {
	// Get returns one item by id.
	List() []string
	Get(id string) string
}
`,
		want: 1,
		says: `"Get"`,
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

// TestRunExitsOneOnInsertedStructField is lifely-030's acceptance criterion in
// the form commands.lint consumes it: a field slipped between a comment and
// the struct field it documents must make the program exit 1, naming the
// field the comment really belongs to.
func TestRunExitsOneOnInsertedStructField(t *testing.T) {
	dir := writeFixture(t, `package fixture

// Origin points back at where the pendency was read from.
type Origin struct {
	// Path is the file the item came from.
	Kind string
	Path string
}
`)
	var stderr bytes.Buffer
	if code := run([]string{dir}, &stderr); code != 1 {
		t.Fatalf("want exit code 1, got %d (stderr: %s)", code, stderr.String())
	}
	if got := stderr.String(); !strings.Contains(got, `"Path"`) {
		t.Fatalf("stderr does not name the displaced field: %q", got)
	}
}

// TestFieldReachStopsAtTheTypeSpec measures the boundary the package doc
// claims: fieldUnits is called from *ast.TypeSpec alone, so the displacement
// TestRunExitsOneOnInsertedStructField catches under `type` -- the same source,
// the same comment, the same inserted sibling -- goes unreported when the
// struct is written anywhere else. This test exists so the doc's "measured,
// not assumed" stays measured; lifely-032 carries the coverage half, and when
// it lands this test is what says so.
//
// A test that asserts SILENCE needs a control, and this one needs two: `check`
// also says nothing about a file it cannot parse (parse errors are the
// compiler's business -- see the ParseFile branch in check), so a syntax slip
// inside a fixture string would make every case below pass while measuring
// nothing at all. So each source is parsed here before it is judged, and the
// same displacement written under a `type` is asserted to FIRE. What the cases
// then prove is silence produced by a live check, not silence from a dead one.
func TestFieldReachStopsAtTheTypeSpec(t *testing.T) {
	const control = `package fixture

// Origin points back at where the pendency was read from.
type Origin struct {
	// Path is the file the item came from.
	Kind string
	Path string
}
`
	problems, err := check(writeFixture(t, control))
	if err != nil {
		t.Fatalf("control: check: %v", err)
	}
	if len(problems) != 1 {
		t.Fatalf("control: the same displacement under `type` must be caught, or the cases below measure nothing; got %d: %v", len(problems), problems)
	}

	unreached := []fixture{
		{
			name: "anonymous struct given to a var",
			src: `package fixture

// Origin points back at where the pendency was read from.
var Origin = struct {
	// Path is the file the item came from.
	Kind string
	Path string
}{}
`,
		},
		{
			name: "anonymous struct spelled into a function signature",
			src: `package fixture

// Read hands back where the pendency was read from.
func Read() (out struct {
	// Path is the file the item came from.
	Kind string
	Path string
}) {
	return out
}
`,
		},
	}
	for _, f := range unreached {
		t.Run(f.name, func(t *testing.T) {
			if _, perr := parser.ParseFile(token.NewFileSet(), "fixture.go", f.src, parser.ParseComments); perr != nil {
				t.Fatalf("fixture does not parse, so doclint's silence about it measures nothing: %v", perr)
			}
			var stderr bytes.Buffer
			if code := run([]string{writeFixture(t, f.src)}, &stderr); code != 0 {
				t.Fatalf("the doc calls this reach a gap, but doclint exited %d: %s", code, stderr.String())
			}
			if stderr.Len() != 0 {
				t.Fatalf("want silence, got %q", stderr.String())
			}
		})
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

// writeTree drops a whole set of files, each at its own path under a fresh
// directory, and returns that directory. The fence check reads the path a file
// sits at -- internal/md is exempt and nothing else is -- so its fixtures need
// directories, which writeFixture's single flat file cannot express.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, src := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", name, err)
		}
		if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return root
}

// fenceMachineSource is the guard itself, in the shape all three scanners wrote
// it before internal/md existed: the delimiter tested, the state flipped.
const fenceMachineSource = "package fixture\n\n" +
	"import \"strings\"\n\n" +
	"// scan walks lines and skips the fenced ones.\n" +
	"func scan(lines []string) int {\n" +
	"\tinFence := false\n" +
	"\tn := 0\n" +
	"\tfor _, line := range lines {\n" +
	"\t\tif strings.HasPrefix(line, \"```\") {\n" +
	"\t\t\tinFence = !inFence\n" +
	"\t\t\tcontinue\n" +
	"\t\t}\n" +
	"\t\tif inFence {\n" +
	"\t\t\tcontinue\n" +
	"\t\t}\n" +
	"\t\tn++\n" +
	"\t}\n" +
	"\treturn n\n" +
	"}\n"

// TestFenceCopiesAcceptsTheParserItself proves the exemption is real: the same
// machine, inside internal/md, is the one copy that must exist.
func TestFenceCopiesAcceptsTheParserItself(t *testing.T) {
	root := writeTree(t, map[string]string{"internal/md/md.go": fenceMachineSource})
	copies, err := fenceCopies(root)
	if err != nil {
		t.Fatalf("fenceCopies: %v", err)
	}
	if len(copies) != 0 {
		t.Fatalf("want no copy reported inside the parser package, got %v", copies)
	}
}

// TestFenceCopiesRejectsACopyOutsideTheParser is AC009 in its direct form: the
// fourth hand-written copy of the fence guard is refused, naming the function
// that holds it.
func TestFenceCopiesRejectsACopyOutsideTheParser(t *testing.T) {
	root := writeTree(t, map[string]string{"internal/scan/tribunal.go": fenceMachineSource})
	copies, err := fenceCopies(root)
	if err != nil {
		t.Fatalf("fenceCopies: %v", err)
	}
	if len(copies) != 1 {
		t.Fatalf("want exactly 1 copy reported, got %d: %v", len(copies), copies)
	}
	if !strings.Contains(copies[0], "scan toggles a fence state of its own") {
		t.Fatalf("report does not name the function holding the copy: %q", copies[0])
	}
}

// TestFenceCopiesIgnoresFencedTestData holds the false-positive line that
// decided the rule's shape: internal/scan's tests carry fenced markdown as
// fixture data, and data is not a guard.
func TestFenceCopiesIgnoresFencedTestData(t *testing.T) {
	src := "package fixture\n\n" +
		"// fixtureBody is a decision body with a fenced example inside it.\n" +
		"func fixtureBody() string {\n" +
		"\treturn \"## D1\\n\\n```markdown\\n## D9 exemplo\\n```\\n\"\n" +
		"}\n"
	root := writeTree(t, map[string]string{"internal/scan/ject_test.go": src})
	copies, err := fenceCopies(root)
	if err != nil {
		t.Fatalf("fenceCopies: %v", err)
	}
	if len(copies) != 0 {
		t.Fatalf("want fenced test data left alone, got %v", copies)
	}
}

// TestFenceCopiesIgnoresAnUnrelatedToggle holds the other false-positive line:
// a boolean that alternates for reasons of its own is nobody's fence.
func TestFenceCopiesIgnoresAnUnrelatedToggle(t *testing.T) {
	src := "package fixture\n\n" +
		"// blink alternates a lamp.\n" +
		"func blink(times int) bool {\n" +
		"\ton := false\n" +
		"\tfor i := 0; i < times; i++ {\n" +
		"\t\ton = !on\n" +
		"\t}\n" +
		"\treturn on\n" +
		"}\n"
	root := writeTree(t, map[string]string{"internal/scan/blink.go": src})
	copies, err := fenceCopies(root)
	if err != nil {
		t.Fatalf("fenceCopies: %v", err)
	}
	if len(copies) != 0 {
		t.Fatalf("want an unrelated toggle left alone, got %v", copies)
	}
}

// TestRunExitsOneOnCopiedFenceGuard is AC009 in the form commands.lint consumes
// it: a copied guard must make the program exit 1 and say which check failed.
func TestRunExitsOneOnCopiedFenceGuard(t *testing.T) {
	root := writeTree(t, map[string]string{"internal/scan/tribunal.go": fenceMachineSource})
	var stderr bytes.Buffer
	if code := run([]string{root}, &stderr); code != 1 {
		t.Fatalf("want exit code 1, got %d (stderr: %s)", code, stderr.String())
	}
	if got := stderr.String(); !strings.Contains(got, "1 copied fence guard(s) outside internal/md") {
		t.Fatalf("stderr does not report the fence count: %q", got)
	}
}

// TestFenceCopiesAcceptsASubpackageOfTheParser holds the exemption's shape: it
// covers the parser's whole tree, because splitting md.go into internal/md/fence
// must not turn the owner of the guard into a copy of it.
func TestFenceCopiesAcceptsASubpackageOfTheParser(t *testing.T) {
	root := writeTree(t, map[string]string{"internal/md/fence/fence.go": fenceMachineSource})
	copies, err := fenceCopies(root)
	if err != nil {
		t.Fatalf("fenceCopies: %v", err)
	}
	if len(copies) != 0 {
		t.Fatalf("want no copy reported inside the parser's tree, got %v", copies)
	}
}

// TestFenceCopiesRejectsALookalikeNeighbour pins the other edge of the same
// rule: a package whose path merely STARTS with the parser's name is not the
// parser and gets no exemption.
func TestFenceCopiesRejectsALookalikeNeighbour(t *testing.T) {
	root := writeTree(t, map[string]string{"internal/mdx/scan.go": fenceMachineSource})
	copies, err := fenceCopies(root)
	if err != nil {
		t.Fatalf("fenceCopies: %v", err)
	}
	if len(copies) != 1 {
		t.Fatalf("want the lookalike package reported, got %d: %v", len(copies), copies)
	}
}

// hoistedFenceMachineSource is the copy one refactor away from invisible: the
// delimiter lives in a package-level constant instead of inline.
const hoistedFenceMachineSource = "package fixture\n\n" +
	"import \"strings\"\n\n" +
	"// fence is the markdown code fence.\n" +
	"const fence = \"```\"\n\n" +
	"// scan walks lines and skips the fenced ones.\n" +
	"func scan(lines []string) int {\n" +
	"\tinFence := false\n" +
	"\tn := 0\n" +
	"\tfor _, line := range lines {\n" +
	"\t\tif strings.HasPrefix(line, fence) {\n" +
	"\t\t\tinFence = !inFence\n" +
	"\t\t\tcontinue\n" +
	"\t\t}\n" +
	"\t\tif inFence {\n" +
	"\t\t\tcontinue\n" +
	"\t\t}\n" +
	"\t\tn++\n" +
	"\t}\n" +
	"\treturn n\n" +
	"}\n"

// TestFenceCopiesRejectsAHoistedDelimiter closes the bypass the gate measured:
// hoisting the delimiter to a package constant must not buy invisibility.
func TestFenceCopiesRejectsAHoistedDelimiter(t *testing.T) {
	root := writeTree(t, map[string]string{"internal/scan/tribunal.go": hoistedFenceMachineSource})
	copies, err := fenceCopies(root)
	if err != nil {
		t.Fatalf("fenceCopies: %v", err)
	}
	if len(copies) != 1 {
		t.Fatalf("want the hoisted copy reported, got %d: %v", len(copies), copies)
	}
}

// TestFenceCopiesRejectsADelimiterHoistedToAnotherFile pins the scope of the
// name lookup: a Go package spans a directory, so the constant and the copy
// using it need not share a file.
func TestFenceCopiesRejectsADelimiterHoistedToAnotherFile(t *testing.T) {
	root := writeTree(t, map[string]string{
		"internal/scan/fence.go": "package fixture\n\n// fence is the markdown code fence.\nconst fence = \"```\"\n",
		"internal/scan/tribunal.go": "package fixture\n\n" +
			"import \"strings\"\n\n" +
			"// scan walks lines and skips the fenced ones.\n" +
			"func scan(lines []string) int {\n" +
			"\tinFence := false\n" +
			"\tn := 0\n" +
			"\tfor _, line := range lines {\n" +
			"\t\tif strings.HasPrefix(line, fence) {\n" +
			"\t\t\tinFence = !inFence\n" +
			"\t\t\tcontinue\n" +
			"\t\t}\n" +
			"\t\tn++\n" +
			"\t}\n" +
			"\treturn n\n" +
			"}\n",
	})
	copies, err := fenceCopies(root)
	if err != nil {
		t.Fatalf("fenceCopies: %v", err)
	}
	if len(copies) != 1 {
		t.Fatalf("want the cross-file hoisted copy reported, got %d: %v", len(copies), copies)
	}
}

// TestFenceCopiesIgnoresAnUnrelatedConstant holds the false-positive line for
// the name lookup: a package constant that is not a fence delimiter lends its
// name to nothing.
func TestFenceCopiesIgnoresAnUnrelatedConstant(t *testing.T) {
	src := "package fixture\n\n" +
		"// bullet is the list marker.\n" +
		"const bullet = \"- \"\n\n" +
		"// blink alternates a lamp.\n" +
		"func blink(times int) bool {\n" +
		"\ton := false\n" +
		"\tfor i := 0; i < times; i++ {\n" +
		"\t\t_ = bullet\n" +
		"\t\ton = !on\n" +
		"\t}\n" +
		"\treturn on\n" +
		"}\n"
	root := writeTree(t, map[string]string{"internal/scan/blink.go": src})
	copies, err := fenceCopies(root)
	if err != nil {
		t.Fatalf("fenceCopies: %v", err)
	}
	if len(copies) != 0 {
		t.Fatalf("want an unrelated constant left alone, got %v", copies)
	}
}

// TestFenceCopiesRejectsAFieldHeldFlag closes the bypass the gate measured by
// running the lint: a machine that keeps its state in a struct field is the
// same machine.
func TestFenceCopiesRejectsAFieldHeldFlag(t *testing.T) {
	src := "package fixture\n\n" +
		"import \"strings\"\n\n" +
		"// mdScanner walks a markdown file.\n" +
		"type mdScanner struct {\n" +
		"\tinFence bool\n" +
		"}\n\n" +
		"// scan walks lines and skips the fenced ones.\n" +
		"func (s *mdScanner) scan(lines []string) int {\n" +
		"\tn := 0\n" +
		"\tfor _, line := range lines {\n" +
		"\t\tif strings.HasPrefix(strings.TrimSpace(line), \"```\") {\n" +
		"\t\t\ts.inFence = !s.inFence\n" +
		"\t\t\tcontinue\n" +
		"\t\t}\n" +
		"\t\tif s.inFence {\n" +
		"\t\t\tcontinue\n" +
		"\t\t}\n" +
		"\t\tn++\n" +
		"\t}\n" +
		"\treturn n\n" +
		"}\n"
	root := writeTree(t, map[string]string{"internal/scan/x.go": src})
	copies, err := fenceCopies(root)
	if err != nil {
		t.Fatalf("fenceCopies: %v", err)
	}
	if len(copies) != 1 {
		t.Fatalf("want the field-held machine reported, got %d: %v", len(copies), copies)
	}
}

// TestFenceCopiesIgnoresANegationOfSomethingElse pins the other edge: negating
// a DIFFERENT value is an ordinary assignment, not a toggle.
func TestFenceCopiesIgnoresANegationOfSomethingElse(t *testing.T) {
	src := "package fixture\n\n" +
		"import \"strings\"\n\n" +
		"// classify says whether a line opens a fence and whether it is plain.\n" +
		"func classify(lines []string) bool {\n" +
		"\tfenced := false\n" +
		"\tplain := false\n" +
		"\tfor _, line := range lines {\n" +
		"\t\tif strings.HasPrefix(line, \"```\") {\n" +
		"\t\t\tfenced = true\n" +
		"\t\t}\n" +
		"\t\tplain = !fenced\n" +
		"\t}\n" +
		"\treturn plain\n" +
		"}\n"
	root := writeTree(t, map[string]string{"internal/scan/classify.go": src})
	copies, err := fenceCopies(root)
	if err != nil {
		t.Fatalf("fenceCopies: %v", err)
	}
	if len(copies) != 0 {
		t.Fatalf("want a negation of another value left alone, got %v", copies)
	}
}

// TestFenceCopiesExemptionIsMeasuredFromTheModuleRoot pins where the exemption
// is anchored: pointing the lint below the module root must not turn the parser
// package into a copy of the guard it owns.
func TestFenceCopiesExemptionIsMeasuredFromTheModuleRoot(t *testing.T) {
	root := writeTree(t, map[string]string{
		"go.mod":            "module example.com/fixture\n\ngo 1.26\n",
		"internal/md/md.go": fenceMachineSource,
	})
	copies, err := fenceCopies(filepath.Join(root, "internal"))
	if err != nil {
		t.Fatalf("fenceCopies: %v", err)
	}
	if len(copies) != 0 {
		t.Fatalf("want the parser exempt whatever the walk root is, got %v", copies)
	}
}

// TestChecksSkipDirectoriesTheGoToolIgnores holds the walk's boundary: neither
// check reads source the compiler cannot see. A nested git worktree under
// .claude/ made `go run ./cmd/doclint .` report three fence guards in files
// `go list ./...` does not even list -- the lint red on the developer's tree
// while the gate stayed green, over code the build ignores.
func TestChecksSkipDirectoriesTheGoToolIgnores(t *testing.T) {
	root := writeTree(t, map[string]string{
		".claude/worktrees/old/scan.go": fenceMachineSource,
		"_scratch/scan.go":              fenceMachineSource,
		"internal/scan/clean.go":        "package fixture\n\n// Alpha does the alpha thing.\nfunc Alpha() {}\n",
	})
	copies, err := fenceCopies(root)
	if err != nil {
		t.Fatalf("fenceCopies: %v", err)
	}
	if len(copies) != 0 {
		t.Fatalf("the walk read source the Go tool ignores: %v", copies)
	}
	problems, err := check(root)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(problems) != 0 {
		t.Fatalf("the doc check read source the Go tool ignores: %v", problems)
	}
}

// TestChecksStillReadOrdinaryDirectories is the other direction: the skip must
// cost nothing outside the names the Go tool itself ignores.
func TestChecksStillReadOrdinaryDirectories(t *testing.T) {
	root := writeTree(t, map[string]string{"internal/scan/tribunal.go": fenceMachineSource})
	copies, err := fenceCopies(root)
	if err != nil {
		t.Fatalf("fenceCopies: %v", err)
	}
	if len(copies) != 1 {
		t.Fatalf("want the ordinary directory still read, got %d: %v", len(copies), copies)
	}
}

// TestCheckReadsADotDirectoryPointedAtOnPurpose pins the exception: skipping
// happens while WALKING PAST, never to the root the caller aimed at.
func TestCheckReadsADotDirectoryPointedAtOnPurpose(t *testing.T) {
	root := writeTree(t, map[string]string{".claude/worktrees/old/scan.go": fenceMachineSource})
	// The root aimed at is itself a dot-directory: that is what exercises the
	// exception. Aiming one level deeper (".claude/worktrees/old") does NOT --
	// the walk's first entry is named "old", which no rule would skip, and a
	// mutation removing the exception survived that spelling.
	copies, err := fenceCopies(filepath.Join(root, ".claude"))
	if err != nil {
		t.Fatalf("fenceCopies: %v", err)
	}
	if len(copies) != 1 {
		t.Fatalf("want the directory aimed at to be read, got %d: %v", len(copies), copies)
	}
}

// TestFenceCopiesSkipsTestdataAndVendor covers the two names the go tool
// ignores by NAME rather than by prefix. `go help packages` states both: a
// directory named testdata is ignored, and since Go 1.9 the ... pattern never
// matches inside vendor. The prefix rule alone let a fixture holding the fence
// machine be reported in a directory `go build ./...` does not compile --
// measured on this tree before the fix, and the reason main_test.go writes its
// own fixtures to a temp dir.
//
// The control is in the SAME tree, not a separate test: an ordinary directory
// holding the identical source must still be reported. A silence assertion
// whose instrument is dead passes for the wrong reason.
func TestFenceCopiesSkipsTestdataAndVendor(t *testing.T) {
	root := writeTree(t, map[string]string{
		"internal/scan/testdata/fence_fixture.go": fenceMachineSource,
		"vendor/example.com/dep/scan.go":          fenceMachineSource,
		"internal/scan/tribunal.go":               fenceMachineSource,
	})
	copies, err := fenceCopies(root)
	if err != nil {
		t.Fatalf("fenceCopies: %v", err)
	}
	if len(copies) != 1 {
		t.Fatalf("want only the ordinary directory reported, got %d: %v", len(copies), copies)
	}
	if want := filepath.Join("internal", "scan", "tribunal.go"); !strings.Contains(copies[0], want) {
		t.Fatalf("want the control %s reported, got %v", want, copies[0])
	}
}

// TestCheckSkipsTestdataAndVendor is the same boundary for the other check.
// The two share skipIgnoredDir, and this is what proves the sharing is real
// rather than a copy that drifted: the doc check reads with a different parse
// mode and reports a different defect.
func TestCheckSkipsTestdataAndVendor(t *testing.T) {
	root := writeTree(t, map[string]string{
		"internal/scan/testdata/displaced.go": displacedDocSource,
		"vendor/example.com/dep/displaced.go": displacedDocSource,
		"internal/scan/displaced.go":          displacedDocSource,
	})
	problems, err := check(root)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(problems) != 1 {
		t.Fatalf("want only the ordinary directory reported, got %d: %v", len(problems), problems)
	}
	if want := filepath.Join("internal", "scan", "displaced.go"); !strings.Contains(problems[0], want) {
		t.Fatalf("want the control %s reported, got %v", want, problems[0])
	}
}

// TestCheckReadsATestdataDirectoryPointedAtOnPurpose carries the root exception
// to the names added here: aiming the lint AT testdata is a request, the same
// way aiming it at a dot-directory is.
func TestCheckReadsATestdataDirectoryPointedAtOnPurpose(t *testing.T) {
	root := writeTree(t, map[string]string{"testdata/scan.go": fenceMachineSource})
	copies, err := fenceCopies(filepath.Join(root, "testdata"))
	if err != nil {
		t.Fatalf("want the directory aimed at to be read: %v", err)
	}
	if len(copies) != 1 {
		t.Fatalf("want the directory aimed at to be read, got %d: %v", len(copies), copies)
	}
}

// displacedDocSource is one doc comment on the wrong symbol -- the defect the
// first check exists for -- so a silence assertion about check has something
// real to be silent about.
const displacedDocSource = "package fixture\n\n" +
	"// Alpha does the alpha thing.\n" +
	"func Beta() {}\n\n" +
	"func Alpha() {}\n"
