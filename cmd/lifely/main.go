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
  lifely stop [--owner tribunal|manual]               derruba o painel
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
		// The FR7.3 guarantee lives in runtime.TakeOver, not here: a copy of
		// this decision inline would be a second implementation, and the test
		// that pins it would be pinning the copy.
		live, moved, err := runtime.TakeOver(live, who)
		if err != nil {
			if errors.Is(err, runtime.ErrChanged) || errors.Is(err, runtime.ErrNoMarker) {
				// Exit non-zero: nothing is serving on our behalf, and telling
				// a script "the panel is up" when it is not is the worse lie.
				return fmt.Errorf("o daemon mudou enquanto eu olhava; rode `lifely serve` de novo")
			}
			return fmt.Errorf("transferindo a posse do daemon: %w", err)
		}
		switch {
		case moved:
			fmt.Printf("lifely ja esta de pe em http://127.0.0.1:%d -- reusando, e a posse passa a ser sua (o fecho do tribunal nao o derruba mais)\n", live.Port)
		case portWasSet(fs) && *port != live.Port:
			// Say it out loud: silently serving a port other than the one
			// asked for is how somebody ends up curling an empty address.
			fmt.Printf("lifely ja esta de pe em http://127.0.0.1:%d (dono: %s) -- reusando; a porta %d que voce pediu foi ignorada\n", live.Port, live.Owner, *port)
		default:
			fmt.Printf("lifely ja esta de pe em http://127.0.0.1:%d (dono: %s) -- reusando\n", live.Port, live.Owner)
		}
		return nil
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
			// Re-read and run the same take-over as the normal reuse path:
			// losing a race must not cost the founder the ownership transfer
			// he would have got a millisecond earlier.
			live, ok := runtime.Running()
			if !ok {
				return fmt.Errorf("outro lifely subiu e saiu enquanto eu registrava; tente de novo")
			}
			if live, moved, terr := runtime.TakeOver(live, who); terr == nil && moved {
				fmt.Printf("outro lifely subiu primeiro em http://127.0.0.1:%d -- reusando, e a posse passa a ser sua\n", live.Port)
			} else {
				fmt.Printf("outro lifely subiu primeiro em http://127.0.0.1:%d (dono: %s) -- reusando o dele\n", live.Port, live.Owner)
			}
			return nil
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
	// Who is asking cannot be guessed, and the two failure modes pull in
	// opposite directions: defaulting to `manual` lets a session-close hook
	// that forgot the flag kill the founder's panel; defaulting to `tribunal`
	// makes the plain interactive pair `serve` then `stop` always refuse.
	//
	// So it is not defaulted at all when nobody is watching: at a terminal the
	// asker is the person typing (`manual`); from a script the flag is
	// REQUIRED, and forgetting it refuses instead of guessing.
	owner := fs.String("owner", "", "who is asking: manual (stops anything) or tribunal (only what it started); required when not run from a terminal")
	force := fs.Bool("force", false, "stop even when the process at that pid cannot be identified")
	if err := fs.Parse(args); err != nil {
		// `-h` is a request, not a failure: flag already printed the usage.
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *owner == "" {
		if !interactive() {
			return errors.New("rode com --owner manual ou --owner tribunal: fora de um terminal eu nao adivinho quem pede")
		}
		*owner = string(runtime.OwnerManual)
	}
	asker, err := parseOwner(*owner)
	if err != nil {
		return err
	}

	live, ok := runtime.Running()
	if !ok {
		fmt.Println("lifely nao esta de pe")
		return nil
	}

	// The tribunal closing its session must not take down a server the
	// founder started by hand (spec FR7.3).
	switch err = live.Stop(asker); {
	case err == nil:
		fmt.Printf("lifely (pid %d, dono: %s) recebeu o pedido de parada\n", live.PID, live.Owner)
		return nil
	case errors.Is(err, runtime.ErrGone):
		fmt.Println("lifely nao esta mais de pe")
		return nil
	case errors.Is(err, runtime.ErrNotOwner):
		// The exit code carries the outcome: a script closing the tribunal
		// must tell "stopped" from "refused, still running".
		fmt.Printf("lifely segue de pe em http://127.0.0.1:%d: %v\n", live.Port, err)
		return errRefused
	case errors.Is(err, runtime.ErrUnidentified):
		if *force {
			if ferr := live.ForceStop(); ferr != nil {
				return ferr
			}
			fmt.Printf("lifely (pid %d) parado por --force, sem confirmacao de identidade\n", live.PID)
			return nil
		}
		fmt.Printf("lifely nao mexeu no pid %d: %v (use --force se tiver certeza)\n", live.PID, err)
		return errRefused
	}
	return err
}

// interactive reports whether a person is typing at a terminal.
func interactive() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// errRefused reports a stop deliberately not performed. It exits non-zero so a
// script can tell refusal from success; the reason is already printed.
var errRefused = errors.New("stop recusado")
