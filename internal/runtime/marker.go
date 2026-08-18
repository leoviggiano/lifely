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
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "lifely")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, "daemon.json"), nil
}

// Write records a running daemon.
func Write(m Marker) error {
	path, err := Path()
	if err != nil {
		return err
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	// Atomic: os.WriteFile truncates in place, and a second process now
	// rewrites this file (the ownership transfer on reuse). A reader landing
	// between truncate and write would see an empty file and conclude there is
	// no daemon. Temp plus rename makes every read see one whole version.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".daemon-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
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

// ErrChanged reports that the marker moved under a compare-and-set.
var ErrChanged = errors.New("the daemon marker changed while we were reading it")

// Read returns the registered daemon, or ErrNoMarker when there is none.
func Read() (Marker, error) {
	path, err := Path()
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
	return Remove()
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
