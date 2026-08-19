// Command lifely serves the local panel that shows the house's pending
// decisions and orchestrates the development sessions behind them.
//
// It is a house tool: local, loopback-only, and stateless by design -- the
// truth lives in the tribunal's files and in ject, never here.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/leoviggiano/lifely/internal/pendency"
	"github.com/leoviggiano/lifely/internal/runtime"
	scanpkg "github.com/leoviggiano/lifely/internal/scan"
	"github.com/leoviggiano/lifely/internal/server"
)

const defaultPort = 7777

func main() {
	if err := run(os.Args[1:]); err != nil {
		// A refusal is not a malfunction: the command already explained
		// itself, so it exits with its own code and prints nothing more.
		// Exit 3 lets a script tell "stopped" from "refused, still running".
		if errors.Is(err, errRefused) {
			os.Exit(3)
		}
		// flag.ContinueOnError already wrote the message and the usage to
		// stderr; printing it again under "lifely:" says the same thing twice
		// and buries the usage between two copies of one sentence.
		// errIncomplete has already printed the headline AND the source list;
		// main adding "lifely: the sweep could not read every source" made a
		// partial sweep say the same thing three times.
		if errors.Is(err, flag.ErrHelp) || errors.Is(err, errIncomplete) || alreadyReported(err) {
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "lifely:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return errors.New("no command given")
	}
	switch args[0] {
	case "serve":
		return serve(args[1:])
	case "scan":
		return scanCmd(args[1:])
	case "status":
		return status()
	case "stop":
		return stop(args[1:])
	case "-h", "--help", "help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `lifely -- local panel of pending decisions and orchestrator of the work

Usage:
  lifely serve --owner manual|tribunal [--port N] [--root DIR]
                                                      start the panel
  lifely scan [--root DIR]                            sweep the sources and print the board
                                                      (exits 1 if any source could not be read)
  lifely status                                       report whether the panel is up
  lifely stop --owner manual|tribunal [--force]       stop the panel
`)
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	port := fs.Int("port", defaultPort, "port to listen on")
	owner := fs.String("owner", "", "who started this daemon: manual (yours) or tribunal (the session) -- required")
	root := fs.String("root", defaultRoot(), "the record repository to sweep")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		// Wrapped so main does not print what flag already printed.
		return &flagParseError{err}
	}

	// The README and usage() both call --owner mandatory here; the default
	// quietly made it optional, so a bare `lifely serve` registered as manual
	// and the tribunal's own close would then refuse to clean up after it.
	// Same reasoning as stop: never guess who is asking.
	if *owner == "" {
		return errors.New("say who is starting this daemon: --owner manual (you) or --owner tribunal (the session)")
	}
	who, err := parseOwner(*owner)
	if err != nil {
		return err
	}

	// One instance, never two: a second daemon would answer with a second
	// scan of the same sources and split the founder's attention between two
	// URLs. Reuse what is already up and say where it is (spec FR7.4).
	//
	// This check is the friendly path, not the lock. The lock is runtime.Claim
	// below: an exclusive create, decided by the filesystem for every caller
	// at once, including two `serve --port` asking for different ports. This
	// check exists to give a good message, not to guarantee anything.
	// Absence and failure are different answers, and serve acts on them very
	// differently: on absence it starts a daemon, on failure it would start a
	// SECOND one while the first is still up.
	if err := markerReadable(); err != nil {
		return err
	}
	if live, ok := runtime.Running(); ok {
		return announceReuse(live, who, fs, *port)
	}

	// Loopback only: the panel is never reachable from the network.
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
	if err != nil {
		// The marker decides who serves, not the port (spec FR7.4). Binding
		// happens first for a good reason -- claiming before we know the
		// bound port would publish a provisional one to `status` and `stop`
		// -- so the ordering is repaired here instead: a bind that loses to a
		// daemon already registered is a reuse, not a failure, and the caller
		// gets the same announcement it would have got a millisecond earlier.
		if live, ok := runtime.Running(); ok {
			return announceReuse(live, who, fs, *port)
		}
		return fmt.Errorf("listening on 127.0.0.1:%d: %w", *port, err)
	}
	bound := listener.Addr().(*net.TCPAddr).Port

	// Arm the signal handler BEFORE the marker is published. Claim makes this
	// pid visible to `lifely stop`, and a SIGTERM arriving before Notify is
	// installed kills the process with the default disposition -- no drain, no
	// deregistration, and the marker it just wrote survives as an orphan.
	//
	// Named `signals`, not `stop`: `stop` is the command function, and
	// shadowing it here makes the two impossible to tell apart at a glance.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)

	// Every path below that returns without serving must give the port back:
	// the reuse announcement told the caller to use the daemon that IS up, and
	// holding its port hostage would make the next honest `serve` fail to bind.
	claimed := false
	defer func() {
		if !claimed {
			_ = listener.Close()
		}
	}()

	if err := runtime.Claim(runtime.Marker{
		PID:     os.Getpid(),
		Port:    bound,
		Owner:   who,
		Version: server.Version,
	}); err != nil {
		if errors.Is(err, runtime.ErrAlreadyRunning) {
			// Another daemon won the race between our Running() check and
			// here. One instance, decided by the filesystem (spec FR7.4).
			//
			// The same announcement as the normal path, from the same
			// function: losing a race must not cost the founder the ownership
			// transfer he would have had a millisecond earlier, and a second
			// copy of this logic would drift from the first (it already did).
			live, ok := runtime.Running()
			if !ok {
				// "Try again" is a lie when the marker is not going anywhere.
				// A surviving marker whose pid the OS recycled blocks Claim
				// forever: Running refuses to call it ours (right) and
				// refuses to erase it (also right), so the caller has to be
				// told what actually stands in the way and how to clear it.
				if stale, perr := runtime.Peek(); perr == nil {
					// --owner manual, never `who`: MayStop admits manual
					// unconditionally, while the owner serve happened to be
					// called with may be refused by the very guard this hint
					// is trying to get the caller past. A way out that does
					// not open is not a way out.
					return fmt.Errorf("a marker for pid %d is in the way and it is not a lifely daemon; clear it with `lifely stop --force --owner manual`", stale.PID)
				}
				return fmt.Errorf("another lifely came up and left while I was registering; try again")
			}
			return announceReuse(live, who, fs, *port)
		}
		return fmt.Errorf("registering the daemon: %w", err)
	}
	claimed = true
	// Only ever clear our own registration (see RemoveIfOwn).
	defer func() { _ = runtime.RemoveIfOwn(os.Getpid()) }()

	// The same sweep `scan --root` already parameterises. Hardcoding it here
	// meant the daemon could only ever read one repository, so no test and no
	// second checkout could ever be served -- for a sweep whose root is a flag
	// three commands over.
	panel := server.NewPanel(*root, scanpkg.CLI)
	httpServer := &http.Server{Handler: server.New(bound, string(who), panel)}
	errs := make(chan error, 1)
	go func() {
		err := httpServer.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errs <- err
	}()

	fmt.Printf("lifely serving at http://127.0.0.1:%d (owner: %s)\n", bound, who)

	select {
	case err := <-errs:
		return err
	case <-signals:
	}

	// Deregister BEFORE draining. Shutdown closes the listener first and then
	// waits for in-flight requests, and for those seconds the marker still
	// advertised a panel that refuses every new connection -- `status` would
	// print a URL nothing answers, and `serve` would reuse a dying daemon
	// instead of starting a live one. The deferred RemoveIfOwn stays: it is
	// the guard for every OTHER exit path, and removing twice is harmless.
	_ = runtime.RemoveIfOwn(os.Getpid())

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return httpServer.Shutdown(shutdownCtx)
}

// announceReuse takes over a running daemon when the founder joins it by hand,
// and says what happened. It is the ONE place that decides this, so the normal
// path and the lost-race path can never diverge.
func announceReuse(live runtime.Marker, who runtime.Owner, fs *flag.FlagSet, wanted int) error {
	live, moved, err := runtime.TakeOver(live, who)
	if err != nil {
		if errors.Is(err, runtime.ErrChanged) || errors.Is(err, runtime.ErrNoMarker) {
			// Exit non-zero: nothing is serving on our behalf, and telling a
			// script "the panel is up" when it is not is the worse lie.
			return fmt.Errorf("the daemon changed while I was looking; run `lifely serve` again")
		}
		// Never swallow the rest: an unexpected failure here means the marker
		// is in a state nobody understands, and reporting "reusando" would
		// hide it behind good news.
		return fmt.Errorf("transferring daemon ownership: %w", err)
	}

	// Both facts can be true at once, and the port notice is the one that
	// changes what the caller does next -- so it is appended, not replaced.
	ignored := ""
	if portWasSet(fs) && wanted != live.Port {
		ignored = fmt.Sprintf("; the port %d you asked for was ignored", wanted)
	}

	switch {
	case moved:
		fmt.Printf("lifely is already running at http://127.0.0.1:%d -- reusing it; ownership is now yours (closing the tribunal will not stop it)%s\n", live.Port, ignored)
	case ignored != "":
		fmt.Printf("lifely is already running at http://127.0.0.1:%d (owner: %s) -- reusing it%s\n", live.Port, live.Owner, ignored)
	default:
		fmt.Printf("lifely is already running at http://127.0.0.1:%d (owner: %s) -- reusing it\n", live.Port, live.Owner)
	}
	return nil
}

// portWasSet reports whether --port was given explicitly, so that reusing a
// daemon on another port is only announced when somebody actually asked.
func portWasSet(fs *flag.FlagSet) bool {
	set := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "port" {
			set = true
		}
	})
	return set
}

func parseOwner(owner string) (runtime.Owner, error) {
	who := runtime.Owner(owner)
	if who != runtime.OwnerTribunal && who != runtime.OwnerManual {
		return "", fmt.Errorf("--owner must be %q or %q, got %q", runtime.OwnerTribunal, runtime.OwnerManual, owner)
	}
	return who, nil
}

func status() error {
	// Same distinction `stop` makes: no marker is an answer, an unreadable
	// marker is not. Running() collapses both into false, so ask first.
	if err := markerReadable(); err != nil {
		return err
	}
	live, ok := runtime.Running()
	if !ok {
		fmt.Println("lifely is not running")
		return nil
	}
	fmt.Printf("lifely running at http://127.0.0.1:%d (pid %d, owner: %s, version %s)\n",
		live.Port, live.PID, live.Owner, live.Version)
	return nil
}

func stop(args []string) error {
	fs := flag.NewFlagSet("stop", flag.ContinueOnError)
	owner := fs.String("owner", "", "who is asking: manual (stops anything) or tribunal (only what it started) -- required")
	force := fs.Bool("force", false, "stop even when the process at that pid cannot be identified")
	if err := fs.Parse(args); err != nil {
		// `-h` is a request, not a failure: flag already printed the usage.
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		// Wrapped so main does not print what flag already printed.
		return &flagParseError{err}
	}
	// No guessing, ever. A TTY on stdin does not prove a person is typing --
	// cron, CI and a tribunal hook can all have one -- and the comment right
	// above this used to say "who is asking cannot be guessed" while the code
	// guessed. One flag costs the founder four words; a wrong guess costs him
	// the panel he was reading.
	if *owner == "" {
		return errors.New("say who is asking: --owner manual (you) or --owner tribunal (the session close)")
	}
	asker, perr := parseOwner(*owner)
	if perr != nil {
		return perr
	}

	// Ownership is decided BEFORE anything else, including --force.
	//
	// --force relaxes IDENTITY ("I could not tell what that pid is"), never
	// OWNERSHIP. An escape that lets the tribunal stop the founder's panel is
	// the FR7.3 bug wearing a flag -- and this package has now produced that
	// bug twice from the same hatch.
	// Peek, not Running(): Running() answers "is a daemon up?" and hides a
	// stale marker behind a false. `stop` has to be able to SEE that marker --
	// otherwise the foreign and gone branches below are unreachable and
	// `--force` has nothing to act on.
	live, err := runtime.Peek()
	switch {
	case errors.Is(err, runtime.ErrNoMarker):
		fmt.Println("lifely is not running")
		return nil
	case runtime.IsCorrupt(err):
		// Heal instead of dead-ending: an unparseable marker would otherwise
		// make `stop` fail forever, and the file it trips on is ours to clear.
		runtime.Running()
		if _, still := runtime.Peek(); runtime.IsCorrupt(still) {
			// Say what is true: the healing may fail (permissions, a racing
			// writer), and claiming "descartado" would send the caller away
			// believing a file that is still there is gone.
			fmt.Println("the marker is unreadable and I could not clear it; lifely is not running")
			return nil
		}
		fmt.Println("the unreadable marker was cleared; lifely is not running")
		return nil
	case err != nil:
		// A marker we could not read is not the same as no marker: saying
		// "not running" with exit 0 would tell a script the panel is down
		// when the truth is that we do not know.
		return fmt.Errorf("could not read the daemon marker: %w", err)
	}

	// The tribunal closing its session must not take down a server the
	// founder started by hand (spec FR7.3).
	// Marker.Stop orders this correctly: liveness first, then ownership. A
	// pre-check here would print "still up" for a daemon it never probed.
	switch err = live.Stop(asker); {
	case err == nil:
		fmt.Printf("lifely (pid %d, owner: %s) received the stop request\n", live.PID, live.Owner)
		return nil
	case errors.Is(err, runtime.ErrGone):
		// Clear what we just proved is an orphan. stop() reads through Peek,
		// which never heals, so without this every later `stop` repeats the
		// same sentence about a file nothing will ever remove.
		// Running() erases a gone marker as part of answering, and reports a
		// daemon only if one registered while we were looking -- which is a
		// different outcome and must not be announced as a stop.
		if live, ok := runtime.Running(); ok {
			fmt.Printf("lifely (pid %d) registered while I was looking; nothing was stopped\n", live.PID)
			return errRefused
		}
		fmt.Println("lifely is no longer running")
		return nil
	case errors.Is(err, runtime.ErrNotOwner):
		// Reached only after the probe said the daemon is alive, so the
		// message can say so honestly.
		fmt.Printf("lifely is still running at http://127.0.0.1:%d: %v\n", live.Port, err)
		return errRefused
	case errors.Is(err, runtime.ErrForeign), errors.Is(err, runtime.ErrUnidentified):
		// One shape, not two: both mean "the pid is not a daemon we can act on
		// normally", and the only difference is what we tell the caller.
		what := "belongs to another program"
		if errors.Is(err, runtime.ErrUnidentified) {
			what = "cannot be identified"
		}
		switch {
		case *force && live.MayStop(asker):
			outcome, ferr := live.ForceStop(asker)
			if ferr != nil && outcome == runtime.ForceNothing {
				return ferr
			}
			if ferr != nil {
				// The action landed and the cleanup did not. Report BOTH:
				// returning only the error would tell the caller nothing
				// happened, about a signal that was already delivered.
				fmt.Fprintf(os.Stderr, "lifely: %v\n", ferr)
			}
			// Narrate what force DID, not what the earlier probe expected:
			// ForceStop probes again and reports its own outcome, and the two
			// paths differ in a way the caller has to know -- one clears a
			// marker, the other kills a process.
			switch outcome {
			case runtime.ForceNothing:
				// Nothing happened, so the exit code must not say success: a
				// script closing the panel has to tell "done" from "the world
				// moved under me and I did not act".
				fmt.Printf("the marker for pid %d changed under us; nothing was done\n", live.PID)
				return errRefused
			case runtime.ForceCleared:
				// Do not repeat `what` here: it came from the probe BEFORE the
				// force, and ForceCleared covers both "somebody else's" and
				// "gone". Say only what is certain -- the marker is gone and
				// nothing of ours was signalled.
				fmt.Printf("marker for pid %d cleared (--force); no daemon of ours was there, and nothing was signalled\n", live.PID)
			default:
				// Say only what force itself established. `what` came from the
				// probe taken BEFORE ForceStop ran, and the branch above
				// already refuses to repeat it for exactly this reason.
				fmt.Printf("SIGTERM sent to pid %d (--force)\n", live.PID)
			}
			return nil
		case *force:
			// The flag was given and the guard still refused: it was
			// ownership, not identity. Suggesting --force again would send the
			// caller in a circle.
			fmt.Printf("lifely did not touch pid %d: %v\n", live.PID, runtime.ErrNotOwner)
		default:
			// Only suggest --force where it can actually work. MayStop is the
			// same guard --force will consult, so offering it to a caller it
			// will refuse just sends them in a circle -- the exact circle the
			// *force branch above already avoids.
			hint := ""
			if live.MayStop(asker) {
				hint = " -- use --force if you are sure"
			}
			fmt.Printf("lifely did not touch pid %d: the process %s (%v)%s\n", live.PID, what, err, hint)
		}
		return errRefused
	}
	return err
}

// markerReadable separates the two answers serve and status spell the same
// way: a marker that is absent or corrupt is not a failure (Running heals the
// corrupt one and honestly reports no daemon), while one we could not READ --
// permissions, I/O -- must reach the caller. `stop` deliberately does NOT use
// this: it switches on the same read to say something different about each
// case, and folding it in here would move that difference, not remove it.
func markerReadable() error {
	_, err := runtime.Peek()
	if err != nil && !errors.Is(err, runtime.ErrNoMarker) && !runtime.IsCorrupt(err) {
		return fmt.Errorf("could not read the daemon marker: %w", err)
	}
	return nil
}

// scanCmd prints one sweep to the terminal.
//
// The panel is the real surface, but a sweep has to be inspectable before any
// UI exists -- a discovery bug is far easier to see in a flat list than in a
// rendered page.
func scanCmd(args []string) error {
	fs := flag.NewFlagSet("scan", flag.ContinueOnError)
	root := fs.String("root", defaultRoot(), "the record repository to sweep")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		// Wrapped like serve and stop: the third caller of the same pattern,
		// swept here because a defect in two places is a defect in three.
		return &flagParseError{err}
	}

	res, _ := scanpkg.All(*root, scanpkg.CLI)

	// The HEADLINE has to carry it, not just the source list at the bottom.
	// A sweep that could not read something is not a clean sweep, and the
	// invariant this command was built on says it in full: "nothing pending"
	// must never be the answer to "I never looked".
	unread := countUnreadable(res.Sources)
	incomplete := sweepIncomplete(res.Sources)
	// Counted apart from `unread` on purpose: a budget cut is not a failure
	// and must NOT reach the exit code -- but it does mean the board is not
	// whole, and the headline is where "not whole" has to be visible.
	partial := 0
	for _, s := range res.Sources {
		if s.Note != "" {
			partial++
		}
	}

	if len(res.Pendencies) == 0 {
		if incomplete {
			fmt.Printf("Nothing pending in what could be read -- but %d source(s) could not be read. This is NOT a clean board.\n", unread)
			printSources(res.Sources)
			return errIncomplete
		}
		fmt.Println("Nothing pending. The sources were swept just now and nothing is waiting on a decision. Zero is a result.")
		printSources(res.Sources)
		return nil
	}

	// The count leads, and the gap in it leads with it: a reader who stops at
	// the first line must not walk away believing the board is whole.
	switch {
	case unread > 0 && partial > 0:
		fmt.Printf("%d pendencies · swept at %s · INCOMPLETE: %d source(s) could not be read, %d cut short\n",
			len(res.Pendencies), res.At.Format("15:04"), unread, partial)
	case unread > 0:
		fmt.Printf("%d pendencies · swept at %s · INCOMPLETE: %d source(s) could not be read\n",
			len(res.Pendencies), res.At.Format("15:04"), unread)
	case partial > 0:
		fmt.Printf("%d pendencies · swept at %s · PARTIAL: %d source(s) were cut short by the sweep budget\n",
			len(res.Pendencies), res.At.Format("15:04"), partial)
	default:
		fmt.Printf("%d pendencies · swept at %s\n", len(res.Pendencies), res.At.Format("15:04"))
	}
	for _, g := range []pendency.Blocker{pendency.Founder, pendency.Gate, pendency.AI, pendency.Hygiene} {
		var inGroup []pendency.Pendency
		for _, p := range res.Pendencies {
			if p.Blocks == g {
				inGroup = append(inGroup, p)
			}
		}
		if len(inGroup) == 0 {
			continue
		}
		fmt.Printf("\n%s (%d)\n", groupLabel[g], len(inGroup))
		for _, p := range inGroup {
			fmt.Printf("  %s %s\n", pad(trim(p.Title, 48), 48), p.Source)
		}
	}

	printSources(res.Sources)
	if incomplete {
		return errIncomplete
	}
	return nil
}

// countUnreadable counts sources that could not be READ. A source carrying
// only a Note was read and not exhausted, which is a different fact and must
// not reach the exit code.
func countUnreadable(sources []scanpkg.SourceState) int {
	n := 0
	for _, s := range sources {
		if s.Err != "" {
			n++
		}
	}
	return n
}

// sweepIncomplete is the predicate the exit code uses.
//
// It exists so the contract can be tested without running a whole sweep -- and
// scanCmd CALLS it, because a predicate that only its own test invokes proves
// nothing about the binary. That was the shape this function shipped in for
// one round.
func sweepIncomplete(sources []scanpkg.SourceState) bool {
	return countUnreadable(sources) > 0
}

func printSources(sources []scanpkg.SourceState) {
	if len(sources) == 0 {
		return
	}
	fmt.Print("\nSOURCES\n")
	for _, s := range sources {
		// Count AND error: `ledgers *.tsv` aggregates many files, so one
		// unreadable ledger must not erase the tally of the ones that were
		// read. Either fact alone leaves the reader with half the picture.
		// A source can carry BOTH: one project whose ticket detail failed AND
		// whose sweep was cut short. Printing only one of them dropped the
		// other silently -- the exact "either fact alone leaves the reader
		// with half the picture" this function was written to avoid.
		note := ""
		if s.Note != "" {
			note = " (partial -- " + s.Note + ")"
		}
		switch {
		case s.Err == "" && note != "":
			// Read, not exhausted. Distinct from UNREADABLE on purpose: this
			// one does not make the sweep incomplete-by-failure.
			fmt.Printf("  %s %d%s\n", pad(s.Name, 30), s.Count, note)
		case s.Err != "" && s.Count > 0:
			fmt.Printf("  %s %d (partial -- %s)%s\n", pad(s.Name, 30), s.Count, s.Err, note)
		case s.Err != "":
			fmt.Printf("  %s UNREADABLE: %s%s\n", pad(s.Name, 30), s.Err, note)
		default:
			fmt.Printf("  %s %d\n", pad(s.Name, 30), s.Count)
		}
	}
}

var groupLabel = map[pendency.Blocker]string{
	pendency.Founder: "WAITING ON THE FOUNDER",
	pendency.Gate:    "GATES AWAITING AN ANSWER",
	pendency.AI:      "AI PREPARES",
	pendency.Hygiene: "HYGIENE",
}

func defaultRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(home, "projects", "artifacts")
}

// pad widens a string to n columns counting runes.
//
// %-48s pads to a BYTE count, so an accented title -- which this command goes
// out of its way to truncate by rune -- still comes out misaligned: 20 runes
// of 25 bytes get 23 spaces instead of 28.
func pad(s string, n int) string {
	if d := n - len([]rune(s)); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

// trim truncates by runes, never by bytes: the sources are accented Portuguese,
// and a byte slice lands mid-rune and prints a replacement character.
func trim(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// alreadyReported says whether the flag package has already put this error in
// front of the user. There is no sentinel for a parse failure -- the package
// returns a bare errors.New -- so the check is on the parser having spoken,
// which is exactly what ContinueOnError guarantees.
func alreadyReported(err error) bool {
	var parse *flagParseError
	return errors.As(err, &parse)
}

// flagParseError wraps what fs.Parse returned so main can tell "the flags were
// wrong, and the flag package said so" from every other failure.
type flagParseError struct{ err error }

func (e *flagParseError) Error() string { return e.err.Error() }
func (e *flagParseError) Unwrap() error { return e.err }

// errIncomplete reports a sweep that ran but could not read every source. It
// exits non-zero because a script that treats a partial board as a clean one
// is exactly the failure the panel exists to prevent -- and the sources it did
// read are still printed, because a partial answer beats no answer.
var errIncomplete = errors.New("the sweep could not read every source")

// errRefused reports a stop deliberately not performed. It exits non-zero so a
// script can tell refusal from success; the reason is already printed.
var errRefused = errors.New("stop refused")
