package server

import (
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"path/filepath"
	"strconv"
	"time"

	"github.com/go-fuego/fuego"

	"github.com/leoviggiano/lifely/internal/sonar"
	"github.com/leoviggiano/lifely/web"
)

// defaultFeedLimit is how many events a request returns when it does not ask.
//
// The log had 1404 lines on its third day and only grows; the feed is a
// history to glance at, not an archive to page through, so the default is one
// screenful of scrolling and the caller widens it with ?limit=.
const defaultFeedLimit = 200

// maxFeedLimit caps what a caller may ask for. The panel is loopback-only and
// the reader is cheap, so the cap is about keeping one request from rendering
// a megabyte of HTML, not about defending a boundary.
const maxFeedLimit = 2000

// pollSeconds is how often the page asks for a new fragment.
//
// The ticket's DoD allows "tempo real ou polling curto" and the sonar's own
// cadence is ~10 minutes, so three seconds is already far finer than the
// thing being watched. It is short enough that an event appears while the
// founder is still looking at the screen, which is the whole request.
const pollSeconds = 3

// sonarTemplates renders the feed page and the fragment it polls.
//
// One renderer, in Go, for both. The alternative was to answer JSON and
// re-render in the browser, which means two implementations of "what an event
// looks like" drifting apart -- the exact class the house has already paid
// for elsewhere. The page and the fragment share the same block, so what the
// first paint shows and what the poll replaces it with cannot disagree.
var sonarTemplates = template.Must(template.New("sonar").Funcs(template.FuncMap{
	"stamp": stamp,
	"since": since,
}).ParseFS(web.Assets, "templates/base.html", "templates/sonar.html"))

// stamp formats an event's time for the feed, or says it had none.
func stamp(t time.Time) string {
	if t.IsZero() {
		return "--:--"
	}
	return t.Format("02/01 15:04")
}

// since renders a duration the way the feed reads it out loud.
func since(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dmin ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%02dmin ago", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// sonarEventJSON is one event on the wire.
type sonarEventJSON struct {
	At    time.Time `json:"at"`
	Kind  string    `json:"kind,omitempty"`
	Topic string    `json:"topic,omitempty"`
	Theme string    `json:"theme"`
	Text  string    `json:"text,omitempty"`
	// Raw is always present, parsed or not: the feed's contract is that the
	// tribunal's own words survive whatever this reader made of them.
	Raw string `json:"raw"`
	// Parsed false means the line carried no readable stamp and travels raw.
	Parsed bool `json:"parsed"`
}

// sonarResponse is the body of GET /api/sonar.
type sonarResponse struct {
	Events []sonarEventJSON `json:"events"`
	// Count is how many events this response carries; Total is how many the
	// read produced before limit. They differ exactly when limit cut.
	Count int `json:"count"`
	Total int `json:"total"`
	// Windowed says Total counts only the end of the log, because the file
	// was bigger than the read's window. A consumer that reports Total as
	// "the fleet's history" without checking this is reporting a fraction as
	// a whole.
	Windowed bool `json:"windowed"`
	// Projects are the slugs the log declares, offered as filter options.
	Projects []string `json:"projects"`
	// Path is the log that was read, so a wrong --root shows on the screen.
	Path     string    `json:"path"`
	ReadAt   time.Time `json:"read_at"`
	NewestAt time.Time `json:"newest_at"`
	// Stale is the newest event being older than twice the sonar's cadence.
	// An old stamp is itself the alarm -- the charter says so -- so it is a
	// field and not something a consumer has to recompute.
	Stale bool `json:"stale"`
	// Missing is "no log on this machine", which is an empty state. Error is
	// "the log is there and unreadable", which is a finding. They are two
	// fields because they are two answers.
	Missing bool   `json:"missing"`
	Error   string `json:"error,omitempty"`
}

// sonarPath is the log this panel reads, derived from the root it was
// pointed at.
func (p *Panel) sonarPath() string {
	return filepath.Join(p.Root, sonar.LogRelPath)
}

// sonarRead is one read of the log: the feed the caller asked for, plus the
// filter options, which are computed from the WHOLE log and not from the
// filtered feed -- filtering to one project must never remove the way back to
// the others.
type sonarRead struct {
	Feed     sonar.Feed
	Projects []string
}

// options are the filter's choices: the projects the log declares, plus the
// filter in force when it is not one of them.
//
// Without that last part the control lied. A URL like `?project=lifely-018`
// narrows the feed to a value the log never declares, so no <option> carried
// `selected` and the browser fell back to showing `all` over a feed that was
// anything but -- the gate caught it (run 01M0FF44RM). A control showing a
// state that is not the state in force is the same class of quiet wrongness
// the rest of this screen refuses.
func (r sonarRead) options(project string) []string {
	if project == "" {
		return r.Projects
	}
	for _, slug := range r.Projects {
		if slug == project {
			return r.Projects
		}
	}
	return append(append([]string{}, r.Projects...), project)
}

// clock is the panel's time source, falling back to the wall clock so a
// zero-value Panel still answers.
func (p *Panel) clock() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

// readSonar reads the log once and applies the project filter.
//
// The sweep cache is deliberately not involved. A sweep shells out to ject
// per ticket and is measured in seconds; this is one read of one file, and
// putting it behind the same three-second TTL would have made the feed as
// slow as the thing it is not.
//
// The narrowing itself is sonar.Filter's and not written here, because
// keeping Events, Total and NewestAt agreeing with one another is one
// invariant and belongs next to the type that declares them.
func (p *Panel) readSonar(project string, limit int) sonarRead {
	feed := sonar.Read(p.sonarPath(), p.clock())
	// Options come from the WHOLE log, before the filter: narrowing to one
	// project must never remove the way back to the others.
	return sonarRead{
		Feed:     sonar.Filter(feed, project, limit),
		Projects: sonar.Projects(feed.Events),
	}
}

// sonarAPI answers GET /api/sonar.
//
// A log that cannot be read comes back 200 with the failure in the body, not
// 500: spec FR3 says a broken source is a marked entry, and a panel that
// answers 500 to "how is the fleet" has replaced the news with its own
// plumbing.
func (p *Panel) sonarAPI(c fuego.ContextNoBody) (sonarResponse, error) {
	read := p.readSonar(c.QueryParam("project"), limitParam(c.QueryParam("limit")))
	feed := read.Feed
	out := sonarResponse{
		Events:   make([]sonarEventJSON, 0, len(feed.Events)),
		Count:    len(feed.Events),
		Total:    feed.Total,
		Windowed: feed.Windowed,
		Projects: read.Projects,
		Path:     feed.Path,
		ReadAt:   feed.ReadAt,
		NewestAt: feed.NewestAt,
		Stale:    feed.Stale(),
		Missing:  feed.Missing,
		Error:    feed.Err,
	}
	for _, ev := range feed.Events {
		out.Events = append(out.Events, sonarEventJSON{
			At: ev.At, Kind: ev.Kind, Topic: ev.Topic,
			Theme: string(ev.Theme), Text: ev.Text, Raw: ev.Raw, Parsed: ev.Parsed,
		})
	}
	return out, nil
}

// limitParam reads ?limit=, falling back to the default on anything it cannot
// use. A bad limit is not worth an error page: the caller asked for the feed.
func limitParam(raw string) int {
	if raw == "" {
		return defaultFeedLimit
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return defaultFeedLimit
	}
	if n > maxFeedLimit {
		return maxFeedLimit
	}
	return n
}

// sonarView is what the templates render.
type sonarView struct {
	Title string
	// Project is the filter in force, empty for all.
	Project string
	// Projects are the options the filter offers: what the log declares, plus
	// Project itself when the URL asked for something the log never named.
	Projects []string
	Limit    int
	Poll     int
	Events   []sonar.Event
	Feed     sonar.Feed
	Age      time.Duration
	// Cut is the limit having hidden matched events, which the footer says
	// out loud rather than letting the reader assume the feed is complete.
	Cut bool
}

// sonarView assembles what the templates render from one request.
func (p *Panel) sonarView(r *http.Request) sonarView {
	project := r.URL.Query().Get("project")
	limit := limitParam(r.URL.Query().Get("limit"))
	read := p.readSonar(project, limit)
	return sonarView{
		Title:    "sonar",
		Project:  project,
		Projects: read.options(project),
		Limit:    limit,
		Poll:     pollSeconds,
		Events:   read.Feed.Events,
		Feed:     read.Feed,
		Age:      read.Feed.Age(),
		Cut:      read.Feed.Total > len(read.Feed.Events),
	}
}

// sonarPage serves the whole feed screen.
func (p *Panel) sonarPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := sonarTemplates.ExecuteTemplate(w, "base.html", p.sonarView(r)); err != nil {
		// The status is already written by the time a template fails
		// halfway, so there is nothing honest left to send. Say it in the
		// body rather than truncate in silence.
		fmt.Fprintf(w, "\n<!-- the feed could not be rendered: %v -->\n", err)
	}
}

// sonarFragment serves just the event list, which is what the page polls.
func (p *Panel) sonarFragment(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// The fragment is a live read of a file that changes; a cached one would
	// make a feed that never moves look exactly like a quiet fleet.
	w.Header().Set("Cache-Control", "no-store")
	if err := sonarTemplates.ExecuteTemplate(w, "feed", p.sonarView(r)); err != nil {
		fmt.Fprintf(w, "\n<!-- the feed could not be rendered: %v -->\n", err)
	}
}

// registerPages mounts the panel's HTML surface on the mux.
//
// This is the first HTML the daemon serves: until this ticket, web.Assets had
// no caller at all and the panel was a JSON API with an embedded template
// nobody read. The eight screens of spec FR4 are untouched and `/` is left
// free on purpose -- the root belongs to the Mesa (FR4.1), whose ticket is
// still in the backlog, and squatting it here would cost that ticket a
// migration.
func registerPages(mux *http.ServeMux, p *Panel) {
	mux.HandleFunc("GET /sonar", p.sonarPage)
	mux.HandleFunc("GET /sonar/feed", p.sonarFragment)

	static, err := fs.Sub(web.Assets, "static")
	if err != nil {
		// The FS is embedded at build time, so this cannot fail in a built
		// binary. Panicking beats serving a panel with no stylesheet and no
		// explanation.
		panic("web assets have no static directory: " + err.Error())
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
}
