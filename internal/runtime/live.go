package runtime

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Live reports whether the process a marker describes is still a live lifely.
//
// A marker outlives a crash: the daemon removes it on a clean exit, but a
// killed process leaves it behind. Checking liveness against the process
// rather than the file is necessary but NOT sufficient -- the OS recycles
// PIDs, so a stale marker can point at somebody else's process. Answering
// "live" there would make `serve` refuse to start forever, and would make
// Stop send SIGTERM to a stranger.
//
// So identity is checked too: the process has to still be a lifely.
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
	switch {
	case err == nil:
	case errors.Is(err, os.ErrPermission):
		// The process exists but belongs to someone else. It is alive, and it
		// is definitely not our daemon -- we could not have started it.
		return false
	default:
		return false
	}
	return m.isLifely()
}

// isLifely asks the OS whether the process at this PID is the same program we
// are -- which is the honest form of the question, because `status` and `stop`
// are the lifely CLI asking about a lifely daemon.
//
// Without this, a recycled PID makes a dead daemon look alive; and with Stop
// on top of it, lifely would SIGTERM whatever inherited the number.
//
// Comparing against our own executable rather than a hard-coded "lifely" also
// keeps the check honest under `go test` and `go run`, where the binary has
// another name but is still us.
func (m Marker) isLifely() bool {
	self, err := os.Executable()
	if err != nil {
		return false
	}
	out, err := exec.Command("ps", "-p", strconv.Itoa(m.PID), "-o", "comm=").Output()
	if err != nil {
		// ps failing is not proof of anything; refuse to claim liveness.
		return false
	}
	return filepath.Base(strings.TrimSpace(string(out))) == filepath.Base(self)
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
// session must not take down a server the founder started by hand. It also
// refuses when the PID is no longer a lifely -- signalling a stranger because
// the OS recycled a number is a worse failure than not stopping anything.
func (m Marker) Stop(asker Owner) error {
	if !m.MayStop(asker) {
		return ErrNotOwner
	}
	if !m.Live() {
		return ErrGone
	}
	proc, err := os.FindProcess(m.PID)
	if err != nil {
		return err
	}
	return proc.Signal(syscall.SIGTERM)
}

// ErrGone reports a stop asked of a daemon that is no longer running.
var ErrGone = errors.New("no lifely daemon is running at that pid")

// ErrNotOwner reports a stop refused because the caller does not own the
// running daemon.
var ErrNotOwner = errors.New("this daemon was started by hand; only an explicit `lifely stop` takes it down")
