// Package runtime tracks the single running lifely daemon: which process it
// is, where it listens, and who started it.
//
// Ownership is the whole point. The tribunal session starts the server and
// stops it when the session closes, but a server the founder started by hand
// must survive that -- killing the tab he left open is the bug this file
// exists to prevent (spec FR7.3).
package runtime

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Owner says who asked for the daemon to run.
type Owner string

const (
	// OwnerTribunal marks a daemon started by the /tribunal session, which
	// may stop it again when that session closes.
	OwnerTribunal Owner = "tribunal"
	// OwnerManual marks a daemon started by hand. Nothing but an explicit
	// `lifely stop` takes it down.
	OwnerManual Owner = "manual"
)

// Marker is the runtime state of a daemon. It is process state, never domain
// state: no pendency, no scan result and no transcript is ever written here.
type Marker struct {
	PID     int    `json:"pid"`
	Port    int    `json:"port"`
	Owner   Owner  `json:"owner"`
	Version string `json:"version"`
}

// ErrNoMarker reports that no daemon has registered itself.
var ErrNoMarker = errors.New("no lifely daemon is registered")

// Path returns the marker's location, creating its directory if needed.
func Path() (string, error) {
	path, err := pathNoCreate()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	return path, nil
}

// pathNoCreate is the single place the marker's location is spelled out, so a
// reader and a writer can never disagree about where it lives.
func pathNoCreate() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "lifely", "daemon.json"), nil
}

// Write records a running daemon.
func Write(m Marker) error {
	path, err := Path()
	if err != nil {
		return err
	}
	tmp, err := stage(m, path)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	// Rename overwrites: this is the update path, and the caller has already
	// decided it may replace what is there.
	return os.Rename(tmp, path)
}

func Claim(m Marker) error {
	path, err := Path()
	if err != nil {
		return err
	}
	tmp, err := stage(m, path)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	// Link, not rename: link fails with EEXIST when the name is taken, so one
	// caller wins and no half-written marker is ever visible. O_EXCL alone
	// would give exclusivity without atomicity -- the file exists, empty,
	// between the create and the write.
	if err := os.Link(tmp, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return ErrAlreadyRunning
		}
		return err
	}
	return nil
}

// stage writes a complete marker to a temporary file beside its final home and
// returns that path. Write and Claim both publish from here; they differ only
// in how the name is put in place.
func stage(m Marker, path string) (string, error) {
	data, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".marker-*.json")
	if err != nil {
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// ErrAlreadyRunning reports that another daemon holds the marker.
var ErrAlreadyRunning = errors.New("another lifely daemon is already registered")

// TakeOver moves a running daemon's ownership when the founder joins it by
// hand, and reports whether anything changed.
//
// It lives here, and not inline in `serve`, because it IS the FR7.3 guarantee:
// a copy of this logic in a test proves nothing about the command that runs it.
func TakeOver(live Marker, asker Owner) (Marker, bool, error) {
	if asker != OwnerManual || live.Owner != OwnerTribunal {
		return live, false, nil
	}
	next := live
	next.Owner = OwnerManual
	if err := WriteIfUnchanged(live, next); err != nil {
		return live, false, err
	}
	return next, true, nil
}

// WriteIfUnchanged replaces the marker only when it still holds `seen`.
//
// The ownership transfer runs in a different process from the daemon it
// rewrites: between reading the marker and writing the new owner, that daemon
// may have exited and cleared it. A blind write would resurrect a marker for a
// process that is gone.
func WriteIfUnchanged(seen, next Marker) error {
	current, err := Read()
	if err != nil {
		return err
	}
	if current != seen {
		return ErrChanged
	}
	return Write(next)
}

// IsCorrupt reports whether an error from Read/Peek means the file cannot be
// parsed -- as opposed to could not be read right now. The first is healable;
// the second is a real failure the caller must hear about.
func IsCorrupt(err error) bool {
	var syntax *json.SyntaxError
	var typ *json.UnmarshalTypeError
	return errors.As(err, &syntax) || errors.As(err, &typ)
}

// ErrChanged reports that the marker moved under a compare-and-set.
var ErrChanged = errors.New("the daemon marker changed while we were reading it")

// Peek is Read without creating anything on the way: for callers on a hot
// path (every /healthz), a lookup must not have the side effect of making a
// directory.
func Peek() (Marker, error) {
	return readFrom(pathNoCreate)
}

// Read returns the registered daemon, or ErrNoMarker when there is none.
func Read() (Marker, error) {
	return readFrom(Path)
}

// readFrom is the one body Read and Peek share: they differ only in whether
// looking is allowed to create the directory on the way.
func readFrom(locate func() (string, error)) (Marker, error) {
	path, err := locate()
	if err != nil {
		return Marker{}, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Marker{}, ErrNoMarker
	}
	if err != nil {
		return Marker{}, err
	}
	var m Marker
	if err := json.Unmarshal(data, &m); err != nil {
		return Marker{}, err
	}
	return m, nil
}

// RemoveIfOwn clears the marker only when it still describes this process.
//
// A daemon shutting down must not delete a marker somebody else wrote: with
// two `serve` calls racing, the loser exiting would otherwise erase the
// winner's registration and leave a live daemon invisible to `status`/`stop`.
func RemoveIfOwn(pid int) error {
	m, err := Read()
	if errors.Is(err, ErrNoMarker) {
		return nil
	}
	if err != nil {
		return err
	}
	if m.PID != pid {
		return nil
	}
	// Compare the WHOLE marker, not just the pid, and only then delete: between
	// Read and Remove another daemon can register, and a pid-only check would
	// still erase a live registration written in that window.
	return removeIfUnchanged(m)
}

// Remove clears the marker.
func Remove() error {
	path, err := Path()
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// MayStop reports whether a caller acting as `asker` is allowed to stop the
// daemon described by m.
//
// The tribunal may only stop what the tribunal started. A manual daemon is the
// founder's, and closing a tribunal session must not take it down.
func (m Marker) MayStop(asker Owner) bool {
	if asker == OwnerManual {
		return true
	}
	return m.Owner == OwnerTribunal
}
