package runtime

import (
	"errors"
	"os"
	"syscall"
)

// Live reports whether the process a marker describes is still running.
//
// A marker outlives a crash: the daemon removes it on a clean exit, but a
// killed process leaves it behind. Treating a stale marker as a live daemon
// would make `serve` refuse to start forever, so liveness is checked against
// the process, not against the file.
func (m Marker) Live() bool {
	if m.PID <= 0 {
		return false
	}
	proc, err := os.FindProcess(m.PID)
	if err != nil {
		return false
	}
	// On unix FindProcess always succeeds; signal 0 is the actual probe.
	err = proc.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	// EPERM means the process exists but belongs to someone else, which still
	// counts as live -- and is a case worth not mistaking for "gone".
	return errors.Is(err, os.ErrPermission)
}

// Running returns the live daemon, if there is one.
//
// A marker left behind by a dead process is cleared as a side effect: the
// caller asked whether a daemon is running, and the honest answer is no.
func Running() (Marker, bool) {
	m, err := Read()
	if err != nil {
		return Marker{}, false
	}
	if !m.Live() {
		_ = Remove()
		return Marker{}, false
	}
	return m, true
}

// Stop asks the daemon described by m to shut down, on behalf of asker.
//
// It refuses when the asker does not own the daemon: the tribunal closing its
// session must not take down a server the founder started by hand.
func (m Marker) Stop(asker Owner) error {
	if !m.MayStop(asker) {
		return ErrNotOwner
	}
	proc, err := os.FindProcess(m.PID)
	if err != nil {
		return err
	}
	return proc.Signal(syscall.SIGTERM)
}

// ErrNotOwner reports a stop refused because the caller does not own the
// running daemon.
var ErrNotOwner = errors.New("this daemon was started by hand; only an explicit `lifely stop` takes it down")
