package runtime

import (
	"os"
	"os/exec"
	"testing"
)

func TestLive(t *testing.T) {
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

// The invariant this ticket exists to protect: closing the tribunal session
// must not kill the server the founder started by hand.
func TestStopRefusesForeignDaemon(t *testing.T) {
	m := Marker{PID: os.Getpid(), Owner: OwnerManual}
	if err := m.Stop(OwnerTribunal); err != ErrNotOwner {
		t.Errorf("tribunal stopping a manual daemon = %v, want ErrNotOwner", err)
	}
}
