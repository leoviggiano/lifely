// Package server holds the local HTTP surface of lifely.
//
// The panel is a local tool: it binds loopback and refuses any request whose
// Host is not a loopback name, so a page on another site cannot reach it
// through the founder's browser (spec NFR3). There is no authentication, and
// there is not meant to be one -- the guard is the network boundary.
package server

import (
	"github.com/go-fuego/fuego"

	"encoding/json"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/leoviggiano/lifely/internal/runtime"
)

// Version is the build's version string, surfaced by /healthz.
const Version = "0.0.1-bootstrap"

// Health is the payload of GET /healthz.
type Health struct {
	Status  string `json:"status"`
	Version string `json:"version"`
	Port    int    `json:"port"`
	Owner   string `json:"owner"`
}

// New returns the panel's handler. port and owner describe the running
// daemon and are reported by /healthz. panel may be nil, which serves only
// /healthz -- useful for a smoke test that should not touch the sources.
func New(port int, owner string, panel *Panel) http.Handler {
	// OpenAPI is generated from the code and served locally; the external
	// Swagger UI stays OFF -- same shape the house proved in ject D19, and the
	// panel is loopback-only, so it must not pull anything from a CDN.
	s := fuego.NewServer(
		fuego.WithoutStartupMessages(),
		fuego.WithEngineOptions(fuego.WithOpenAPIConfig(fuego.OpenAPIConfig{
			DisableSwaggerUI: true,
			DisableLocalSave: true,
			DisableMessages:  true,
		})),
	)
	if panel != nil {
		panel.Register(s)
	}
	// Mount the spec route by hand. fuego does it inside Run(), which this
	// daemon never calls -- it hands the mux to its own http.Server so the
	// loopback guard and the shutdown path stay ours. Without this the whole
	// point of the migration silently does not happen: measured before
	// adding it, /swagger/openapi.json answered 404.
	//
	// SpecHandler only; never UIHandler. The panel is loopback-only and the
	// swagger UI pulls its assets from a CDN (ject D19: "UI externa
	// desabilitada").
	s.SpecHandler(s.Engine)
	mux := s.Mux
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, Health{
			Status:  "ok",
			Version: Version,
			Port:    port,
			// Read from the marker, not from the value captured at startup:
			// ownership can transfer while the daemon runs (a manual `serve`
			// reusing a tribunal daemon), and a frozen answer here would tell
			// the caller the daemon still belongs to whoever started it.
			Owner: currentOwner(owner),
		})
	})
	return LoopbackOnly(mux)
}

// LoopbackOnly rejects requests whose Host header is not a loopback name.
//
// Binding to 127.0.0.1 alone does not stop a page served by another site from
// pointing a request at the port through the browser; checking Host does.
func LoopbackOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isLoopbackHost(r.Host) {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"code":  "not_loopback",
				"error": "lifely only answers requests addressed to a loopback host",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isLoopbackHost(host string) bool {
	if host == "" {
		return false
	}
	name := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		name = h
	}
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	if name == "localhost" {
		return true
	}
	name = strings.Trim(name, "[]")
	ip := net.ParseIP(name)
	return ip != nil && ip.IsLoopback()
}

// currentOwner returns the owner recorded for the running daemon, falling
// back to the one this process started with when there is no marker to read.
func currentOwner(fallback string) string {
	// Read, never create: Path() does a MkdirAll, and /healthz is polled --
	// creating a directory on every request is a side effect a read has no
	// business having.
	m, err := runtime.Peek()
	// Trust the marker only when it describes THIS process: the rest of this
	// package established that invariant (RemoveIfOwn, WriteIfUnchanged), and
	// a marker written by another daemon would make us report its owner as
	// ours.
	if err != nil || m.PID != os.Getpid() {
		return fallback
	}
	return string(m.Owner)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
