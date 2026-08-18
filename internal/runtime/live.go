package runtime

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// ErrNotOwner reports a stop refused because the caller does not own the
// running daemon.
var ErrNotOwner = errors.New("this daemon was started by hand; only an explicit `lifely stop` takes it down")

// ErrGone reports a stop asked of a daemon that is no longer running.
var ErrGone = errors.New("no lifely daemon is running at that pid")

// ErrUnidentified reports a stop refused because we could not confirm that the
// process at that pid is still a lifely.
var ErrUnidentified = errors.New("cannot confirm the process at that pid is lifely; refusing to signal it")

// identity is what we could learn about the process behind a marker.
type identity int

const (
	// identityGone: no such process, or it is not ours to talk to.
	identityGone identity = iota
	// identityOurs: the process is the same program we are.
	identityOurs
	// identityUnknown: the process is alive, but the probe could not run.
	identityUnknown
	// identityForeign: the process is alive and is a different program.
	identityForeign
)

// probe answers what the marker's pid actually is.
//
// Three outcomes matter and they must not be collapsed into a boolean:
//   - ours          -> a live daemon; report it, and Stop may signal it.
//   - foreign       -> a recycled pid; report absence, and never signal it.
//   - unknown       -> alive, but we could not identify it. Reporting absence
//     here makes a live daemon invisible (`status` lies, `serve` then fails to
//     bind); so it counts as live, but Stop still refuses -- not knowing what
//     a process is, is a reason not to kill it, not a reason to pretend it is
//     gone.
func (m Marker) probe() identity {
	if m.PID <= 0 {
		return identityGone
	}
	proc, err := os.FindProcess(m.PID)
	if err != nil {
		return identityGone
	}
	// On unix FindProcess always succeeds; signal 0 is the actual probe.
	switch err = proc.Signal(syscall.Signal(0)); {
	case err == nil:
	case errors.Is(err, os.ErrPermission):
		// Alive, but owned by another user -- we could not have started it.
		return identityForeign
	default:
		return identityGone
	}

	self, err := os.Executable()
	if err != nil {
		return identityUnknown
	}
	name, ok := processName(m.PID)
	if !ok {
		return identityUnknown
	}
	if sameProgram(name, filepath.Base(self)) {
		return identityOurs
	}
	return identityForeign
}

// processName asks the OS what program is running at pid.
func processName(pid int) (string, bool) {
	// Linux exposes the real path, with no truncation.
	if runtime.GOOS == "linux" {
		if target, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe"); err == nil {
			return filepath.Base(target), true
		}
	}
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err != nil {
		return "", false
	}
	name := strings.TrimSpace(string(out))
	if name == "" {
		return "", false
	}
	return filepath.Base(name), true
}

// sameProgram compares two program names, tolerating the truncation `ps` does.
//
// `ps -o comm=` cuts at 15 characters on Linux, so an exact comparison would
// reject a binary whose name is longer than that.
func sameProgram(observed, self string) bool {
	if observed == self {
		return true
	}
	const psCommLimit = 15
	if len(observed) == psCommLimit && strings.HasPrefix(self, observed) {
		return true
	}
	return false
}

// Live reports whether the marker still describes a running lifely.
//
// A marker outlives a crash, and the OS recycles pids -- so liveness alone is
// not enough: a stale marker can point at somebody else's process.
func (m Marker) Live() bool {
	switch m.probe() {
	case identityOurs, identityUnknown:
		return true
	default:
		return false
	}
}

// Running returns the live daemon, if there is one.
//
// A marker left behind by a dead process is cleared as a side effect -- but
// only if it has not changed since we judged it dead. Without that check, a
// `serve` racing with us can write its own marker in the window while we are
// probing, and our Remove() would erase a LIVE daemon's registration, leaving
// it invisible to status and stop.
func Running() (Marker, bool) {
	m, err := Read()
	if err != nil {
		return Marker{}, false
	}
	if !m.Live() {
		_ = removeIfUnchanged(m)
		return Marker{}, false
	}
	return m, true
}

// removeIfUnchanged deletes the marker only if it still holds exactly what we
// read before probing.
func removeIfUnchanged(seen Marker) error {
	current, err := Read()
	if errors.Is(err, ErrNoMarker) {
		return nil
	}
	if err != nil {
		return err
	}
	if current != seen {
		return nil
	}
	return Remove()
}

// Stop asks the daemon described by m to shut down, on behalf of asker.
//
// It refuses when the asker does not own the daemon (the tribunal closing its
// session must not take down a server the founder started by hand), when the
// pid is gone, and when we cannot confirm the process is a lifely.
func (m Marker) Stop(asker Owner) error {
	if !m.MayStop(asker) {
		return ErrNotOwner
	}
	switch m.probe() {
	case identityGone, identityForeign:
		return ErrGone
	case identityUnknown:
		return ErrUnidentified
	}
	proc, err := os.FindProcess(m.PID)
	if err != nil {
		return err
	}
	return proc.Signal(syscall.SIGTERM)
}
