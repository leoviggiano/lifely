package runtime

import (
	"os"
	"os/exec"
	"testing"
)

func TestLive(t *testing.T) {
	// Our own pid is, by construction, the same program we are.
	if !(Marker{PID: os.Getpid()}).Live() {
		t.Error("Live() on our own pid = false, want true")
	}
	if (Marker{PID: 0}).Live() {
		t.Error("Live() on pid 0 = true, want false")
	}

	// A process that has exited is not live -- and its marker must not keep
	// `serve` from starting.
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting a throwaway process: %v", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatalf("waiting on the throwaway process: %v", err)
	}
	if (Marker{PID: pid}).Live() {
		t.Errorf("Live() on the exited pid %d = true, want false", pid)
	}
}

// A marker left behind by a crash must not be reported as a running daemon,
// and must be cleared so the next `serve` can start.
func TestRunningClearsStaleMarker(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting a throwaway process: %v", err)
	}
	dead := cmd.Process.Pid
	_ = cmd.Wait()

	if err := Write(Marker{PID: dead, Port: 7777, Owner: OwnerTribunal}); err != nil {
		t.Fatalf("Write() = %v", err)
	}
	if _, ok := Running(); ok {
		t.Fatal("Running() with a stale marker = true, want false")
	}
	if _, err := Read(); err != ErrNoMarker {
		t.Errorf("the stale marker survived Running(): Read() = %v, want ErrNoMarker", err)
	}
}

// stranger starts a live, same-user process that is definitely not lifely.
//
// pid 1 does NOT exercise the identity check: on macOS Signal(0) to launchd
// returns EPERM, so the probe stops at the permission branch and never asks
// what the process is. A same-user child is the only way to reach the identity
// guard deterministically -- which is the whole point of the guard.
func stranger(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start a helper process here: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return cmd.Process.Pid
}

// The OS recycles pids. A marker left by a crashed daemon can end up pointing
// at somebody else's process, and answering "live" there would make Stop
// SIGTERM a stranger -- the worst failure this package can produce.
func TestLiveRejectsARecycledPID(t *testing.T) {
	pid := stranger(t)
	if (Marker{PID: pid}).Live() {
		t.Errorf("Live() on pid %d (a live `sleep`) = true: a foreign process passed as our daemon", pid)
	}
}

// Stopping a pid that is alive but is not us must refuse, not signal it.
func TestStopRefusesAForeignPID(t *testing.T) {
	m := Marker{PID: stranger(t), Owner: OwnerManual}
	if err := m.Stop(OwnerManual); err != ErrGone {
		t.Errorf("stopping a live stranger = %v, want ErrGone", err)
	}
}

// The race the review found: `status` reads a stale marker and probes it (which
// forks a process, taking milliseconds); meanwhile `serve` wins the port and
// writes its own marker. Clearing blindly would erase the LIVE daemon's
// registration and leave it invisible to status and stop.
func TestRunningDoesNotClearAMarkerThatChanged(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	stale := Marker{PID: 999999, Port: 7777, Owner: OwnerTribunal}
	fresh := Marker{PID: os.Getpid(), Port: 7778, Owner: OwnerManual}
	if err := Write(fresh); err != nil {
		t.Fatal(err)
	}

	// Somebody else replaced the marker while we were probing the stale one.
	if err := removeIfUnchanged(stale); err != nil {
		t.Fatalf("removeIfUnchanged() = %v", err)
	}

	got, err := Read()
	if err != nil {
		t.Fatalf("the live daemon's marker was erased: %v", err)
	}
	if got != fresh {
		t.Errorf("marker = %+v, want the live one %+v", got, fresh)
	}
}

// A daemon shutting down must not delete a marker somebody else wrote.
func TestRemoveIfOwnLeavesAnotherProcessAlone(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	other := Marker{PID: os.Getpid() + 1, Port: 7777, Owner: OwnerManual}
	if err := Write(other); err != nil {
		t.Fatal(err)
	}
	if err := RemoveIfOwn(os.Getpid()); err != nil {
		t.Fatalf("RemoveIfOwn() = %v", err)
	}
	got, err := Read()
	if err != nil {
		t.Fatalf("the other process's marker was erased: %v", err)
	}
	if got.PID != other.PID {
		t.Errorf("marker = %+v, want the one written by the other process", got)
	}

	if err := Write(Marker{PID: os.Getpid()}); err != nil {
		t.Fatal(err)
	}
	if err := RemoveIfOwn(os.Getpid()); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(); err != ErrNoMarker {
		t.Error("RemoveIfOwn did not clear our own marker")
	}
}

// The invariant this ticket exists to protect: closing the tribunal session
// must not kill the server the founder started by hand.
func TestStopRefusesForeignDaemon(t *testing.T) {
	m := Marker{PID: os.Getpid(), Owner: OwnerManual}
	if err := m.Stop(OwnerTribunal); err != ErrNotOwner {
		t.Errorf("tribunal stopping a manual daemon = %v, want ErrNotOwner", err)
	}
}

// A marker that cannot be parsed can never be healed by probing: no pid to
// check, no owner to compare. Leaving it makes every future `serve` trip over
// the same corrupt file, so Running clears it.
func TestRunningHealsACorruptMarker(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	path, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{ isto nao e json"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, ok := Running(); ok {
		t.Fatal("a corrupt marker was reported as a running daemon")
	}
	if _, err := Read(); err != ErrNoMarker {
		t.Errorf("the corrupt marker survived: Read() = %v, want ErrNoMarker", err)
	}
}

// The FR7.3 bug, pinned against the function the command actually calls.
//
// The first version of this test re-implemented the transfer inline, so it
// would have passed even if `serve` never transferred anything -- a test of a
// copy proves nothing about the original. TakeOver exists so this test and
// `serve` exercise the same code.
func TestTakeOverMovesOwnershipToTheFounder(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	tribunal := Marker{PID: os.Getpid(), Port: 7777, Owner: OwnerTribunal, Version: "test"}
	if err := Write(tribunal); err != nil {
		t.Fatal(err)
	}

	got, moved, err := TakeOver(tribunal, OwnerManual)
	if err != nil || !moved {
		t.Fatalf("TakeOver() = (%+v, %v, %v), want a move", got, moved, err)
	}
	if got.Owner != OwnerManual {
		t.Fatalf("owner = %q, want %q", got.Owner, OwnerManual)
	}
	if onDisk, _ := Read(); onDisk.Owner != OwnerManual {
		t.Errorf("the marker on disk still says %q", onDisk.Owner)
	}
	// And now closing the tribunal must not take down the founder's panel.
	if err := got.Stop(OwnerTribunal); err != ErrNotOwner {
		t.Errorf("the tribunal could still stop it: %v", err)
	}
}

// A tribunal serve over a tribunal daemon changes nothing, and a manual serve
// over a manual daemon changes nothing either: only the founder joining a
// tribunal daemon moves ownership.
func TestTakeOverOnlyMovesInTheOneCase(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	for _, tc := range []struct{ owner, asker Owner }{
		{OwnerTribunal, OwnerTribunal},
		{OwnerManual, OwnerManual},
		{OwnerManual, OwnerTribunal},
	} {
		m := Marker{PID: os.Getpid(), Port: 7777, Owner: tc.owner}
		if err := Write(m); err != nil {
			t.Fatal(err)
		}
		if _, moved, err := TakeOver(m, tc.asker); moved || err != nil {
			t.Errorf("TakeOver(owner=%q, asker=%q) moved, want no change", tc.owner, tc.asker)
		}
	}
}

// One instance, decided by the filesystem rather than by convention: a second
// Claim must lose even when it asks for a different port (spec FR7.4).
func TestClaimRefusesASecondDaemon(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	if err := Claim(Marker{PID: os.Getpid(), Port: 7777, Owner: OwnerTribunal}); err != nil {
		t.Fatalf("the first Claim failed: %v", err)
	}
	if err := Claim(Marker{PID: os.Getpid() + 1, Port: 8080, Owner: OwnerManual}); err != ErrAlreadyRunning {
		t.Errorf("a second Claim on another port = %v, want ErrAlreadyRunning", err)
	}
	if got, _ := Read(); got.Port != 7777 {
		t.Errorf("the second daemon overwrote the first: %+v", got)
	}
}

// The transfer is a compare-and-set: if the daemon vanished while we probed
// it, the write must fail rather than resurrect a marker for a dead process.
func TestTransferRefusesWhenTheDaemonMovedUnderUs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	seen := Marker{PID: os.Getpid(), Port: 7777, Owner: OwnerTribunal}
	moved := Marker{PID: os.Getpid(), Port: 7778, Owner: OwnerTribunal}
	if err := Write(moved); err != nil {
		t.Fatal(err)
	}

	next := seen
	next.Owner = OwnerManual
	if err := WriteIfUnchanged(seen, next); err != ErrChanged {
		t.Errorf("WriteIfUnchanged over a moved marker = %v, want ErrChanged", err)
	}
	if got, _ := Read(); got != moved {
		t.Errorf("the marker was overwritten anyway: %+v", got)
	}
}

// A transient read error is not corruption: healing it would delete a good
// marker over a passing failure.
func TestOnlyUnparseableMarkersAreHealed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	path, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{ nao e json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := Running(); ok {
		t.Fatal("a corrupt marker was reported as running")
	}
	if _, err := Read(); err != ErrNoMarker {
		t.Errorf("the corrupt marker survived: %v", err)
	}

	if isCorrupt(os.ErrPermission) {
		t.Error("a permission error was classified as corruption")
	}
}
