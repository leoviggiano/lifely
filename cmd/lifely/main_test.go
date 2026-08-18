package main

import (
	"errors"
	"os"
	"testing"

	"github.com/leoviggiano/lifely/internal/runtime"
)

// The command layer carries contracts nobody else can check: which exit code
// a refusal produces, whether a missing flag is refused instead of guessed,
// and whether `-h` is a request or a failure. All of it was proven by hand
// until now, which is proof that vanishes when the terminal closes.

func TestStopRequiresAnExplicitOwner(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	err := stop(nil)
	if err == nil {
		t.Fatal("stop with no --owner succeeded; it must refuse instead of guessing")
	}
	if errors.Is(err, errRefused) {
		t.Error("a missing flag is a usage error, not a refusal")
	}
}

func TestStopRejectsAnUnknownOwner(t *testing.T) {
	if err := stop([]string{"--owner", "sindico"}); err == nil {
		t.Error("stop accepted an owner that does not exist")
	}
}

// `-h` prints usage and succeeds: a help request is not a failure.
func TestHelpIsNotAnError(t *testing.T) {
	for _, args := range [][]string{{"-h"}, {"--help"}, {"help"}} {
		if err := run(args); err != nil {
			t.Errorf("run(%v) = %v, want nil", args, err)
		}
	}
	if err := stop([]string{"-h"}); err != nil {
		t.Errorf("stop -h = %v, want nil", err)
	}
}

func TestUnknownCommandFails(t *testing.T) {
	if err := run([]string{"voar"}); err == nil {
		t.Error("an unknown command succeeded")
	}
}

// Refusing to stop somebody else's daemon is a distinct outcome from failing:
// a script closing the tribunal has to tell "stopped" from "still running".
func TestRefusalIsItsOwnOutcome(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	// A live daemon owned by the founder, with this test process as its pid.
	if err := runtime.Write(runtime.Marker{
		PID: os.Getpid(), Port: 7777, Owner: runtime.OwnerManual,
	}); err != nil {
		t.Fatal(err)
	}

	err := stop([]string{"--owner", "tribunal"})
	if !errors.Is(err, errRefused) {
		t.Fatalf("the tribunal stopping a manual daemon = %v, want errRefused", err)
	}
	// And the daemon is untouched.
	if got, rerr := runtime.Read(); rerr != nil || got.Owner != runtime.OwnerManual {
		t.Errorf("the refused stop changed the marker: %+v (%v)", got, rerr)
	}
}

func TestStatusOnAnEmptyCacheSaysNothingIsUp(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := status(); err != nil {
		t.Errorf("status() with no daemon = %v, want nil", err)
	}
}
