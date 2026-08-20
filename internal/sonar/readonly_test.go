package sonar

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This file is the ticket's negative acceptance criterion: "escrita no
// arquivo: inexistente por construcao -- nenhum caminho do lifely abre o log
// para escrita".
//
// It is proved twice, on purpose, because the two proofs fail in different
// ways. The behavioural test would still pass if a write path existed but
// happened not to run; the structural one would still pass if some other
// package learned to write the log. Together they say what the criterion
// says: not "it did not write", but "it cannot".

// writeCalls are the standard-library calls that can create, truncate or
// otherwise modify a file. os.Open and os.Stat are not here: they are the two
// this package is allowed.
//
// os.OpenFile is refused outright rather than inspected for O_RDONLY. A flag
// argument is a thing a later edit changes without changing the call, and a
// guard that has to reason about arithmetic on flags is a guard that will one
// day reason wrong.
var writeCalls = map[string][]string{
	"os": {
		"Create", "CreateTemp", "OpenFile", "WriteFile", "Remove", "RemoveAll",
		"Rename", "Truncate", "Chmod", "Chown", "Mkdir", "MkdirAll", "Link",
		"Symlink", "Append",
	},
	"io":     {"Copy", "CopyN", "WriteString"},
	"ioutil": {"WriteFile", "TempFile"},
}

func TestPackageHasNoWritePathByConstruction(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		// The guard is about the production package. A test file may write --
		// this one's sibling builds a fixture with os.WriteFile in a TempDir.
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) == 0 {
		t.Fatal("parsed no package; the guard would pass on an empty walk")
	}

	banned := map[string]bool{}
	for pkg, names := range writeCalls {
		for _, name := range names {
			banned[pkg+"."+name] = true
		}
	}

	found := 0
	for _, pkg := range pkgs {
		for path, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				ident, ok := sel.X.(*ast.Ident)
				if !ok {
					return true
				}
				qualified := ident.Name + "." + sel.Sel.Name
				if banned[qualified] {
					t.Errorf("%s calls %s: this package reads the tribunal's log and never writes it",
						filepath.Base(path), qualified)
				}
				// A method named Write on anything at all -- a *os.File, a
				// bufio.Writer wrapping one. The package has no legitimate
				// use for one, so the blanket refusal costs nothing and
				// catches the shape the qualified list cannot name.
				if strings.HasPrefix(sel.Sel.Name, "Write") {
					t.Errorf("%s calls %s: no Write of any kind belongs in a strict reader",
						filepath.Base(path), qualified)
				}
				found++
				return true
			})
		}
	}
	// A walk that inspected nothing would report clean. Pin that it looked.
	if found == 0 {
		t.Fatal("the walk found no calls at all; it is not reading the package")
	}
}

// TestReadDoesNotTouchTheFile is the behavioural half: read the same log many
// times and prove the bytes, the size, the mode and the modification time all
// came out the other side unchanged.
func TestReadDoesNotTouchTheFile(t *testing.T) {
	src, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "sonar.log")
	if err := os.WriteFile(path, src, 0o644); err != nil {
		t.Fatal(err)
	}
	// A modification time in the past: if anything wrote, even a zero-byte
	// open-for-append, the filesystem moves this to now.
	past := time.Now().Add(-90 * time.Minute)
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatal(err)
	}

	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 25; i++ {
		if feed := Read(path, 0, time.Now()); feed.Err != "" {
			t.Fatalf("read %d failed: %s", i, feed.Err)
		}
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("modification time moved from %v to %v", before.ModTime(), after.ModTime())
	}
	if after.Size() != before.Size() {
		t.Errorf("size moved from %d to %d", before.Size(), after.Size())
	}
	if after.Mode() != before.Mode() {
		t.Errorf("mode moved from %v to %v", before.Mode(), after.Mode())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(src) {
		t.Error("the log's contents changed")
	}
}

// A log the process cannot write at all still reads. This is the guard
// against a future edit that opens for read-write "just in case": on a
// read-only file that call fails, and the feed would go dark on exactly the
// machine where the tribunal's log is protected.
func TestReadWorksOnAReadOnlyFile(t *testing.T) {
	src, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "sonar.log")
	if err := os.WriteFile(path, src, 0o444); err != nil {
		t.Fatal(err)
	}
	feed := Read(path, 0, time.Now())
	if feed.Err != "" {
		t.Fatalf("Err = %q, want none: a read-only log is exactly what this reader is for", feed.Err)
	}
	if len(feed.Events) == 0 {
		t.Error("no events read from a read-only log")
	}
}
