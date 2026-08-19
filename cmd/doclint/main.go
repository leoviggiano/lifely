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
			// Go's own convention is the whole rule: a doc comment OPENS with
			// the name of what it documents. A line that opens with a declared
			// name starts a doc block, and if that name is not this
			// declaration's, the block belongs to somebody else.
			owners := declaredNames(decl)
			for i, line := range doc.List {
				fields := strings.Fields(strings.TrimPrefix(line.Text, "// "))
				if len(fields) < 2 || !declared[fields[0]] || contains(owners, fields[0]) {
					continue
				}
				// A block OPENS at the first line or right after a blank
				// comment line; a mid-paragraph mention is ordinary prose.
				if i == 0 || strings.TrimSpace(doc.List[i-1].Text) == "//" {
					problems = append(problems, fmt.Sprintf("%s:%d: %v carries the doc comment of %q",
						path, fset.Position(decl.Pos()).Line, owners, fields[0]))
				}
			}
		}
		return nil
	})
	return problems, err
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
