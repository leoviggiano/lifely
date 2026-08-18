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
	fmt.Fprint(os.Stderr, `lifely -- painel local de pendências e orquestrador do desenvolvimento

Uso:
  lifely serve [--port N] [--owner tribunal|manual]   sobe o painel
  lifely status                                       diz se ha painel de pe
  lifely stop --owner manual|tribunal [--force]       derruba o painel
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
				return fmt.Errorf("outro lifely subiu e saiu enquanto eu registrava; tente de novo")
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

	fmt.Printf("lifely servindo em http://127.0.0.1:%d (dono: %s)\n", bound, who)

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
			return fmt.Errorf("o daemon mudou enquanto eu olhava; rode `lifely serve` de novo")
		}
		// Never swallow the rest: an unexpected failure here means the marker
		// is in a state nobody understands, and reporting "reusando" would
		// hide it behind good news.
		return fmt.Errorf("transferindo a posse do daemon: %w", err)
	}

	// Both facts can be true at once, and the port notice is the one that
	// changes what the caller does next -- so it is appended, not replaced.
	ignored := ""
	if portWasSet(fs) && wanted != live.Port {
		ignored = fmt.Sprintf("; a porta %d que voce pediu foi ignorada", wanted)
	}

	switch {
	case moved:
		fmt.Printf("lifely ja esta de pe em http://127.0.0.1:%d -- reusando, e a posse passa a ser sua (o fecho do tribunal nao o derruba mais)%s\n", live.Port, ignored)
	case ignored != "":
		fmt.Printf("lifely ja esta de pe em http://127.0.0.1:%d (dono: %s) -- reusando%s\n", live.Port, live.Owner, ignored)
	default:
		fmt.Printf("lifely ja esta de pe em http://127.0.0.1:%d (dono: %s) -- reusando\n", live.Port, live.Owner)
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
		return fmt.Errorf("nao consegui ler o marcador do daemon: %w", err)
	}
	live, ok := runtime.Running()
	if !ok {
		fmt.Println("lifely nao esta de pe")
		return nil
	}
	fmt.Printf("lifely de pe em http://127.0.0.1:%d (pid %d, dono: %s, versao %s)\n",
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
		return errors.New("diga quem pede: --owner manual (voce) ou --owner tribunal (o fecho da sessao)")
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
		fmt.Println("lifely nao esta de pe")
		return nil
	case runtime.IsCorrupt(err):
		// Heal instead of dead-ending: an unparseable marker would otherwise
		// make `stop` fail forever, and the file it trips on is ours to clear.
		runtime.Running()
		fmt.Println("marcador ilegivel foi descartado; lifely nao esta de pe")
		return nil
	case err != nil:
		// A marker we could not read is not the same as no marker: saying
		// "nao esta de pe" with exit 0 would tell a script the panel is down
		// when the truth is that we do not know.
		return fmt.Errorf("nao consegui ler o marcador do daemon: %w", err)
	}

	// The tribunal closing its session must not take down a server the
	// founder started by hand (spec FR7.3).
	// Marker.Stop orders this correctly: liveness first, then ownership. A
	// pre-check here would print "segue de pe" for a daemon it never probed.
	switch err = live.Stop(asker); {
	case err == nil:
		fmt.Printf("lifely (pid %d, dono: %s) recebeu o pedido de parada\n", live.PID, live.Owner)
		return nil
	case errors.Is(err, runtime.ErrGone):
		fmt.Println("lifely nao esta mais de pe")
		return nil
	case errors.Is(err, runtime.ErrNotOwner):
		// Reached only after the probe said the daemon is alive, so the
		// message can say so honestly.
		fmt.Printf("lifely segue de pe em http://127.0.0.1:%d: %v\n", live.Port, err)
		return errRefused
	case errors.Is(err, runtime.ErrForeign), errors.Is(err, runtime.ErrUnidentified):
		// One shape, not two: both mean "the pid is not a daemon we can act on
		// normally", and the only difference is what we tell the caller.
		what := "e de outro programa"
		if errors.Is(err, runtime.ErrUnidentified) {
			what = "nao pode ser identificado"
		}
		switch {
		case *force && live.MayStop(asker):
			if ferr := live.ForceStop(asker); ferr != nil {
				return ferr
			}
			fmt.Printf("pedido de limpeza do marcador do pid %d enviado (--force); o processo %s\n", live.PID, what)
			return nil
		case *force:
			// The flag was given and the guard still refused: it was
			// ownership, not identity. Suggesting --force again would send the
			// caller in a circle.
			fmt.Printf("lifely nao mexeu no pid %d: %v\n", live.PID, runtime.ErrNotOwner)
		default:
			fmt.Printf("lifely nao mexeu no pid %d: o processo %s (%v) -- use --force se tiver certeza\n", live.PID, what, err)
		}
		return errRefused
	}
	return err
}

// errRefused reports a stop deliberately not performed. It exits non-zero so a
// script can tell refusal from success; the reason is already printed.
var errRefused = errors.New("stop recusado")
