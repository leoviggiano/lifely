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
