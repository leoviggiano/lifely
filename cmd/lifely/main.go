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
	"syscall"
	"time"

	"github.com/leoviggiano/lifely/internal/runtime"
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
  lifely serve [--port N] [--owner tribunal|manual]   start the panel
  lifely status                                       report whether the panel is up
  lifely stop --owner manual|tribunal [--force]       stop the panel
`)
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	port := fs.Int("port", defaultPort, "port to listen on")
	owner := fs.String("owner", string(runtime.OwnerManual), "who started this daemon: tribunal or manual")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
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
	if _, perr := runtime.Peek(); perr != nil && !errors.Is(perr, runtime.ErrNoMarker) && !runtime.IsCorrupt(perr) {
		return fmt.Errorf("could not read the daemon marker: %w", perr)
	}
	if live, ok := runtime.Running(); ok {
		return announceReuse(live, who, fs, *port)
	}

	// Loopback only: the panel is never reachable from the network.
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
	if err != nil {
		return fmt.Errorf("listening on 127.0.0.1:%d: %w", *port, err)
	}
	bound := listener.Addr().(*net.TCPAddr).Port

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
				return fmt.Errorf("another lifely came up and left while I was registering; try again")
			}
			return announceReuse(live, who, fs, *port)
		}
		return fmt.Errorf("registering the daemon: %w", err)
	}
	// Only ever clear our own registration (see RemoveIfOwn).
	defer func() { _ = runtime.RemoveIfOwn(os.Getpid()) }()

	httpServer := &http.Server{Handler: server.New(bound, string(who))}
	errs := make(chan error, 1)
	go func() {
		err := httpServer.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errs <- err
	}()

	fmt.Printf("lifely serving at http://127.0.0.1:%d (owner: %s)\n", bound, who)

	// Named `signals`, not `stop`: `stop` is the command function, and
	// shadowing it here makes the two impossible to tell apart at a glance.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	select {
	case err := <-errs:
		return err
	case <-signals:
	}

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
	// A corrupt marker is not a dead end: Running() heals it and reports no
	// daemon, which is the honest answer. Only a marker we could not READ --
	// permissions, I/O -- is a failure the caller has to hear about.
	if _, err := runtime.Peek(); err != nil && !errors.Is(err, runtime.ErrNoMarker) && !runtime.IsCorrupt(err) {
		return fmt.Errorf("could not read the daemon marker: %w", err)
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
		return err
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
	// pre-check here would print "segue de pe" for a daemon it never probed.
	switch err = live.Stop(asker); {
	case err == nil:
		fmt.Printf("lifely (pid %d, owner: %s) received the stop request\n", live.PID, live.Owner)
		return nil
	case errors.Is(err, runtime.ErrGone):
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
				fmt.Printf("the marker for pid %d changed under us; nothing was done\n", live.PID)
			case runtime.ForceCleared:
				// Do not repeat `what` here: it came from the probe BEFORE the
				// force, and ForceCleared covers both "somebody else's" and
				// "gone". Say only what is certain -- the marker is gone and
				// nothing of ours was signalled.
				fmt.Printf("marker for pid %d cleared (--force); no daemon of ours was there, and nothing was signalled\n", live.PID)
			default:
				fmt.Printf("SIGTERM sent to pid %d (--force); the process %s\n", live.PID, what)
			}
			return nil
		case *force:
			// The flag was given and the guard still refused: it was
			// ownership, not identity. Suggesting --force again would send the
			// caller in a circle.
			fmt.Printf("lifely did not touch pid %d: %v\n", live.PID, runtime.ErrNotOwner)
		default:
			fmt.Printf("lifely did not touch pid %d: the process %s (%v) -- use --force if you are sure\n", live.PID, what, err)
		}
		return errRefused
	}
	return err
}

// errRefused reports a stop deliberately not performed. It exits non-zero so a
// script can tell refusal from success; the reason is already printed.
var errRefused = errors.New("stop refused")
