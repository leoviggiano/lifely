// Command doclint reports a doc comment that sits on the wrong symbol, and a
// code-fence state machine written outside the package that owns it.
//
// Two checks, one program, because both are the same shape of defect: something
// the compiler, the test suite and a quick read all wave through. The first
// check is below; the second is fenceCopies, and it says why it exists there.
//
// It is a LINT, and it is a program because that is what a lint is. It began
// as a test, and the gate objected three times to the same thing: a _test.go
// asserting on the shape of source code is not a behaviour test, and putting
// it in `go test ./...` makes a green suite mean less. The objection was
// right, and the shape was the answer -- not the argument.
//
// Not a shell script either: this repo lost an hour to the gate's daemon not
// finding `go` on its PATH, and a lint that needs a second runtime is a lint
// that stops running. `go run ./cmd/doclint .` needs exactly what the build
// already needs.
//
// The class it kills: something slipped between a doc comment and the symbol
// it documents -- a function above a declaration, a member above the constant
// it names inside a const(...)/var(...)/type(...) block, or a field inserted
// above the struct field or interface method it describes (docUnits says how
// each is reached). Invisible to the compiler, to every test, and to a quick
// read -- five instances in one night, two from a merge and three from my own
// edits.
//
// What it does NOT reach, said plainly so the claim above stays honest:
// import declarations (an aliased import does bind a name, but reaching it
// means teaching the file-wide index about aliases too -- a surface of its
// own; docUnits says why it stays out), and declarations inside function
// bodies (only the file's top-level declarations are walked). A comment above
// `const (` itself IS examined, but it owns every name in the block at once --
// Go convention says it documents the GROUP -- so a member slipping in under
// it is not a misplacement here; it is still flagged when its first word names
// a symbol declared somewhere else in the file.
package main

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

// run holds the whole program so a test can assert on the exit code without
// spawning a process: the contract this lint sells to commands.lint is "0 when
// clean, 1 when it found something", and a contract nothing exercises is the
// same blind spot this file was fixed for.
func run(args []string, stderr io.Writer) int {
	root := "."
	if len(args) > 0 {
		root = args[0]
	}
	if root == "." {
		if found, err := moduleRoot(); err == nil {
			root = found
		}
	}
	problems, err := check(root)
	if err != nil {
		fmt.Fprintln(stderr, "doclint:", err)
		return 2
	}
	copies, err := fenceCopies(root)
	if err != nil {
		fmt.Fprintln(stderr, "doclint:", err)
		return 2
	}
	for _, p := range problems {
		fmt.Fprintln(stderr, p)
	}
	for _, c := range copies {
		fmt.Fprintln(stderr, c)
	}
	if len(problems) > 0 {
		fmt.Fprintf(stderr, "doclint: %d doc comment(s) on the wrong symbol\n", len(problems))
	}
	if len(copies) > 0 {
		fmt.Fprintf(stderr, "doclint: %d copied fence guard(s) outside %s\n", len(copies), parserPackage)
	}
	if len(problems) > 0 || len(copies) > 0 {
		return 1
	}
	return 0
}

// check walks root and reports every doc comment attached to a symbol other
// than the one it names.
func check(root string) ([]string, error) {
	var problems []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		fset := token.NewFileSet()
		file, perr := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if perr != nil {
			return nil // not our business: the compiler reports these
		}

		declared := map[string]bool{}
		for _, decl := range file.Decls {
			for _, name := range declaredNames(decl) {
				declared[name] = true
			}
		}

		for _, decl := range file.Decls {
			for _, unit := range docUnits(decl) {
				// ONLY the first line, because that is where Go's convention
				// actually binds: a doc comment OPENS with the name of what it
				// documents. Everything after is prose, and prose legitimately
				// names other symbols.
				//
				// The previous rule also flagged later paragraphs, which made
				// an ordinary cross-reference fail the build -- a lint that
				// rejects correct code is worse than no lint, and this one is
				// wired into commands.lint with no suppression directive. It
				// still catches every instance this program was written for:
				// all of them were blocks glued ABOVE a declaration, so the
				// first line named somebody else.
				if len(unit.doc.List) == 0 {
					continue
				}
				fields := strings.Fields(strings.TrimPrefix(unit.doc.List[0].Text, "// "))
				if len(fields) < 2 {
					continue
				}
				// A trailing colon is stripped because "// Name: prose" is a
				// style this repo actually uses -- two of the four grouped const
				// blocks are written that way. Left attached, the first token
				// matches no declared name and the check reads every one of
				// those comments as fine: coverage that exists and never fires
				// is worse than coverage that is absent, because it looks like a
				// guard.
				//
				// ONLY the colon. A trailing comma was stripped too for one
				// round, and it turned "// Gamma, Alpha and Beta are the modes."
				// into a build failure -- an enumeration is not a claim of
				// ownership, and this lint has no suppression directive.
				named := strings.TrimSuffix(fields[0], ":")
				if contains(unit.owners, named) {
					continue
				}
				if declared[named] || unit.local[named] {
					problems = append(problems, fmt.Sprintf("%s:%d: %v carries the doc comment of %q",
						path, fset.Position(unit.pos).Line, unit.owners, named))
				}
			}
		}
		return nil
	})
	return problems, err
}

// docUnit is one doc comment together with the names it is allowed to open
// with. A declaration can carry several: a parenthesised const/var/type block
// has its own comment plus one per spec inside it, and any type spec adds one
// per documented struct field or interface method.
type docUnit struct {
	doc    *ast.CommentGroup
	owners []string
	local  map[string]bool
	pos    token.Pos
}

// docUnits returns every doc comment a declaration carries.
//
// The blind spot it closes: reading only GenDecl.Doc means a comment attached
// to a single ValueSpec or TypeSpec inside const(...)/var(...)/type(...) is
// never examined -- so inserting a member between a comment and the constant it
// documents compiles, reads wrong, and passed this lint. Owners are per spec,
// not per block: the block's own names would let a comment name any sibling.
//
// Unparenthesised declarations carry the comment on the GenDecl only (go/parser
// leaves Spec.Doc nil there), so no unit is reported twice.
//
// A unit with NO owner is dropped, wherever it comes from: with an empty owner
// set every first word belongs to somebody else, so the comment above
// `import (` -- an import declaration declares no name -- would be reported as
// misplaced. That was already true before grouped specs were reached, and a
// lint that rejects correct code is worse than no lint.
//
// Struct fields and interface methods live in a container of their own
// (ast.Field, under a TypeSpec); fieldUnits says how they are reached and why
// their owners are per field.
//
// Imports themselves are deliberately NOT covered. An aliased import does bind
// a name, but reaching it means teaching the file-wide index about aliases too,
// and that is a second surface with its own failure modes -- outside the
// declaration forms this lint names. Tried once, measured at three gate rounds,
// and withdrawn; if it ever pays, it is a ticket of its own.
func docUnits(decl ast.Decl) []docUnit {
	switch d := decl.(type) {
	case *ast.FuncDecl:
		if d.Doc == nil {
			return nil
		}
		return []docUnit{{doc: d.Doc, owners: []string{d.Name.Name}, local: localNames(d), pos: d.Pos()}}
	case *ast.GenDecl:
		var out []docUnit
		if names := declaredNames(d); d.Doc != nil && len(names) > 0 {
			out = append(out, docUnit{doc: d.Doc, owners: names, pos: d.Pos()})
		}
		for _, spec := range d.Specs {
			doc, names := specDoc(spec)
			if doc != nil && len(names) > 0 {
				out = append(out, docUnit{doc: doc, owners: names, pos: spec.Pos()})
			}
			if ts, ok := spec.(*ast.TypeSpec); ok {
				out = append(out, fieldUnits(ts)...)
			}
		}
		return out
	}
	return nil
}

// fieldUnits returns one unit per documented struct field or interface method
// under a type spec -- the ast.Field container the rest of docUnits never sees.
// The blind spot it closes is the same class as the grouped specs: a field
// inserted between a comment and the field it documents compiles, reads wrong,
// and passed this lint (internal/pendency/pendency.go's Origin was the live
// surface that proved it).
//
// Owners are the names of THAT field, never the struct's: the container's
// names would let a comment claim any sibling. The siblings travel in the
// unit's LOCAL set instead of the file-wide index -- a field name is not a
// top-level symbol, and indexing it globally would change what the lint
// flags in the whole file, not just inside its own container. That keeps the
// insertion case visible (the displaced comment names a sibling) without
// widening anything else.
//
// An embedded field owns its IMPLICIT name -- the unqualified type name, which
// is how Go itself addresses it (`sync.Mutex` embeds as `Mutex`). Dropping it
// instead, as the first round did, left the ticket's own bug class reachable:
// an embedded field inserted between a comment and the field it documents
// swallowed the displaced comment and the lint stayed silent (gate finding
// `embedded-field-insertion-unreached`). A field whose name cannot be derived
// still produces no unit -- the empty-owner rule that keeps `import (`
// comments out, because with no owner every first word belongs to somebody
// else.
func fieldUnits(ts *ast.TypeSpec) []docUnit {
	var out []docUnit
	ast.Inspect(ts.Type, func(n ast.Node) bool {
		var list *ast.FieldList
		switch t := n.(type) {
		case *ast.StructType:
			list = t.Fields
		case *ast.InterfaceType:
			list = t.Methods
		default:
			return true
		}
		if list == nil {
			return true
		}
		siblings := map[string]bool{}
		for _, field := range list.List {
			for _, name := range fieldNames(field) {
				siblings[name] = true
			}
		}
		for _, field := range list.List {
			owners := fieldNames(field)
			if field.Doc == nil || len(owners) == 0 {
				continue
			}
			out = append(out, docUnit{doc: field.Doc, owners: owners, local: siblings, pos: field.Pos()})
		}
		return true
	})
	return out
}

// fieldNames returns the names a field answers to: its explicit names, or the
// implicit one an embedded field takes from its type.
func fieldNames(field *ast.Field) []string {
	if len(field.Names) > 0 {
		var out []string
		for _, name := range field.Names {
			out = append(out, name.Name)
		}
		return out
	}
	if name := embeddedName(field.Type); name != "" {
		return []string{name}
	}
	return nil
}

// embeddedName returns the implicit name of an embedded field: the unqualified
// type name, with any pointer or type-argument wrapping peeled off, exactly as
// the language derives it.
func embeddedName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return t.Sel.Name
	case *ast.StarExpr:
		return embeddedName(t.X)
	case *ast.IndexExpr:
		return embeddedName(t.X)
	case *ast.IndexListExpr:
		return embeddedName(t.X)
	}
	return ""
}

// specDoc returns the doc comment attached to a single spec and the names that
// spec declares. An import spec declares nothing this lint indexes, so it comes
// back with an empty set and docUnits drops it.
func specDoc(spec ast.Spec) (*ast.CommentGroup, []string) {
	switch s := spec.(type) {
	case *ast.TypeSpec:
		return s.Doc, []string{s.Name.Name}
	case *ast.ValueSpec:
		var names []string
		for _, n := range s.Names {
			names = append(names, n.Name)
		}
		return s.Doc, names
	}
	return nil, nil
}

// localNames returns the receiver and parameter names of a function, which a
// doc comment must never open with: Go's convention reserves that position for
// the name of the thing being documented.
func localNames(fn *ast.FuncDecl) map[string]bool {
	out := map[string]bool{}
	fields := []*ast.FieldList{fn.Type.Params, fn.Recv}
	for _, list := range fields {
		if list == nil {
			continue
		}
		for _, field := range list.List {
			for _, name := range field.Names {
				out[name.Name] = true
			}
		}
	}
	return out
}

// declaredNames returns every top-level name a declaration introduces, which is
// what the file-wide index of known symbols is built from. The spec->names
// extraction is specDoc's -- one copy of that rule, because this index decides
// whether a problem is reported at all, and a second copy is where the two
// would drift apart. Field names are deliberately NOT in here: they are local
// to their container, and fieldUnits carries them in the unit's local set.
func declaredNames(decl ast.Decl) []string {
	switch d := decl.(type) {
	case *ast.FuncDecl:
		return []string{d.Name.Name}
	case *ast.GenDecl:
		var out []string
		for _, spec := range d.Specs {
			_, names := specDoc(spec)
			out = append(out, names...)
		}
		return out
	}
	return nil
}

// parserPackage is the root of the one tree allowed to own a code-fence state
// machine -- the package itself and anything under it (ownsTheGuard says why
// the exemption covers the subtree and not a single directory).
// The path is relative to the root doclint was pointed at, so the exemption
// travels with the module rather than with anybody's working directory.
const parserPackage = "internal/md"

// fenceDelimiter is the markdown code-fence marker.
//
// Naming it here does NOT hide it from the check: since the const bypass was
// closed, a package-level name holding the delimiter counts exactly like an
// inline literal, this file included. What keeps this lint from reporting
// itself is the other half of the signature -- no function here flips a boolean
// onto itself, because none of them tracks fence state. The day one does, this
// check will have to answer for it, which is the point.
const fenceDelimiter = "```"

// fenceCopies reports every hand-written copy of the code-fence state machine
// living outside internal/md.
//
// This is the second check, and it exists because of what the first one cannot
// see: lifely-028 measured three scanners carrying their own copies of the same
// fence guard, and the copies diverged TWICE in two gate rounds -- a guard
// propagated by half recreates the bug it was written to kill. Consolidating
// them into internal/md fixes the instances; only a mechanical lock keeps the
// fourth copy from being written next month. It is a lint and not a _test.go by
// the lesson this repository already paid for: an assertion about the SHAPE of
// code is a lint, and the gate raised that same objection three times.
//
// The signature it looks for is the machine's two inseparable halves, in one
// function: the fence delimiter, and a boolean that flips on itself. Both, or
// nothing. Test fixtures across internal/scan hold fenced markdown as data and
// would be false positives under a rule that took the delimiter alone as
// proof; a bool that toggles for any other reason is nobody's fence.
// A lint that rejects correct code is worse than no lint, and this one is wired
// into commands.lint with no suppression directive.
// The delimiter counts whether it is written inline or hoisted to a package
// constant: reading only inline literals left the copy one refactor away from
// invisible, and hoisting the delimiter is precisely the idiom this file itself
// uses (gate finding `fence-lint-const-bypass`, run 01M0EET1HB). Package-level
// names are collected per DIRECTORY, because that is the scope a Go package
// spans -- the constant and the copy that uses it need not share a file.
func fenceCopies(root string) ([]string, error) {
	var files []parsedFile
	named := map[string]map[string]bool{}
	// The exemption is measured from the MODULE root, not from wherever the
	// walk was pointed. Measured against the walk root, `doclint internal`
	// turned the parser itself into a copy of the guard it owns -- exit 1 on
	// correct code, the one failure this lint cannot afford (gate finding
	// `doclint-exemption-root-relative`, run 01M0EG4DSK). Outside a module the
	// walk root is the only answer there is, and it stands in.
	base := root
	if found, err := moduleRootAbove(root); err == nil {
		base = found
	}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		if ownsTheGuard(base, path) {
			return nil
		}
		fset := token.NewFileSet()
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil // not our business: the compiler reports these
		}
		dir := filepath.Dir(path)
		files = append(files, parsedFile{path: path, dir: dir, fset: fset, file: file})
		for name := range delimiterNames(file) {
			if named[dir] == nil {
				named[dir] = map[string]bool{}
			}
			named[dir][name] = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var problems []string
	for _, pf := range files {
		for _, decl := range pf.file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			fence, flip := fenceMachine(fn.Body, named[pf.dir])
			if !flip.IsValid() {
				continue
			}
			problems = append(problems, fmt.Sprintf("%s:%d: %s toggles a fence state of its own (delimiter at line %d); %s owns that guard",
				pf.path, pf.fset.Position(flip).Line, fn.Name.Name, pf.fset.Position(fence).Line, parserPackage))
		}
	}
	return problems, nil
}

// parsedFile is one Go file kept between the two passes fenceCopies makes: the
// package-level delimiter names have to be known before any function body in
// that directory can be judged, and re-parsing to learn them twice would be the
// same file read twice for no reason.
type parsedFile struct {
	path string
	dir  string
	fset *token.FileSet
	file *ast.File
}

// delimiterNames returns the package-level constant and variable names in one
// file whose value carries the fence delimiter. A local declaration needs no
// entry here: its literal sits inside the function body, where fenceMachine
// already walks over it.
func delimiterNames(file *ast.File) map[string]bool {
	out := map[string]bool{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || (gen.Tok != token.CONST && gen.Tok != token.VAR) {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range value.Names {
				if i >= len(value.Values) {
					continue
				}
				lit, ok := value.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				if strings.Contains(literalValue(lit), fenceDelimiter) {
					out[name.Name] = true
				}
			}
		}
	}
	return out
}

// ownsTheGuard reports whether path lives in the parser package's tree, the one
// place the fence state machine is allowed to live. base is the module root, so
// the answer does not move with the directory the lint was pointed at.
//
// The exemption covers the SUBTREE, not one directory: splitting the machine
// into internal/md/fence is the natural move the day md.go grows, and an
// exemption pinned to a single level would report the owner of the guard as a
// copy of it. Reporting correct code is the failure this lint cannot afford --
// it runs in commands.lint with no suppression directive (gate finding
// `ownstheguard-exact-dir`, run 01M0EE3B7R).
func ownsTheGuard(base, path string) bool {
	// Both sides are made absolute first: base comes from the module root and
	// path from the walk, so one can be absolute while the other is not, and
	// filepath.Rel on that mix fails and silently reports the parser package
	// as a copy.
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(base, abs)
	if err != nil {
		return false
	}
	dir := filepath.ToSlash(filepath.Dir(rel))
	return dir == parserPackage || strings.HasPrefix(dir, parserPackage+"/")
}

// fenceMachine returns the positions of a fence state machine's two halves
// inside one function body: where the delimiter is named, and where a boolean
// flips on itself. Both positions come back invalid unless BOTH halves are
// present, because either one alone is ordinary code.
//
// The delimiter is named either by an inline literal or by one of the
// package-level names in delimiters, which is what closes the hoist-it-to-a-
// constant bypass.
func fenceMachine(body *ast.BlockStmt, delimiters map[string]bool) (fence, flip token.Pos) {
	ast.Inspect(body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.Ident:
			if !fence.IsValid() && delimiters[node.Name] {
				fence = node.Pos()
			}
		case *ast.BasicLit:
			if !fence.IsValid() && node.Kind == token.STRING && strings.Contains(literalValue(node), fenceDelimiter) {
				fence = node.Pos()
			}
		case *ast.AssignStmt:
			if !flip.IsValid() {
				if pos := selfNegation(node); pos.IsValid() {
					flip = pos
				}
			}
		}
		return true
	})
	if !fence.IsValid() || !flip.IsValid() {
		return token.NoPos, token.NoPos
	}
	return fence, flip
}

// literalValue returns what a string literal actually holds. An unquote failure
// falls back to the raw source text: a delimiter this lint cannot decode is
// still a delimiter, and missing it would be the silent half of the failure.
func literalValue(lit *ast.BasicLit) string {
	if unquoted, err := strconv.Unquote(lit.Value); err == nil {
		return unquoted
	}
	return lit.Value
}

// selfNegation returns the position of an assignment that flips a value onto
// itself (`x = !x`) -- the shape every copy of the fence machine wrote, because
// a fence alternates. Anything else comes back as no position.
//
// The two sides are compared as WRITTEN, not by node type. Requiring an
// identifier on the left let a machine that keeps its flag in a struct field
// (`s.inFence = !s.inFence`) walk straight past the check -- measured by the
// gate, which ran the built lint against both spellings of the same machine and
// got exit 0 and exit 1 (finding `fence-lint-field-bypass`, run 01M0EFH2F9).
// The defect there was tying the rule to the SHAPE of the lvalue instead of to
// the relation, so the fix answers for the class: field, nested field and
// indexed element are all one comparison.
func selfNegation(as *ast.AssignStmt) token.Pos {
	if as.Tok != token.ASSIGN || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
		return token.NoPos
	}
	unary, ok := as.Rhs[0].(*ast.UnaryExpr)
	if !ok || unary.Op != token.NOT {
		return token.NoPos
	}
	if types.ExprString(as.Lhs[0]) != types.ExprString(unary.X) {
		return token.NoPos
	}
	return as.Pos()
}

// contains reports whether needle is one of the names in haystack.
func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// moduleRoot walks up to the directory holding go.mod, so the check covers the
// whole repository rather than the package it happens to live in.
func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return moduleRootAbove(dir)
}

// moduleRootAbove walks up from dir to the directory holding go.mod. One copy
// of that walk, because two places now need it -- where the check starts and
// where the parser package's exemption is measured from -- and a second copy is
// where the two would drift apart.
func moduleRootAbove(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("no go.mod above the working directory")
		}
		dir = parent
	}
}
