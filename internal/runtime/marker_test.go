package runtime

import "testing"

// The tribunal closing its session must never take down a daemon the founder
// started by hand: that would kill the tab he left open (spec FR7.3).
func TestMayStop(t *testing.T) {
	tests := []struct {
		name  string
		owner Owner
		asker Owner
		want  bool
	}{
		{"tribunal stops what it started", OwnerTribunal, OwnerTribunal, true},
		{"tribunal leaves a manual daemon alone", OwnerManual, OwnerTribunal, false},
		{"an explicit stop always wins", OwnerManual, OwnerManual, true},
		{"an explicit stop reaches a tribunal daemon too", OwnerTribunal, OwnerManual, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := Marker{Owner: tt.owner}
			if got := m.MayStop(tt.asker); got != tt.want {
				t.Errorf("Marker{Owner: %q}.MayStop(%q) = %v, want %v", tt.owner, tt.asker, got, tt.want)
			}
		})
	}
}

func TestRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	if _, err := Read(); err != ErrNoMarker {
		t.Fatalf("Read() on a clean cache = %v, want ErrNoMarker", err)
	}

	want := Marker{PID: 4242, Port: 7777, Owner: OwnerTribunal, Version: "test"}
	if err := Write(want); err != nil {
		t.Fatalf("Write() = %v", err)
	}
	got, err := Read()
	if err != nil {
		t.Fatalf("Read() after Write() = %v", err)
	}
	if got != want {
		t.Errorf("Read() = %+v, want %+v", got, want)
	}

	if err := Remove(); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	if _, err := Read(); err != ErrNoMarker {
		t.Errorf("Read() after Remove() = %v, want ErrNoMarker", err)
	}
}
