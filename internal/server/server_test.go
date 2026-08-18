package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/leoviggiano/lifely/internal/runtime"
)

// A page on another site must not be able to reach the panel through the
// browser, so the Host header is checked and not just the bind address.
func TestLoopbackOnly(t *testing.T) {
	tests := []struct {
		host string
		want int
	}{
		{"127.0.0.1:7777", http.StatusOK},
		{"localhost:7777", http.StatusOK},
		{"[::1]:7777", http.StatusOK},
		{"lifely.example.com", http.StatusForbidden},
		{"192.168.0.10:7777", http.StatusForbidden},
		{"", http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			req.Host = tt.host
			rec := httptest.NewRecorder()
			New(7777, "manual").ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Errorf("Host %q = %d, want %d", tt.host, rec.Code, tt.want)
			}
		})
	}
}

// Ownership can transfer while the daemon runs, so /healthz must read the
// marker instead of echoing what this process started with -- but only when
// the marker describes THIS process.
func TestHealthzReadsOwnershipFromTheMarker(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := runtime.Write(runtime.Marker{PID: os.Getpid(), Port: 7777, Owner: runtime.OwnerManual}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Host = "127.0.0.1:7777"
	rec := httptest.NewRecorder()
	New(7777, "tribunal").ServeHTTP(rec, req)

	var got Health
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Owner != string(runtime.OwnerManual) {
		t.Errorf("/healthz owner = %q, want %q -- it echoed the startup value", got.Owner, runtime.OwnerManual)
	}
}

func TestHealthz(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Host = "127.0.0.1:7777"
	rec := httptest.NewRecorder()
	New(7777, "tribunal").ServeHTTP(rec, req)

	var got Health
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decoding /healthz: %v", err)
	}
	want := Health{Status: "ok", Version: Version, Port: 7777, Owner: "tribunal"}
	if got != want {
		t.Errorf("/healthz = %+v, want %+v", got, want)
	}
}

// A marker written by another daemon must not make us report its owner as
// ours: /healthz answers for the process serving the request.
func TestHealthzIgnoresAnotherProcessMarker(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := runtime.Write(runtime.Marker{PID: os.Getpid() + 1, Owner: runtime.OwnerManual}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Host = "127.0.0.1:7777"
	rec := httptest.NewRecorder()
	New(7777, "tribunal").ServeHTTP(rec, req)

	var got Health
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Owner != "tribunal" {
		t.Errorf("/healthz owner = %q, want the startup value -- it trusted a foreign marker", got.Owner)
	}
}
