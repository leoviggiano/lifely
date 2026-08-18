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

// The OS recycles pids. A marker left by a crashed daemon can end up pointing
// at somebody else's process, and answering "live" there would make Stop
// SIGTERM a stranger -- the worst failure this package can produce.
func TestLiveRejectsARecycledPID(t *testing.T) {
	// pid 1 is alive and is definitely not us.
	if (Marker{PID: 1}).Live() {
		t.Error("Live() on pid 1 = true: a foreign process passed as our daemon")
	}
}

// Stopping a daemon that is no longer there must say so, not signal whoever
// inherited the number.
func TestStopRefusesAForeignPID(t *testing.T) {
	m := Marker{PID: 1, Owner: OwnerManual}
	if err := m.Stop(OwnerManual); err != ErrGone {
		t.Errorf("stopping a foreign pid = %v, want ErrGone", err)
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
