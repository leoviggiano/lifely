// Package server holds the local HTTP surface of lifely.
//
// The panel is a local tool: it binds loopback and refuses any request whose
// Host is not a loopback name, so a page on another site cannot reach it
// through the founder's browser (spec NFR3). There is no authentication, and
// there is not meant to be one -- the guard is the network boundary.
package server

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"
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
	mux := http.NewServeMux()
	if panel != nil {
		panel.Routes(mux)
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, Health{
			Status:  "ok",
			Version: Version,
			Port:    port,
			Owner:   owner,
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

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
