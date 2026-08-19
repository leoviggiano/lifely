package scan

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A sweep that finds nothing must still report the sources it could not read.
//
// This is the invariant behind the empty screen: "nada pendente" has to mean
// "we looked and there is nothing", never "we could not look". The panel's
// zero state is exactly where nobody goes hunting for a failure, so a silent
// source there is worse than anywhere else (NFR6).
func TestZeroPendenciesStillReportsUnreadableSources(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not block reads")
	}
	root := t.TempDir()
	blocked := filepath.Join(root, "FOUNDER.md")
	if err := os.WriteFile(blocked, []byte("## Faixa 1\n\n- [x] ~~feito~~\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(blocked, 0o000); err != nil {
		t.Skipf("cannot make a file unreadable here: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o644) })

	res := Tribunal(root)
	if len(res.Pendencies) != 0 {
		t.Fatalf("got %d pendencies, want 0 for this fixture", len(res.Pendencies))
	}

	var reported bool
	for _, s := range res.Sources {
		if s.Name == "FOUNDER.md" && s.Err != "" {
			reported = true
		}
	}
	if !reported {
		t.Error("a zero-pendency sweep hid an unreadable source")
	}
}

// A directory that cannot be walked is a finding too: `ledgers` is the only
// sweep that discovers files, so nothing else would ever report it.
func TestUnwalkableDirectoryIsReported(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not block the walk")
	}
	root := t.TempDir()
	locked := filepath.Join(root, "medicao")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "fila.tsv"), []byte("# id\tstatus\nA\tpendente\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Skipf("cannot make a directory unreadable here: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	var reported string
	for _, s := range Tribunal(root).Sources {
		if s.Name == "ledgers *.tsv" {
			reported = s.Err
		}
	}
	if reported == "" {
		t.Error("a directory that could not be walked was dropped without a trace")
	}
}

// A root that is not a git repository has no tree to be dirty. That is normal
// absence -- the same call latestSummary makes for a missing summary -- and
// calling it ILEGIVEL trains the reader to ignore the marker that should
// always mean something.
func TestNonRepositoryRootIsNotAFinding(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "FOUNDER.md"), []byte("## Faixa 1\n\n- [ ] **algo**\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, s := range Tribunal(root).Sources {
		if s.Name == "git status" && s.Err != "" {
			t.Errorf("a non-repository root was reported as unreadable: %q", s.Err)
		}
	}
}

// A ledger that breaks partway through still told us about everything above
// the break. Returning nil on a scanner error undid the preservation this
// scanner does deliberately -- one bad line would hide every open decision
// above it.
func TestLedgerKeepsRowsReadBeforeAFailure(t *testing.T) {
	root := t.TempDir()
	// A very long line blows the scanner's token limit partway through.
	huge := strings.Repeat("x", 5*1024*1024)
	body := "# id\ttitulo\tstatus\n" +
		"A\tprimeira\tpendente\n" +
		"B\tsegunda\tpendente\n" +
		"C\t" + huge + "\tpendente\n"
	if err := os.WriteFile(filepath.Join(root, "fila.tsv"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	res := Tribunal(root)
	var kept int
	for _, p := range res.Pendencies {
		if p.Class == "A2" {
			kept++
		}
	}
	if kept < 2 {
		t.Errorf("kept %d rows read before the failure, want at least 2", kept)
	}

	var reported bool
	for _, s := range res.Sources {
		if s.Name == "ledgers *.tsv" && s.Err != "" {
			reported = true
		}
	}
	if !reported {
		t.Error("the scanner failure was not reported as a finding")
	}
}

// `git -C` resolves upward. A root that is not a repository but sits inside
// one must not report the PARENT's dirty tree as if it were this source's --
// which is what the lifely repo itself would do to any scratch directory
// under it.
func TestDirtyTreeDoesNotClimbToAnAncestorRepository(t *testing.T) {
	outer := t.TempDir()
	if err := exec.Command("git", "-C", outer, "init", "-q").Run(); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outer, "sujo.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	inner := filepath.Join(outer, "sem-repo")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inner, "FOUNDER.md"), []byte("## Faixa 1\n\n- [ ] **algo**\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, p := range Tribunal(inner).Pendencies {
		if p.Class == "A5" {
			t.Errorf("reported the ancestor repository's dirty tree: %q", p.Detail)
		}
	}
}
