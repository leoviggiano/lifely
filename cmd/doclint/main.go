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
// The class it kills: inserting a function between a doc comment and its
// declaration. Invisible to the compiler, to every test, and to a quick read
// -- five instances in one night, two from a merge and three from my own
// edits.
package main

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	if root == "." {
		if found, err := moduleRoot(); err == nil {
			root = found
		}
	}
	problems, err := check(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "doclint:", err)
		os.Exit(2)
	}
	for _, p := range problems {
		fmt.Fprintln(os.Stderr, p)
	}
	if len(problems) > 0 {
		fmt.Fprintf(os.Stderr, "doclint: %d doc comment(s) on the wrong symbol\n", len(problems))
		os.Exit(1)
	}
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
			doc := declDoc(decl)
			if doc == nil {
				continue
			}
			// ONLY the first line, because that is where Go's convention
			// actually binds: a doc comment OPENS with the name of what it
			// documents. Everything after is prose, and prose legitimately
			// names other symbols.
			//
			// The previous rule also flagged later paragraphs, which made an
			// ordinary cross-reference fail the build -- a lint that rejects
			// correct code is worse than no lint, and this one is wired into
			// commands.lint with no suppression directive. It still catches
			// every instance this program was written for: all of them were
			// blocks glued ABOVE a declaration, so the first line named
			// somebody else.
			owners := declaredNames(decl)
			local := localNames(decl)
			if len(doc.List) == 0 {
				continue
			}
			fields := strings.Fields(strings.TrimPrefix(doc.List[0].Text, "// "))
			if len(fields) < 2 || contains(owners, fields[0]) {
				continue
			}
			if declared[fields[0]] || local[fields[0]] {
				problems = append(problems, fmt.Sprintf("%s:%d: %v carries the doc comment of %q",
					path, fset.Position(decl.Pos()).Line, owners, fields[0]))
			}
		}
		return nil
	})
	return problems, err
}

// localNames returns the receiver and parameter names of a function, which a
// doc comment must never open with: Go's convention reserves that position for
// the name of the thing being documented.
func localNames(decl ast.Decl) map[string]bool {
	out := map[string]bool{}
	fn, ok := decl.(*ast.FuncDecl)
	if !ok {
		return out
	}
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

func declDoc(decl ast.Decl) *ast.CommentGroup {
	switch d := decl.(type) {
	case *ast.FuncDecl:
		return d.Doc
	case *ast.GenDecl:
		return d.Doc
	}
	return nil
}

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
