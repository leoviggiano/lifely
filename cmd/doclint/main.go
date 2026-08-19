// Command doclint reports a doc comment that sits on the wrong symbol.
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
// The class it kills: anything slipped between a doc comment and what it
// documents -- a function above a declaration, or a member above the constant
// it names inside a const(...)/var(...)/type(...) block (docUnits says how
// both are reached). Invisible to the compiler, to every test, and to a quick
// read -- five instances in one night, two from a merge and three from my own
// edits.
package main

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
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
	for _, p := range problems {
		fmt.Fprintln(stderr, p)
	}
	if len(problems) > 0 {
		fmt.Fprintf(stderr, "doclint: %d doc comment(s) on the wrong symbol\n", len(problems))
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
				// The trailing punctuation is stripped because "// Name: prose"
				// is a style this repo actually uses -- two of the four grouped
				// const blocks are written that way. Left attached, the first
				// token matches no declared name and the check reads every one
				// of those comments as fine: coverage that exists and never
				// fires is worse than coverage that is absent, because it looks
				// like a guard.
				named := strings.TrimRight(fields[0], ":,")
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
// has its own comment plus one per spec inside it.
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
// Imports themselves are deliberately NOT covered. An aliased import does bind
// a name, but reaching it means teaching the file-wide index about aliases too,
// and that is a second surface with its own failure modes -- outside the
// declaration forms this ticket names. Recorded as a follow-up, not smuggled in.
func docUnits(decl ast.Decl) []docUnit {
	switch d := decl.(type) {
	case *ast.FuncDecl:
		if d.Doc == nil {
			return nil
		}
		return []docUnit{{doc: d.Doc, owners: []string{d.Name.Name}, local: localNames(d), pos: d.Pos()}}
	case *ast.GenDecl:
		var out []docUnit
		if d.Doc != nil && len(declaredNames(d)) > 0 {
			out = append(out, docUnit{doc: d.Doc, owners: declaredNames(d), pos: d.Pos()})
		}
		for _, spec := range d.Specs {
			doc, names := specDoc(spec)
			if doc == nil || len(names) == 0 {
				continue
			}
			out = append(out, docUnit{doc: doc, owners: names, pos: spec.Pos()})
		}
		return out
	}
	return nil
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
// what the file-wide index of known symbols is built from.
func declaredNames(decl ast.Decl) []string {
	switch d := decl.(type) {
	case *ast.FuncDecl:
		return []string{d.Name.Name}
	case *ast.GenDecl:
		var out []string
		for _, spec := range d.Specs {
			switch s := spec.(type) {
			case *ast.TypeSpec:
				out = append(out, s.Name.Name)
			case *ast.ValueSpec:
				for _, n := range s.Names {
					out = append(out, n.Name)
				}
			}
		}
		return out
	}
	return nil
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
