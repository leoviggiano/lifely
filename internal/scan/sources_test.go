package scan

import (
	"os"
	"path/filepath"
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
