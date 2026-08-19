package scan

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A doc comment must sit on the symbol it names.
//
// Inserting a function between a doc comment and its declaration is invisible
// to the compiler, to every other test, and to a quick read -- and the gate
// caught this exact defect FOUR times in one night, twice from a merge and
// twice from my own edits. A recurring mechanical error asks for a mechanical
// check, not for more care.
//
// Written in Go on purpose: it could have been a lint script, but that would
// put a new binary on the gate's PATH, and this repo lost an hour tonight to
// the daemon not finding `go`. The suite already runs everywhere.
func TestDocCommentsBelongToTheirSymbol(t *testing.T) {
	root := moduleRoot(t)

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
			// the name of what it documents. So a line that opens with a
			// declared name is the start of a doc block, and if that name is
			// not this declaration's, the block belongs to somebody else.
			//
			// The first version keyed on a hardcoded list of 34 verbs and let
			// "matches", "decides", "runs", "prints" and "strips" through --
			// an instrument that measured less than it claimed, which is the
			// defect it exists to catch, committed inside itself.
			owners := declaredNames(decl)
			for i, line := range doc.List {
				text := strings.TrimPrefix(line.Text, "// ")
				fields := strings.Fields(text)
				if len(fields) < 2 || !declared[fields[0]] || contains(owners, fields[0]) {
					continue
				}
				// A block OPENS at the first line, or right after a blank
				// comment line. Mid-paragraph mentions of another symbol are
				// ordinary prose and must not fire.
				opensBlock := i == 0 || strings.TrimSpace(doc.List[i-1].Text) == "//"
				if opensBlock {
					t.Errorf("%s: %v carries the doc comment of %q",
						filepath.Base(path), owners, fields[0])
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
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
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's working directory")
		}
		dir = parent
	}
}
