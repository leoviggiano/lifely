package server

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/leoviggiano/lifely/internal/pendency"
	"github.com/leoviggiano/lifely/internal/scan"
)

// Panel answers questions about what is pending.
//
// Every answer is recomputed from the sources: nothing about a pendency is
// stored, and a count is never inherited from a previous sweep (spec NFR2).
// The only concession is a short in-memory cache so that a page made of
// several requests does not sweep the disk several times -- it never survives
// a restart, and its lifetime is measured in seconds.
type Panel struct {
	Root string
	Run  scan.Runner
	TTL  time.Duration

	mu       sync.Mutex
	cached   *snapshot
	cachedAt time.Time
	now      func() time.Time
}

type snapshot struct {
	Result scan.Result
	Graph  map[string][]string
}

// NewPanel builds a panel over a record repository and a ject runner.
func NewPanel(root string, run scan.Runner) *Panel {
	return &Panel{Root: root, Run: run, TTL: 3 * time.Second, now: time.Now}
}

// sweep returns a fresh view, or the one from a moment ago.
func (p *Panel) sweep() *snapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cached != nil && p.now().Sub(p.cachedAt) < p.TTL {
		return p.cached
	}
	res, graph := scan.All(p.Root, p.Run)
	p.cached = &snapshot{Result: res, Graph: graph}
	p.cachedAt = p.now()
	return p.cached
}

// Invalidate drops the cached sweep. Anything that changes a source calls it,
// so the next read is honest rather than three seconds stale.
func (p *Panel) Invalidate() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cached = nil
}

type pendencyJSON struct {
	ID      string `json:"id"`
	UUID    string `json:"uuid"`
	Class   string `json:"class"`
	Source  string `json:"source"`
	Title   string `json:"title"`
	Detail  string `json:"detail,omitempty"`
	Blocks  string `json:"blocks"`
	Surface string `json:"surface,omitempty"`
	Origin  struct {
		Path    string `json:"path"`
		Locator string `json:"locator,omitempty"`
		Open    string `json:"open,omitempty"`
	} `json:"origin"`
	SeenAt time.Time `json:"seen_at"`
}

func toJSON(p pendency.Pendency) pendencyJSON {
	var out pendencyJSON
	out.ID, out.UUID = p.ID, pendency.UUID(p.ID)
	out.Class, out.Source, out.Title, out.Detail = p.Class, p.Source, p.Title, p.Detail
	out.Blocks, out.Surface, out.SeenAt = string(p.Blocks), p.Surface, p.SeenAt
	out.Origin.Path, out.Origin.Locator, out.Origin.Open = p.Origin.Path, p.Origin.Locator, p.Origin.Open
	return out
}

// Routes registers the read API.
func (p *Panel) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/pendencies", p.list)
	mux.HandleFunc("GET /api/pendencies/{id...}", p.one)
	mux.HandleFunc("GET /api/sources", p.sources)
	mux.HandleFunc("GET /api/projects", p.projects)
}

func (p *Panel) list(w http.ResponseWriter, r *http.Request) {
	snap := p.sweep()
	who := r.URL.Query().Get("who")
	class := r.URL.Query().Get("class")
	source := r.URL.Query().Get("source")

	items := []pendencyJSON{}
	for _, item := range snap.Result.Pendencies {
		if who != "" && string(item.Blocks) != who {
			continue
		}
		if class != "" && item.Class != class {
			continue
		}
		if source != "" && !strings.Contains(item.Source, source) {
			continue
		}
		items = append(items, toJSON(item))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"pendencies": items,
		"count":      len(items),
		"swept_at":   snap.Result.At,
	})
}

func (p *Panel) one(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	for _, item := range p.sweep().Result.Pendencies {
		if item.ID == id {
			writeJSON(w, http.StatusOK, toJSON(item))
			return
		}
	}
	// A pendency that is gone is not an error in the panel: the source may
	// have been decided a second ago, and saying so is the honest answer.
	writeJSON(w, http.StatusNotFound, map[string]string{
		"code":  "gone_from_source",
		"error": "essa pendencia nao esta mais aberta na fonte",
	})
}

type sourceJSON struct {
	Name  string `json:"name"`
	Path  string `json:"path,omitempty"`
	Count int    `json:"count"`
	Error string `json:"error,omitempty"`
}

func (p *Panel) sources(w http.ResponseWriter, r *http.Request) {
	snap := p.sweep()
	out := []sourceJSON{}
	for _, s := range snap.Result.Sources {
		out = append(out, sourceJSON{Name: s.Name, Path: s.Path, Count: s.Count, Error: s.Err})
	}
	// Sources that could not be read travel with the rest, marked: absence of
	// a source is a finding, never silence (spec FR1.3/NFR6).
	writeJSON(w, http.StatusOK, map[string]any{"sources": out, "swept_at": snap.Result.At})
}

func (p *Panel) projects(w http.ResponseWriter, r *http.Request) {
	snap := p.sweep()
	type project struct {
		Slug       string   `json:"slug"`
		Open       int      `json:"open"`
		Blocked    int      `json:"blocked"`
		Dependents []string `json:"-"`
	}
	byslug := map[string]*project{}
	for _, item := range snap.Result.Pendencies {
		if item.Class != "B" {
			continue
		}
		slug := strings.TrimPrefix(item.Source, "ject:")
		if byslug[slug] == nil {
			byslug[slug] = &project{Slug: slug}
		}
		byslug[slug].Open++
		if item.Blocks == pendency.Gate {
			byslug[slug].Blocked++
		}
	}
	out := []project{}
	for _, pr := range byslug {
		out = append(out, *pr)
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": out, "graph": snap.Graph})
}
