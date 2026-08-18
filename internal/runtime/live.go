package runtime

import (
	"errors"
	"fmt"
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

// ErrForeign reports a stop refused because the pid belongs to another
// program: the marker is stale and the process is somebody else's.
var ErrForeign = errors.New("the pid in the marker belongs to another program; refusing to signal it")

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

// probeAlive reports whether the pid is still running at all, regardless of
// what program it is. Used by tests that need to assert we left a process
// alone.
func (m Marker) probeAlive() bool {
	return m.probe() != identityGone
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
		// A marker we cannot parse is worse than none: it can never be healed
		// by a probe, and every future `serve` would keep tripping over it.
		// Absent is the honest state, so make it true.
		// Heal only what is provably unusable, and only if nobody replaced it
		// meanwhile: an unconditional Remove here would erase a marker another
		// daemon wrote in the window -- the very erasure this file spends two
		// other functions preventing. A transient read error is not corruption.
		if isCorrupt(err) {
			// A failure to heal is worth knowing about -- it means every
			// later serve will trip over the same file -- but it must not
			// turn "no daemon" into an error for the caller.
			if rmErr := removeIfCorrupt(); rmErr != nil {
				fmt.Fprintf(os.Stderr, "lifely: nao consegui limpar o marcador corrompido: %v\n", rmErr)
			}
		}
		return Marker{}, false
	}
	if !m.Live() {
		if rmErr := removeIfUnchanged(m); rmErr != nil {
			fmt.Fprintf(os.Stderr, "lifely: nao consegui limpar o marcador orfao: %v\n", rmErr)
		}
		return Marker{}, false
	}
	return m, true
}

// isCorrupt separates "this file cannot be parsed" from "I could not read it
// right now". Only the first is healed; the second is transient, and healing
// it would delete a perfectly good marker over a passing error.
func isCorrupt(err error) bool { return IsCorrupt(err) }

// removeIfCorrupt re-reads and deletes only while the file is still
// unparseable: between our read and this one another daemon may have written a
// perfectly good marker, and that one is not ours to erase.
func removeIfCorrupt() error {
	if _, err := Read(); err == nil || !isCorrupt(err) {
		return nil
	}
	return Remove()
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

// ForceStop signals the pid without confirming identity.
//
// It exists for the environment where the identity probe cannot run at all --
// otherwise an unidentifiable marker would be permanently unstoppable. Behind
// an explicit flag on purpose: signalling a process you cannot identify is a
// last resort, not a fallback.
func (m Marker) ForceStop(asker Owner) error {
	// Its own ownership guard, not just the caller's: Stop has one, and an
	// API where the escape hatch is the unguarded twin invites exactly the
	// mistake the hatch was fenced against in the command layer.
	if !m.MayStop(asker) {
		return ErrNotOwner
	}
	// NEVER signal a process we have identified as somebody else's.
	//
	// The escape hatch exists for the pid we could not identify, where a human
	// looked and decided. For a pid we CAN identify as foreign, signalling is
	// the exact bug this package spends four functions preventing -- so the
	// force path only clears the stale marker and leaves the stranger alone.
	if m.probe() == identityForeign {
		return removeIfUnchanged(m)
	}
	proc, err := os.FindProcess(m.PID)
	if err != nil {
		return err
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return err
	}
	// Clear the marker too: the daemon we just signalled cannot be identified,
	// so it will not be trusted to clean up after itself. Leaving the file
	// behind would keep every later `status` reporting a daemon that is gone.
	return removeIfUnchanged(m)
}

// Stop asks the daemon described by m to shut down, on behalf of asker.
//
// It refuses when the asker does not own the daemon (the tribunal closing its
// session must not take down a server the founder started by hand), when the
// pid is gone, and when we cannot confirm the process is a lifely.
func (m Marker) Stop(asker Owner) error {
	// Liveness first, ownership second: refusing on ownership when the daemon
	// is already gone tells the caller to try a different flag for a process
	// that does not exist.
	switch m.probe() {
	case identityGone:
		return ErrGone
	case identityForeign:
		// Distinct from "gone": the pid IS alive, it is simply not us. The
		// caller may still want the marker out of the way, and collapsing this
		// into ErrGone would leave `--force` unreachable for the one case
		// where a human has actually looked and decided.
		return ErrForeign
	case identityUnknown:
		return ErrUnidentified
	}
	if !m.MayStop(asker) {
		return ErrNotOwner
	}
	proc, err := os.FindProcess(m.PID)
	if err != nil {
		return err
	}
	return proc.Signal(syscall.SIGTERM)
}
