// Package web carries the panel's templates and static assets, embedded into
// the binary so that `git clone` + `go build` produces the whole app -- no JS
// toolchain, no separate asset step (spec FR4.11/NFR4).
package web

import "embed"

// Assets holds every file the panel serves.
//
//go:embed templates static
var Assets embed.FS
